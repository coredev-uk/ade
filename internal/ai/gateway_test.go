package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
)

// gateway is an httptest LiteLLM stand-in that records every chat request.
type gateway struct {
	srv *httptest.Server
	mu  sync.Mutex
	req []gwReq
	// reject, when set, answers a request carrying response_format with this
	// 400 body, like a gateway whose model has no JSON-schema support.
	reject string
	// reply is the assistant text per model; "" gives a summary.
	reply map[string]string
	delay time.Duration
}

type gwReq struct {
	Model          string
	ResponseFormat map[string]any
	Header         http.Header
}

func newGateway(t *testing.T) *gateway {
	t.Helper()
	g := &gateway{reply: map[string]string{}}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Model          string         `json:"model"`
			ResponseFormat map[string]any `json:"response_format"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		g.mu.Lock()
		g.req = append(g.req, gwReq{body.Model, body.ResponseFormat, r.Header.Clone()})
		g.mu.Unlock()
		if g.delay > 0 {
			time.Sleep(g.delay)
		}
		if g.reject != "" && body.ResponseFormat != nil {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, g.reject)
			return
		}
		text := g.reply[body.Model]
		if text == "" {
			text = "- a summary\nRisk: low"
		}
		w.Header().Set("x-litellm-response-cost", "0.0001")
		out, _ := json.Marshal(map[string]any{
			"model":   body.Model,
			"choices": []any{map[string]any{"message": map[string]any{"content": text}}},
			"usage":   map[string]any{"prompt_tokens": 50, "completion_tokens": 10},
		})
		w.Write(out)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *gateway) requests() []gwReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gwReq(nil), g.req...)
}

// useGateway points the env's profile at g with the given role models.
func (e *env) useGateway(g *gateway, models map[string]string) {
	e.t.Setenv("ADE_TEST_GW_KEY", "sk-gw")
	e.conf.AI.Profiles["p"] = config.AIProfile{
		Driver: "openai-compat", BaseURL: g.srv.URL + "/v1", KeyEnv: "ADE_TEST_GW_KEY",
		Models: models, Billing: config.AIBilling{Source: "litellm"},
	}
	e.svc.Open = func(ctx context.Context, p config.AIProfile) (Driver, error) {
		return DriverFor(ctx, p, g.srv.Client())
	}
}

func TestClassifyHitsTheClassifyModelAndSummarizeTheExplainModel(t *testing.T) {
	e := newEnv(t)
	g := newGateway(t)
	g.reply["small-classifier"] = `{"type":"bug","apps":["web"],"reason":"crash"}`
	e.useGateway(g, map[string]string{"classify": "small-classifier", "explain": "big-explainer"})

	cr, err := e.svc.Classify(context.Background(), "f", classifyIn("Search crashes on empty query", ""), ClassifyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if cr.Run == nil || cr.Run.Model != "small-classifier" || strings.Join(cr.Suggestion.Labels, ",") != "type::bug,app::web" {
		t.Fatalf("%+v", cr)
	}
	if _, err := e.svc.Run(context.Background(), summarizeJob("abc")); err != nil {
		t.Fatal(err)
	}
	reqs := g.requests()
	if len(reqs) != 2 || reqs[0].Model != "small-classifier" || reqs[1].Model != "big-explainer" {
		t.Fatalf("requests = %+v", reqs)
	}
	if reqs[0].ResponseFormat["type"] != "json_schema" {
		t.Fatalf("classify must ask for a json_schema answer: %+v", reqs[0].ResponseFormat)
	}
	if reqs[1].ResponseFormat != nil {
		t.Fatalf("a summary is prose, no response_format: %+v", reqs[1].ResponseFormat)
	}
	es := e.entries("ai.classify")
	if len(es) != 1 || es[0].AI.Model != "small-classifier" || es[0].Params["structured"] != true {
		t.Fatalf("audit = %+v", es)
	}
}

func TestClassifyFallsBackToTheExplainModel(t *testing.T) {
	e := newEnv(t)
	g := newGateway(t)
	g.reply["big-explainer"] = `{"type":"chore","apps":[],"reason":"r"}`
	e.useGateway(g, map[string]string{"explain": "big-explainer"})
	if _, err := e.svc.Classify(context.Background(), "f", classifyIn("Tidy the build", ""), ClassifyOpts{}); err != nil {
		t.Fatal(err)
	}
	if reqs := g.requests(); len(reqs) != 1 || reqs[0].Model != "big-explainer" {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestStructuredOutputFallsBackToTextOnceAndRemembers(t *testing.T) {
	e := newEnv(t)
	g := newGateway(t)
	g.reject = `{"error":{"message":"litellm.BadRequestError: response_format json_schema is not supported by this model","type":"invalid_request_error"}}`
	// The fenced answer is what an unconstrained small model tends to send.
	g.reply["small"] = "```json\n{\"type\":\"story\",\"apps\":[\"api\"],\"reason\":\"r\"}\n```"
	e.useGateway(g, map[string]string{"classify": "small", "explain": "big"})
	drv, err := e.svc.Open(context.Background(), e.conf.AI.Profiles["p"])
	if err != nil {
		t.Fatal(err)
	}
	e.svc.Open = func(context.Context, config.AIProfile) (Driver, error) { return drv, nil } // one driver: it remembers

	for i, title := range []string{"Add an export", "Add an import"} {
		cr, err := e.svc.Classify(context.Background(), "f", classifyIn(title, ""), ClassifyOpts{})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if strings.Join(cr.Suggestion.Labels, ",") != "type::story,app::api" {
			t.Fatalf("run %d: %+v", i, cr.Suggestion)
		}
	}
	// 1st classify: schema try (rejected) + plain retry; 2nd: plain only.
	reqs := g.requests()
	if len(reqs) != 3 || reqs[0].ResponseFormat == nil || reqs[1].ResponseFormat != nil || reqs[2].ResponseFormat != nil {
		t.Fatalf("requests = %+v", reqs)
	}
	if es := e.entries("ai.classify"); len(es) != 2 || es[0].Params["structured"] != false {
		t.Fatalf("audit = %+v", es)
	}
}

func TestOtherBadRequestsAreNotMistakenForAnUnsupportedSchema(t *testing.T) {
	e := newEnv(t)
	g := newGateway(t)
	g.reject = `{"error":{"message":"model not found","type":"invalid_request_error"}}` // says nothing of the format
	e.useGateway(g, map[string]string{"classify": "small", "explain": "big"})
	if _, err := e.svc.Classify(context.Background(), "f", classifyIn("Search crashes", ""), ClassifyOpts{}); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("err = %v", err)
	}
	if n := len(g.requests()); n != 1 {
		t.Fatalf("a model-not-found error must not be retried, got %d requests", n)
	}
}

func TestExtraHeadersAndTimeoutApplyToGatewayCalls(t *testing.T) {
	g := newGateway(t)
	p := config.AIProfile{
		Driver: "openai-compat", BaseURL: g.srv.URL + "/v1", KeyEnv: "ADE_TEST_GW_KEY",
		ExtraHeaders: map[string]string{"x-litellm-tags": "ade,triage", "X-Team": "platform"},
		Timeout:      config.Duration{Duration: 100 * time.Millisecond},
	}
	t.Setenv("ADE_TEST_GW_KEY", "sk-gw")
	d, err := DriverFor(context.Background(), p, g.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Complete(context.Background(), Call{Model: "m", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	h := g.requests()[0].Header
	if h.Get("x-litellm-tags") != "ade,triage" || h.Get("X-Team") != "platform" || h.Get("Authorization") != "Bearer sk-gw" {
		t.Fatalf("headers = %v", h)
	}
	g.delay = 400 * time.Millisecond
	if _, err := d.Complete(context.Background(), Call{Model: "m", Prompt: "hi"}); err == nil {
		t.Fatal("a call slower than the profile timeout must fail")
	}
}
