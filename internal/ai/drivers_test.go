package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
)

// The budget-exceeded shapes docs/billing/litellm.md and vendors.md list.
// Classification is by body: the status varies by LiteLLM version.
func TestClassifyStopByBodyNotStatus(t *testing.T) {
	stops := map[string]string{
		"litellm key (current)": `{"error":{"message":"Budget has been exceeded! Key=ade-test Current cost: 0.012, Max budget: 0.01","type":"budget_exceeded","param":null,"code":"429"}}`,
		"litellm typed only":    `{"error":{"message":"nope","type":"budget_exceeded"}}`,
		"litellm old detail":    `{"detail":"Authentication Error, ExceededTokenBudget: Current spend for token: 7.2e-05; Max Budget for Token: 2e-07"}`,
		"litellm team":          `{"error":{"message":"Budget has been exceeded! Team=t1 Current cost: 5, Max budget: 5","type":"auth_error","code":"401"}}`,
		"litellm user":          `{"error":{"message":"ExceededBudget: User=u over budget. Spend=1, Budget=1","type":"auth_error"}}`,
		"litellm model":         `{"error":{"message":"LiteLLM Virtual Key: abc, key_alias: None, exceeded budget for model=gpt","type":"x"}}`,
		"anthropic platform":    `{"type":"error","error":{"type":"rate_limit_error","message":"cap","details":{"error_code":"enforced_spend_limit_reached"}}}`,
		"anthropic user limit":  `{"type":"error","error":{"type":"invalid_request_error","message":"You have reached your specified API usage limits. You will regain access on 2026-12-01"}}`,
		"openai quota":          `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`,
		"moonshot":              `{"error":{"message":"Your account is suspended","type":"exceeded_current_quota_error"}}`,
		"plain text body":       `Budget has been exceeded for this key`,
	}
	for name, body := range stops {
		if classifyStop("gateway", []byte(body)) == nil {
			t.Errorf("%s: not classified as a budget stop: %s", name, body)
		}
	}
	notStops := []string{
		`{"error":{"message":"rate limit reached, retry later","type":"rate_limit_error"}}`,
		`{"error":{"message":"model not found","type":"invalid_request_error"}}`,
		`{"error":{"message":"internal error","type":"server_error"}}`,
		``,
	}
	for _, body := range notStops {
		if classifyStop("gateway", []byte(body)) != nil {
			t.Errorf("wrongly classified as a budget stop: %q", body)
		}
	}
}

func TestOpenAICompatReadsGatewayCostHeadersAndUsage(t *testing.T) {
	var got struct {
		auth string
		body map[string]any
		path string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth, got.path = r.Header.Get("Authorization"), r.URL.Path
		json.NewDecoder(r.Body).Decode(&got.body)
		w.Header().Set("x-litellm-response-cost", "0.004")
		w.Header().Set("x-litellm-key-spend", "0.0081")
		w.Header().Set("x-litellm-key-max-budget", "0.01")
		io.WriteString(w, `{"model":"echo","choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`)
	}))
	defer srv.Close()
	d := newOpenAI(config.AIProfile{BaseURL: srv.URL + "/v1/"}, "sk-test-key", srv.Client())
	r, err := d.Complete(context.Background(), Call{Model: "echo", System: "sys", Prompt: "hi", MaxTokens: 50})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/chat/completions" || got.auth != "Bearer sk-test-key" {
		t.Fatalf("request: %+v", got)
	}
	msgs := got.body["messages"].([]any)
	if got.body["model"] != "echo" || got.body["max_tokens"] != float64(50) || len(msgs) != 2 {
		t.Fatalf("body: %+v", got.body)
	}
	if r.Text != "hello" || r.CostUSD != 0.004 || r.CostSource != CostGateway || r.Usage.In != 12 || r.Usage.Out != 3 {
		t.Fatalf("response: %+v", r)
	}
	if r.Gateway == nil || *r.Gateway.KeySpend != 0.0081 || *r.Gateway.KeyMaxBudget != 0.01 {
		t.Fatalf("gateway info: %+v", r.Gateway)
	}
	if r.DestHost == "" || r.Bytes == 0 {
		t.Fatalf("dest/bytes must be recorded: %+v", r)
	}
}

func TestOpenAICompatComputesCostWhenNoHeaderAndFlagsUnpriced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"x"}}],"usage":{"prompt_tokens":1000000,"completion_tokens":1000000,"prompt_tokens_details":{"cached_tokens":400000}}}`)
	}))
	defer srv.Close()
	p := config.AIProfile{BaseURL: srv.URL, Prices: map[string]config.Price{"m": {Input: 1, Output: 2, CacheRead: 0.1}}}
	r, err := newOpenAI(p, "k", srv.Client()).Complete(context.Background(), Call{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	// 600k fresh input at $1, 400k cached at $0.1, 1M output at $2 = 0.6 + 0.04 + 2.
	if r.CostSource != CostComputed || r.CostUSD < 2.639 || r.CostUSD > 2.641 {
		t.Fatalf("computed cost = %v (%s)", r.CostUSD, r.CostSource)
	}
	r, err = newOpenAI(config.AIProfile{BaseURL: srv.URL}, "k", srv.Client()).Complete(context.Background(), Call{Model: "unpriced"})
	if err != nil || r.CostSource != CostUnknown || r.CostUSD != 0 {
		t.Fatalf("unpriced: %+v %v", r, err)
	}
}

func TestOpenAICompatBudgetErrorsAreStopsWhateverTheStatus(t *testing.T) {
	for _, status := range []int{400, 401, 422, 429} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"message":"Budget has been exceeded! Key=k Current cost: 0.012, Max budget: 0.01","type":"budget_exceeded","code":"`+http.StatusText(status)+`"}}`)
		}))
		_, err := newOpenAI(config.AIProfile{BaseURL: srv.URL}, "secret-key", srv.Client()).Complete(context.Background(), Call{Model: "m"})
		srv.Close()
		stop, ok := AsStop(err)
		if !ok || stop.Source != "gateway" || !strings.Contains(stop.Message, "Max budget: 0.01") {
			t.Errorf("status %d: err = %v", status, err)
		}
	}
}

func TestOpenAICompatOtherErrorsAreNotStopsAndNeverLeakTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"upstream exploded","type":"server_error"}}`)
	}))
	_, err := newOpenAI(config.AIProfile{BaseURL: srv.URL}, "secret-key", srv.Client()).Complete(context.Background(), Call{Model: "m"})
	if _, ok := AsStop(err); ok || err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v", err)
	}
	srv.Close()
	// A refused connection must not echo the credential either.
	_, err = newOpenAI(config.AIProfile{BaseURL: srv.URL}, "secret-key", &http.Client{}).Complete(context.Background(), Call{Model: "m"})
	if err == nil || strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAICompatPricesFromLiteLLMModelGroupInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model_group/info" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"data":[{"model_group":"echo","input_cost_per_token":0.000001,"output_cost_per_token":0.000002},{"model_group":"free","input_cost_per_token":null,"output_cost_per_token":null}]}`)
	}))
	defer srv.Close()
	prices, err := newOpenAI(config.AIProfile{BaseURL: srv.URL + "/v1"}, "k", srv.Client()).Prices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, ok := prices["echo"]
	if !ok || p.Input < 0.99 || p.Input > 1.01 || p.Output < 1.99 || p.Output > 2.01 {
		t.Fatalf("prices = %+v: want USD per million tokens", prices)
	}
	if _, ok := prices["free"]; ok {
		t.Fatal("a model without a price must be omitted, not priced at 0")
	}
	// A plain OpenAI-compatible server has no such route: not an error.
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()
	if prices, err := newOpenAI(config.AIProfile{BaseURL: plain.URL}, "k", plain.Client()).Prices(context.Background()); err != nil || prices != nil {
		t.Fatalf("plain server: %v %v", prices, err)
	}
}

func TestAnthropicRequestShapeCostAndStop(t *testing.T) {
	var gotHeaders http.Header
	var gotBody map[string]any
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(status)
		if status != 200 {
			io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"x","details":{"error_code":"enforced_spend_limit_reached"}}}`)
			return
		}
		io.WriteString(w, `{"model":"claude-x","content":[{"type":"text","text":"he"},{"type":"text","text":"llo"}],"usage":{"input_tokens":1000000,"output_tokens":1000000,"cache_read_input_tokens":1000000,"cache_creation_input_tokens":0}}`)
	}))
	defer srv.Close()
	p := config.AIProfile{BaseURL: srv.URL, Prices: map[string]config.Price{"claude-x": {Input: 1, Output: 5, CacheRead: 0.1}}}
	d := newAnthropic(p, "sk-ant-key", srv.Client())
	r, err := d.Complete(context.Background(), Call{Model: "claude-x", System: "s", Prompt: "p", MaxTokens: 77})
	if err != nil {
		t.Fatal(err)
	}
	if gotHeaders.Get("x-api-key") != "sk-ant-key" || gotHeaders.Get("anthropic-version") == "" {
		t.Fatalf("headers: %v", gotHeaders)
	}
	if gotBody["system"] != "s" || gotBody["max_tokens"] != float64(77) {
		t.Fatalf("body: %+v", gotBody)
	}
	if r.Text != "hello" || r.CostSource != CostComputed || r.CostUSD < 6.099 || r.CostUSD > 6.101 {
		t.Fatalf("response: %+v (want 1 + 5 + 0.1)", r)
	}
	status = 429
	_, err = d.Complete(context.Background(), Call{Model: "claude-x"})
	if stop, ok := AsStop(err); !ok || stop.Source != "provider" {
		t.Fatalf("enforced_spend_limit_reached must be a stop: %v", err)
	}
}

func TestParseOMPOutputSumsTurnsAndReadsReportedCost(t *testing.T) {
	raw, err := os.ReadFile("testdata/omp-print.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	r, err := parseOMP(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "Hi" || r.Model != "claude-haiku-4-5" || r.Usage.In != 195 || r.Usage.Out != 4 || r.CostUSD != 0.000215 || r.DestHost != "omp:anthropic" {
		t.Fatalf("parsed = %+v", r)
	}
	if _, err := parseOMP([]byte(`{"type":"agent_start"}` + "\n")); err == nil {
		t.Fatal("output without a turn is an error")
	}
	failed := `{"type":"turn_end","message":{"role":"assistant","stopReason":"error","errorMessage":"401 invalid x-api-key","content":[],"usage":{}}}`
	if _, err := parseOMP([]byte(failed)); err == nil || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Fatalf("a failed turn must surface its message: %v", err)
	}
}

// fakeOMP writes a shell script that records its argv and stdin and prints
// the canned omp output, exiting with code.
func fakeOMP(t *testing.T, out string, code int) (cmd, argvFile, stdinFile string) {
	t.Helper()
	dir := t.TempDir()
	cmd = filepath.Join(dir, "omp")
	argvFile, stdinFile = filepath.Join(dir, "argv"), filepath.Join(dir, "stdin")
	canned := filepath.Join(dir, "canned")
	if err := os.WriteFile(canned, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvFile + "\ncat > " + stdinFile + "\ncat " + canned + "\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(cmd, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return cmd, argvFile, stdinFile
}

func TestOMPDriverInvocationIsMinimalAndPromptGoesOnStdin(t *testing.T) {
	canned, _ := os.ReadFile("testdata/omp-print.jsonl")
	cmd, argvFile, stdinFile := fakeOMP(t, string(canned), 0)
	d := newOMP(config.AIProfile{Command: cmd})
	big := strings.Repeat("diff line\n", 50000) // far beyond one argv entry
	r, err := d.Complete(context.Background(), Call{Model: "anthropic/claude-haiku-4-5", System: "be brief", Prompt: big, Thinking: "off", MaxCostUSD: 0.05})
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "Hi" || r.CostSource != CostReported || r.CostUSD != 0.000215 {
		t.Fatalf("response %+v", r)
	}
	argvRaw, _ := os.ReadFile(argvFile)
	argv := strings.Split(strings.TrimSpace(string(argvRaw)), "\n")
	for _, want := range []string{"-p", "json", "--no-session", "--no-tools", "--no-context-files", "--system-prompt", "be brief", "--model", "anthropic/claude-haiku-4-5", "--thinking", "off", "--max-cost", "0.050000"} {
		if !contains(argv, want) {
			t.Errorf("argv lacks %q: %v", want, argv)
		}
	}
	if stdin, _ := os.ReadFile(stdinFile); string(stdin) != big {
		t.Fatalf("prompt must arrive intact on stdin (%d bytes of %d)", len(stdin), len(big))
	}
	if len(argv) > 40 {
		t.Fatalf("argv unexpectedly large: %d entries", len(argv))
	}
}

func TestOMPMaxCostExitIsABudgetStop(t *testing.T) {
	cmd, _, _ := fakeOMP(t, "", 3)
	_, err := newOMP(config.AIProfile{Command: cmd}).Complete(context.Background(), Call{Model: "m", MaxCostUSD: 0.01})
	if stop, ok := AsStop(err); !ok || stop.Source != "omp" {
		t.Fatalf("exit 3 is omp's --max-cost stop: %v", err)
	}
	cmd, _, _ = fakeOMP(t, "", 1)
	_, err = newOMP(config.AIProfile{Command: cmd}).Complete(context.Background(), Call{Model: "m"})
	if _, ok := AsStop(err); ok || err == nil {
		t.Fatalf("any other failure is a plain error: %v", err)
	}
}

func TestOMPPricesFromCatalogue(t *testing.T) {
	cat := `{"models":[{"id":"claude-haiku-4-5","selector":"anthropic/claude-haiku-4-5","cost":{"input":1,"output":5,"cacheRead":0.1,"cacheWrite":1.25}},{"id":"nocost","selector":"x/nocost"}]}`
	cmd, _, _ := fakeOMP(t, cat, 0)
	prices, err := newOMP(config.AIProfile{Command: cmd}).Prices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"claude-haiku-4-5", "anthropic/claude-haiku-4-5"} {
		if p := prices[k]; p.Input != 1 || p.Output != 5 {
			t.Errorf("%s: %+v", k, p)
		}
	}
	if _, ok := prices["nocost"]; ok {
		t.Error("a model without cost data must be omitted")
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
