package doctor

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
)

const (
	readSecret  = "r3ad-only-token-value"
	writeSecret = secret
)

// gitlabTokens serves a GitLab where each token has its own PAT record and
// account. scopes and users are keyed by token.
func gitlabTokens(t *testing.T, scopes, users map[string]string) config.Forge {
	t.Helper()
	return forgeServer(t, func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("PRIVATE-TOKEN")
		switch r.URL.Path {
		case "/api/v4/personal_access_tokens/self":
			if s, ok := scopes[tok]; ok {
				w.Write([]byte(`{"scopes":` + s + `,"active":true}`))
				return
			}
		case "/api/v4/user":
			if u, ok := users[tok]; ok {
				w.Write([]byte(`{"username":"` + u + `"}`))
				return
			}
		case "/api/v4/version":
			w.Write([]byte(`{"version":"17.5.0","enterprise":false}`))
			return
		}
		http.NotFound(w, r)
	})
}

func runForgeWithReadToken(t *testing.T, kind string, f config.Forge) []Check {
	t.Helper()
	t.Setenv("DOCTOR_TEST_TOKEN", writeSecret)
	t.Setenv("DOCTOR_TEST_READ_TOKEN", readSecret)
	f.Kind, f.TokenEnv, f.ReadTokenEnv = kind, "DOCTOR_TEST_TOKEN", "DOCTOR_TEST_READ_TOKEN"
	checks := forgeOnly(&config.Config{Forges: map[string]config.Forge{"f": f}}, 5*time.Second)
	for _, c := range checks {
		if strings.Contains(c.Detail+c.Fix, readSecret) || strings.Contains(c.Detail+c.Fix, writeSecret) {
			t.Errorf("check %q leaks a token", c.Name)
		}
	}
	return checks
}

func TestGitLabReadTokenScopes(t *testing.T) {
	tests := []struct {
		name     string
		scopes   string
		protocol string
		status   Status
		detail   string
	}{
		{"read only", `["read_api","read_user"]`, "", OK, "(read-only)"},
		{"read only over https with read_repository", `["read_api","read_user","read_repository"]`, "https", OK, "(read-only)"},
		{"read only over https without read_repository", `["read_api","read_user"]`, "https", Warn, "read_repository missing"},
		{"api can write", `["api"]`, "", Warn, "can write"},
		{"missing read_user", `["read_api"]`, "", Error, "missing read_user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := gitlabTokens(t,
				map[string]string{writeSecret: `["api"]`, readSecret: tt.scopes},
				map[string]string{writeSecret: "me", readSecret: "me"})
			f.GitProtocol = tt.protocol
			checks := runForgeWithReadToken(t, "gitlab", f)
			if c := byName(checks, "f token"); c.Status != OK {
				t.Errorf("write token: %+v", c)
			}
			if c := byName(checks, "f read token"); c.Status != OK || !strings.Contains(c.Detail, "$DOCTOR_TEST_READ_TOKEN") {
				t.Errorf("read token: %+v", c)
			}
			if c := byName(checks, "f auth"); c.Status != OK || c.Detail != "scopes: api" {
				t.Errorf("write auth: %+v", c)
			}
			c := byName(checks, "f read auth")
			if c.Status != tt.status || !strings.Contains(c.Detail, tt.detail) {
				t.Errorf("got %s %q, want %s containing %q", c.Status, c.Detail, tt.status, tt.detail)
			}
			if c.Status != OK && c.Fix == "" {
				t.Error("non-ok check has no fix hint")
			}
		})
	}
}

func TestReadTokenAccountMustMatchWriteToken(t *testing.T) {
	f := gitlabTokens(t,
		map[string]string{writeSecret: `["api"]`, readSecret: `["read_api","read_user"]`},
		map[string]string{writeSecret: "alice", readSecret: "ci-bot"})
	checks := runForgeWithReadToken(t, "gitlab", f)
	c := byName(checks, "f read auth")
	if c.Status != Error || c.Detail != "read token is ci-bot, write token is alice" || c.Fix == "" {
		t.Errorf("got %+v", c)
	}
}

func TestGitHubReadToken(t *testing.T) {
	tests := []struct {
		name   string
		header map[string]string
		status Status
		detail string
	}{
		{"classic with repo", map[string]string{"X-OAuth-Scopes": "repo"}, Warn, "scopes: repo"},
		{"classic without repo", map[string]string{"X-OAuth-Scopes": "read:org"}, OK, "scopes: read:org"},
		{"fine grained", nil, OK, "permissions not reported; give it read-only access"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := forgeServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v3/user" {
					http.NotFound(w, r)
					return
				}
				if r.Header.Get("Authorization") == "Bearer "+readSecret {
					for k, v := range tt.header {
						w.Header().Set(k, v)
					}
				} else {
					w.Header().Set("X-OAuth-Scopes", "repo")
				}
				w.Write([]byte(`{"login":"octo"}`))
			})
			checks := runForgeWithReadToken(t, "github", f)
			if c := byName(checks, "f auth"); c.Status != OK {
				t.Errorf("write auth: %+v", c)
			}
			c := byName(checks, "f read auth")
			if c.Status != tt.status || !strings.Contains(c.Detail, tt.detail) || !strings.Contains(c.Detail, "octo") {
				t.Errorf("got %s %q, want %s containing %q", c.Status, c.Detail, tt.status, tt.detail)
			}
		})
	}
}

func TestGitHubReadTokenOfAnotherAccount(t *testing.T) {
	f := forgeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+readSecret {
			w.Write([]byte(`{"login":"bot"}`))
			return
		}
		w.Header().Set("X-OAuth-Scopes", "repo")
		w.Write([]byte(`{"login":"octo"}`))
	})
	c := byName(runForgeWithReadToken(t, "github", f), "f read auth")
	if c.Status != Error || c.Detail != "read token is bot, write token is octo" {
		t.Errorf("got %+v", c)
	}
}

func TestOnlyReadTokenWarnsAndGoesOn(t *testing.T) {
	f := gitlabTokens(t,
		map[string]string{readSecret: `["read_api","read_user"]`},
		map[string]string{readSecret: "me"})
	t.Setenv("DOCTOR_TEST_READ_TOKEN", readSecret)
	f.Kind, f.ReadTokenEnv = "gitlab", "DOCTOR_TEST_READ_TOKEN"
	checks := forgeOnly(&config.Config{Forges: map[string]config.Forge{"f": f}}, 5*time.Second)
	if c := byName(checks, "f token"); c.Status != Warn || !strings.Contains(c.Detail, "read-only") || c.Fix == "" {
		t.Errorf("token: %+v", c)
	}
	if c := byName(checks, "f read auth"); c.Status != OK {
		t.Errorf("read auth: %+v", c)
	}
	if c := byName(checks, "f auth"); c.Status != "absent" {
		t.Errorf("there is no write token to check: %+v", c)
	}
	if c := byName(checks, "f version"); c.Status != OK {
		t.Errorf("version should use the read token: %+v", c)
	}
	if c := byName(checks, "f agents"); c.Status != "absent" {
		t.Errorf("no write token for agents to reach: %+v", c)
	}
}

func TestUnresolvableWriteTokenStaysAnErrorWithAReadToken(t *testing.T) {
	t.Setenv("DOCTOR_TEST_READ_TOKEN", readSecret)
	cfg := &config.Config{Forges: map[string]config.Forge{"f": {
		Kind: "gitlab", TokenEnv: "DOCTOR_TEST_UNSET", ReadTokenEnv: "DOCTOR_TEST_READ_TOKEN",
	}}}
	if c := byName(forgeOnly(cfg, time.Second), "f token"); c.Status != Error || c.Fix == "" {
		t.Errorf("got %+v", c)
	}
}

func TestUnresolvableReadTokenIsAnError(t *testing.T) {
	t.Setenv("DOCTOR_TEST_TOKEN", writeSecret)
	cfg := &config.Config{Forges: map[string]config.Forge{"f": {
		Kind: "gitlab", TokenEnv: "DOCTOR_TEST_TOKEN", ReadTokenEnv: "DOCTOR_TEST_UNSET",
	}}}
	checks := forgeOnly(cfg, time.Second)
	if c := byName(checks, "f read token"); c.Status != Error || c.Fix == "" {
		t.Errorf("got %+v", c)
	}
	if c := byName(checks, "f read auth"); c.Status != "absent" {
		t.Errorf("read auth must not run without a read token: %+v", c)
	}
}

func TestReadTokenFromCommand(t *testing.T) {
	f := gitlabTokens(t,
		map[string]string{writeSecret: `["api"]`, readSecret: `["read_api","read_user"]`},
		map[string]string{writeSecret: "me", readSecret: "me"})
	t.Setenv("DOCTOR_TEST_TOKEN", writeSecret)
	f.Kind, f.TokenEnv, f.ReadTokenCmd = "gitlab", "DOCTOR_TEST_TOKEN", "echo "+readSecret
	checks := forgeOnly(&config.Config{Forges: map[string]config.Forge{"f": f}}, 5*time.Second)
	if c := byName(checks, "f read token"); c.Status != OK || c.Detail != "resolved via read_token_cmd" {
		t.Errorf("read token: %+v", c)
	}
	if c := byName(checks, "f read auth"); c.Status != OK {
		t.Errorf("read auth: %+v", c)
	}
}

func TestAgentsCheck(t *testing.T) {
	tests := []struct {
		name   string
		forge  config.Forge
		status Status
		detail string
	}{
		{"write token from the environment", config.Forge{TokenEnv: "W", ReadTokenEnv: "R"}, OK, "do not inherit $W"},
		{"write token from a command", config.Forge{TokenCmd: "pass w", ReadTokenEnv: "R"}, OK, "can run token_cmd themselves"},
		{"one variable for both", config.Forge{TokenEnv: "T", ReadTokenEnv: "T"}, Warn, "same variable $T"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ok := agentsCheck("f", tt.forge)
			if !ok || c.Status != tt.status || !strings.Contains(c.Detail, tt.detail) {
				t.Errorf("got %v %+v", ok, c)
			}
			if c.Status != OK && c.Fix == "" {
				t.Error("non-ok check has no fix hint")
			}
		})
	}
}

func TestNoReadTokenKeepsTheChecksUnchanged(t *testing.T) {
	f := gitlabTokens(t, map[string]string{writeSecret: `["api"]`}, map[string]string{writeSecret: "me"})
	checks := runForge(t, "gitlab", f)
	var names []string
	for _, c := range checks {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, ","); got != "f token,f auth,f version" {
		t.Errorf("checks = %s", got)
	}
}
