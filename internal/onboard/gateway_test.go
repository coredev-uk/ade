package onboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
)

func TestRenderWritesAValidGatewayProfile(t *testing.T) {
	a := Answers{
		Handle: "jdoe", ForgeName: "work", Kind: "gitlab", TokenEnv: "GITLAB_TOKEN", GitProtocol: "ssh", Root: "~/ade",
		AI: &AIGateway{
			BaseURL: "https://litellm.example.internal/v1", KeyEnv: "LITELLM_KEY",
			Models: map[string]string{"explain": "big-explainer", "classify": "small-classifier"},
		},
	}
	data, err := Render(a)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if len(cfg.Undecoded) > 0 {
		t.Errorf("unknown keys written: %v\n%s", cfg.Undecoded, data)
	}
	for _, is := range cfg.Validate() {
		if is.Severity == config.SeverityError {
			t.Errorf("rendered config invalid: %s: %s\n%s", is.Path, is.Msg, data)
		}
	}
	name, p, err := cfg.ActiveProfile()
	if err != nil || name != "gateway" || p.Driver != "openai-compat" || p.Billing.Source != "litellm" || p.KeyEnv != "LITELLM_KEY" {
		t.Fatalf("active = %q %+v %v", name, p, err)
	}
	if m, _ := p.Model("classify"); m != "small-classifier" {
		t.Fatalf("classify = %q", m)
	}
	if m, _ := p.Model("review"); m != "big-explainer" {
		t.Fatalf("review must fall back to explain, got %q", m)
	}
	if strings.Contains(string(data), "sk-") {
		t.Fatalf("a key was written:\n%s", data)
	}

	a.AI = nil
	if data, _ := Render(a); strings.Contains(string(data), "[ai") {
		t.Fatalf("no gateway asked for, but an [ai] block was written:\n%s", data)
	}
}

func TestNormalizeGatewayURL(t *testing.T) {
	for in, want := range map[string]string{
		"litellm.example.internal":            "https://litellm.example.internal/v1",
		"https://litellm.example.internal/":   "https://litellm.example.internal/v1",
		"http://127.0.0.1:4000":               "http://127.0.0.1:4000/v1",
		"https://gw.example.internal/llm/v1/": "https://gw.example.internal/llm/v1",
	} {
		if got, err := NormalizeGatewayURL(in); err != nil || got != want {
			t.Errorf("%q -> %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x.example", "https://user:pw@x.example/v1", "https://"} {
		if _, err := NormalizeGatewayURL(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestAIGatewayValidate(t *testing.T) {
	ok := AIGateway{BaseURL: "https://x.example/v1", KeyEnv: "K", Models: map[string]string{"explain": "m"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*AIGateway){
		"no key":        func(g *AIGateway) { g.KeyEnv = "" },
		"no explain":    func(g *AIGateway) { g.Models = map[string]string{"classify": "m"} },
		"unknown role":  func(g *AIGateway) { g.Models = map[string]string{"explain": "m", "triage": "x"} },
		"bad url":       func(g *AIGateway) { g.BaseURL = "nope" },
		"empty a model": func(g *AIGateway) { g.Models = map[string]string{"explain": "m", "draft": " "} },
	} {
		g := ok
		g.Models = map[string]string{"explain": "m"}
		mutate(&g)
		if g.Validate() == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
