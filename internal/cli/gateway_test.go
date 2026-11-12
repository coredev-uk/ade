package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// gatewayStub is a LiteLLM stand-in: /v1/models and /v1/chat/completions.
type gatewayStub struct {
	srv    *httptest.Server
	mu     sync.Mutex
	models []string // models called, in order
}

func newGatewayStub(t *testing.T, offered ...string) *gatewayStub {
	t.Helper()
	g := &gatewayStub{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-gw-test" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"bad key"}}`)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			var data []map[string]string
			for _, m := range offered {
				data = append(data, map[string]string{"id": m})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "/v1/chat/completions":
			var body struct {
				Model string `json:"model"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			g.mu.Lock()
			g.models = append(g.models, body.Model)
			g.mu.Unlock()
			text := `{"type":"bug","apps":["web"],"reason":"crash"}`
			json.NewEncoder(w).Encode(map[string]any{
				"model":   body.Model,
				"choices": []any{map[string]any{"message": map[string]any{"content": text}}},
				"usage":   map[string]any{"prompt_tokens": 40, "completion_tokens": 8},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *gatewayStub) called() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.models...)
}

func TestInitWritesAGatewayProfile(t *testing.T) {
	e := newInitEnv(t)
	gw := newGatewayStub(t, "big-explainer", "small-classifier")
	t.Setenv("ADE_TEST_GW_KEY", "sk-gw-test")

	res, progress, err := e.init(e.args(
		"--ai-gateway", gw.srv.URL, "--ai-key-env", "ADE_TEST_GW_KEY",
		"--ai-model-explain", "big-explainer", "--ai-model-classify", "small-classifier",
	)...)
	if err != nil && !doctorOnly(err, res.Doctor) {
		t.Fatalf("init: %v\n%s", err, progress)
	}
	cfg, lerr := config.Load(e.configPath())
	if lerr != nil {
		t.Fatal(lerr)
	}
	name, p, perr := cfg.ActiveProfile()
	if perr != nil || name != "gateway" || p.Driver != "openai-compat" || p.BaseURL != gw.srv.URL+"/v1" ||
		p.KeyEnv != "ADE_TEST_GW_KEY" || p.Billing.Source != "litellm" {
		t.Fatalf("profile = %q %+v %v", name, p, perr)
	}
	if m, _ := p.Model("classify"); m != "small-classifier" {
		t.Fatalf("classify = %q", m)
	}
	if m, _ := p.Model("draft"); m != "big-explainer" {
		t.Fatalf("draft must fall back to explain, got %q", m)
	}
	if b, _ := os.ReadFile(e.configPath()); strings.Contains(string(b), "sk-gw-test") {
		t.Fatal("the gateway key was written to the config")
	}
	if res.AI == nil || res.AI.Profile != "gateway" || res.AI.Models["classify"] != "small-classifier" {
		t.Fatalf("json ai = %+v", res.AI)
	}
	if !strings.Contains(progress, "gateway ok:") {
		t.Fatalf("progress:\n%s", progress)
	}
}

func TestInitGatewayFlagErrorsWriteNothing(t *testing.T) {
	gw := newGatewayStub(t, "m1", "m2")
	t.Setenv("ADE_TEST_GW_KEY", "sk-gw-test")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"ai flags without a gateway": {[]string{"--ai-key-env", "ADE_TEST_GW_KEY"}, "--ai-gateway"},
		"no key source":              {[]string{"--ai-gateway", gw.srv.URL, "--ai-model-explain", "m1"}, "--ai-key-env"},
		"no explain model lists what the gateway offers": {
			[]string{"--ai-gateway", gw.srv.URL, "--ai-key-env", "ADE_TEST_GW_KEY"}, "m1, m2",
		},
		"unknown role flag": {[]string{"--ai-gateway", gw.srv.URL, "--ai-model-triage", "x"}, "unknown flag"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newInitEnv(t)
			t.Setenv("ADE_TEST_GW_KEY", "sk-gw-test")
			_, _, err := e.init(e.args(tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
			if _, serr := os.Stat(e.configPath()); serr == nil {
				t.Fatal("a failed init wrote the config")
			}
		})
	}
}

func TestInitGatewayWithAnUnreachableKeyStillWritesWhenModelsAreGiven(t *testing.T) {
	e := newInitEnv(t)
	gw := newGatewayStub(t, "m1")
	t.Setenv("ADE_TEST_GW_KEY", "wrong-key") // the gateway refuses it
	res, progress, err := e.init(e.args("--ai-gateway", gw.srv.URL, "--ai-key-env", "ADE_TEST_GW_KEY", "--ai-model-explain", "m1")...)
	if err != nil && !doctorOnly(err, res.Doctor) {
		t.Fatalf("init: %v\n%s", err, progress)
	}
	if !strings.Contains(progress, "could not list the gateway's models") {
		t.Fatalf("the unverified gateway must be said:\n%s", progress)
	}
}

func TestAIModelsMarksWhichRoleUsesWhichModel(t *testing.T) {
	e, _ := aiEnvFor(t, "[]")
	gw := newGatewayStub(t, "big-explainer", "small-classifier", "unused-model")
	t.Setenv("ADE_TEST_GW_KEY", "sk-gw-test")
	e.app.Config.AI.Profile = "gw"
	e.app.Config.AI.Profiles["gw"] = config.AIProfile{
		Driver: "openai-compat", BaseURL: gw.srv.URL + "/v1", KeyEnv: "ADE_TEST_GW_KEY",
		Models:  map[string]string{"explain": "big-explainer", "classify": "small-classifier", "draft": "retired-model"},
		Billing: config.AIBilling{Source: "litellm"},
	}
	out := e.mustAI("models")
	for _, want := range []string{
		"small-classifier  classify",
		"big-explainer     explain, plan (via explain), implement (via explain)",
		"warning: draft uses \"retired-model\", which the gateway does not list",
		"warning: review uses \"retired-model\"", // review falls back to draft
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if line := lineWith(out, "unused-model"); line != "unused-model" {
		t.Errorf("an unused model carries no role: %q", line)
	}

	asJSON(t)
	var js struct {
		Models []string
		Roles  []roleUse
	}
	if err := json.Unmarshal([]byte(e.mustAI("models")), &js); err != nil {
		t.Fatal(err)
	}
	var classify *roleUse
	for i := range js.Roles {
		if js.Roles[i].Role == "classify" {
			classify = &js.Roles[i]
		}
	}
	if len(js.Models) != 3 || classify == nil || classify.Model != "small-classifier" || classify.From != "" || classify.Offered == nil || !*classify.Offered {
		t.Fatalf("json = %+v", js)
	}
}

func TestAIClassifyThroughTheGatewayUsesTheClassifyModel(t *testing.T) {
	e := classifyEnv(t)
	gw := newGatewayStub(t, "big-explainer", "small-classifier")
	t.Setenv("ADE_TEST_GW_KEY", "sk-gw-test")
	e.app.Config.AI.Profile = "gw"
	e.app.Config.AI.Profiles["gw"] = config.AIProfile{
		Driver: "openai-compat", BaseURL: gw.srv.URL + "/v1", KeyEnv: "ADE_TEST_GW_KEY",
		Models: map[string]string{"explain": "big-explainer", "classify": "small-classifier"},
	}
	out := e.mustAI("classify", fake.GroupPath+"/tracker#11")
	if !strings.Contains(out, "suggested labels (ai): type::bug") {
		t.Fatalf("output:\n%s", out)
	}
	if got := gw.called(); len(got) != 1 || got[0] != "small-classifier" {
		t.Fatalf("gateway models called = %v", got)
	}
}

func lineWith(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestGatherAIPromptsListGatewayModelsAndAcceptNumbersOrIDs(t *testing.T) {
	gw := newGatewayStub(t, "alpha-large", "beta-mini", "gamma-flash")
	t.Setenv("ADE_TEST_GW_KEY", "sk-gw-test")
	answers := strings.Join([]string{
		"y",               // use a gateway
		gw.srv.URL,        // url, scheme and /v1 as the user has them
		"ADE_TEST_GW_KEY", // key source
		"9", "1",          // explain: out of range (asked again), then alpha-large
		"3",                  // classify: gamma-flash
		"",                   // draft: explain's
		"typed-review-model", // review: a typed id
	}, "\n") + "\n"
	var out bytes.Buffer
	pr := &prompter{in: bufio.NewReader(strings.NewReader(answers)), out: &out}
	g, err := gatherAI(context.Background(), pr, aiFlags{models: map[string]*string{}}, &out)
	if err != nil || g == nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if g.BaseURL != gw.srv.URL+"/v1" || g.KeyEnv != "ADE_TEST_GW_KEY" {
		t.Fatalf("%+v", g)
	}
	want := map[string]string{"explain": "alpha-large", "classify": "gamma-flash", "review": "typed-review-model"}
	if len(g.Models) != len(want) {
		t.Fatalf("models = %v", g.Models)
	}
	for r, m := range want {
		if g.Models[r] != m {
			t.Errorf("%s = %q, want %q", r, g.Models[r], m)
		}
	}
	for _, m := range []string{"alpha-large", "beta-mini", "gamma-flash"} {
		if !strings.Contains(out.String(), m) {
			t.Errorf("the gateway's %s was not listed:\n%s", m, out.String())
		}
	}
	if !strings.Contains(out.String(), "small, cheap model") {
		t.Errorf("classify is not pointed at a small model:\n%s", out.String())
	}

	// Saying no asks nothing more.
	pr = &prompter{in: bufio.NewReader(strings.NewReader("n\n")), out: &out}
	if g, err := gatherAI(context.Background(), pr, aiFlags{models: map[string]*string{}}, &out); g != nil || err != nil {
		t.Fatalf("declined: %+v %v", g, err)
	}
}
