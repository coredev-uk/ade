package config

import (
	"context"
	"strings"
	"testing"
)

const aiGateway = `
[ai]
mode = "ask"
profile = "gateway"
[ai.profiles.gateway]
driver = "openai-compat"
base_url = "http://127.0.0.1:4000/v1"
key_env = "ADE_TEST_LLM_KEY"
models = { explain = "echo", draft = "echo" }
billing = { source = "litellm" }
[ai.profiles.gateway.prices.echo]
input = 1.0
output = 2.0
[ai.profiles.sub]
driver = "omp"
billing = { source = "omp", weekly_soft_usd = 10 }
[budget]
daily_usd = 2.0
call_usd = 0.5
`

func TestAIConfigParsesAndValidates(t *testing.T) {
	cfg := loadConfig(t, aiGateway)
	if issues := cfg.Validate(); len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	p := cfg.AI.Profiles["gateway"]
	if p.Driver != "openai-compat" || p.Prices["echo"].Output != 2 || p.Billing.Source != "litellm" {
		t.Fatalf("gateway = %+v", p)
	}
	if cfg.AI.Profiles["sub"].Billing.WeeklySoftUSD != 10 || cfg.Budget.CallUSD != 0.5 || cfg.Budget.DailyUSD != 2 {
		t.Fatalf("sub/budget = %+v %+v", cfg.AI.Profiles["sub"], cfg.Budget)
	}
	// Unset budget keys keep the plan's defaults; the config's explicit 0 means no cap.
	if cfg.Budget.WeeklyUSD != 8 || cfg.Budget.ConfirmOverUSD != 0.25 || cfg.Budget.AgentRunUSD != 1.5 {
		t.Fatalf("defaults: %+v", cfg.Budget)
	}
	if off := loadConfig(t, "[budget]\ndaily_usd = 0\n"); off.Budget.DailyUSD != 0 {
		t.Fatalf("explicit 0 = no cap: %+v", off.Budget)
	}
}

func TestAIConfigIssues(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"bad mode", "[ai]\nmode = \"sometimes\"\n", "ai.mode:"},
		{"unknown active profile", "[ai]\nprofile = \"nope\"\n", "ai.profile:"},
		{"bad driver", "[ai.profiles.x]\ndriver = \"bard\"\n", "ai.profiles.x.driver:"},
		{"compat needs base_url", "[ai.profiles.x]\ndriver = \"openai-compat\"\nkey_env = \"K\"\n", "ai.profiles.x.base_url: required"},
		{"bad base_url", "[ai.profiles.x]\ndriver = \"anthropic\"\nbase_url = \"api.example.com\"\nkey_env = \"K\"\n", "ai.profiles.x.base_url:"},
		{"no key warns", "[ai.profiles.x]\ndriver = \"anthropic\"\n", "warn: ai.profiles.x:"},
		{"bad role", "[ai.profiles.x]\ndriver = \"omp\"\nmodels = { chat = \"m\" }\n", "ai.profiles.x.models.chat:"},
		{"bad billing source", "[ai.profiles.x]\ndriver = \"omp\"\nbilling = { source = \"stripe\" }\n", "ai.profiles.x.billing.source:"},
		{"negative price", "[ai.profiles.x]\ndriver = \"omp\"\n[ai.profiles.x.prices.m]\ninput = -1\n", "ai.profiles.x.prices.m:"},
		{"negative cap", "[budget]\nweekly_usd = -1\n", "budget.weekly_usd:"},
	}
	for _, tc := range tests {
		var got []string
		for _, i := range loadConfig(t, tc.src).Validate() {
			got = append(got, i.String())
		}
		if !strings.Contains(strings.Join(got, "\n"), tc.want) {
			t.Errorf("%s: issues %q lack %q", tc.name, got, tc.want)
		}
	}
}

func TestProfileModelFallsBackAndSecretsResolve(t *testing.T) {
	p := AIProfile{Models: map[string]string{"explain": "small", "draft": "mid"}}
	for role, want := range map[string]string{"explain": "small", "draft": "mid", "review": "mid", "plan": "small"} {
		if got, err := p.Model(role); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", role, got, err, want)
		}
	}
	if _, err := (AIProfile{}).Model("explain"); err == nil {
		t.Error("a profile without models must say which role lacks one")
	}

	t.Setenv("ADE_TEST_LLM_KEY", " sk-from-env \n")
	p.KeyEnv = "ADE_TEST_LLM_KEY"
	if k, err := p.Key(context.Background()); err != nil || k != "sk-from-env" {
		t.Fatalf("key = %q %v", k, err)
	}
	p = AIProfile{KeyCmd: "printf sk-from-cmd"}
	if k, err := p.Key(context.Background()); err != nil || k != "sk-from-cmd" {
		t.Fatalf("key = %q %v", k, err)
	}
	if _, err := (AIProfile{KeyEnv: "ADE_TEST_UNSET_KEY"}).Key(context.Background()); err == nil {
		t.Fatal("an empty env var without a command must fail")
	}
	if _, err := (AIProfile{KeyCmd: "exit 3"}).Key(context.Background()); err == nil {
		t.Fatal("a failing command must fail")
	}

	cfg := loadConfig(t, "")
	if _, _, err := cfg.ActiveProfile(); err == nil {
		t.Fatal("no profile selected")
	}
	cfg = loadConfig(t, aiGateway)
	if name, _, err := cfg.ActiveProfile(); err != nil || name != "gateway" {
		t.Fatalf("%s %v", name, err)
	}
	cfg.AI.Mode = "off"
	if _, _, err := cfg.ActiveProfile(); err == nil {
		t.Fatal("ai.mode off")
	}
}

func TestGatewayProfileValidation(t *testing.T) {
	ok := loadConfig(t, `
[ai]
profile = "gw"
[ai.profiles.gw]
driver = "openai-compat"
base_url = "https://litellm.example.internal/v1"
key_env = "ADE_TEST_LLM_KEY"
timeout = "90s"
extra_headers = { "x-litellm-tags" = "ade" }
models = { explain = "big", classify = "small-classifier" }
billing = { source = "litellm" }
`)
	if issues := ok.Validate(); len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	p := ok.AI.Profiles["gw"]
	if m, _ := p.Model("classify"); m != "small-classifier" || p.Timeout.Seconds() != 90 || p.ExtraHeaders["x-litellm-tags"] != "ade" {
		t.Fatalf("%+v", p)
	}
	if m, from, _ := p.ModelFrom("draft"); m != "big" || from != "explain" {
		t.Fatalf("draft = %q from %q", m, from)
	}
	delete(p.Models, "classify")
	if m, from, _ := p.ModelFrom("classify"); m != "big" || from != "explain" {
		t.Fatalf("classify must fall back to explain, got %q from %q", m, from)
	}

	for name, tc := range map[string]struct{ toml, path string }{
		"litellm billing on the wrong driver": {`driver = "anthropic"
key_env = "K"
billing = { source = "litellm" }`, "ai.profiles.gw.billing.source"},
		"litellm without a key": {`driver = "openai-compat"
base_url = "https://x.example/v1"
billing = { source = "litellm" }`, "ai.profiles.gw"},
		"authorization header": {`driver = "openai-compat"
base_url = "https://x.example/v1"
key_env = "K"
extra_headers = { Authorization = "Bearer x" }`, "ai.profiles.gw.extra_headers.Authorization"},
		"bad header name": {`driver = "openai-compat"
base_url = "https://x.example/v1"
key_env = "K"
extra_headers = { "bad name" = "x" }`, "ai.profiles.gw.extra_headers.bad name"},
		"empty model": {`driver = "openai-compat"
base_url = "https://x.example/v1"
key_env = "K"
models = { classify = " " }`, "ai.profiles.gw.models.classify"},
		"unknown role": {`driver = "openai-compat"
base_url = "https://x.example/v1"
key_env = "K"
models = { triage = "m" }`, "ai.profiles.gw.models.triage"},
	} {
		cfg := loadConfig(t, "[ai.profiles.gw]\n"+tc.toml+"\n")
		found := false
		for _, is := range cfg.Validate() {
			if is.Path == tc.path && is.Severity == SeverityError {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no error at %s in %v", name, tc.path, cfg.Validate())
		}
	}
}
