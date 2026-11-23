package doctor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
)

func aiRun(t *testing.T, a config.AI, lookPath func(string) (string, error)) []Check {
	t.Helper()
	cfg := &config.Config{AI: a}
	o := Options{Config: cfg, Timeout: 5 * time.Second, LookPath: lookPath}
	o.defaults()
	checks := aiChecks(context.Background(), o)
	for _, c := range checks {
		if c.Group != aiGroup {
			t.Errorf("check %q is in group %q", c.Name, c.Group)
		}
		if c.Status == Error {
			t.Errorf("AI is optional: %q must never be an error (%+v)", c.Name, c)
		}
	}
	return checks
}

func TestAIChecksTellOffFromUnsetFromMisconfigured(t *testing.T) {
	prof := map[string]config.AIProfile{"sub": {Driver: "omp", Models: map[string]string{"explain": "m"}}}
	for _, tc := range []struct {
		name   string
		ai     config.AI
		status Status
		detail string
		fix    string
	}{
		{"off", config.AI{Mode: "off", Profile: "sub", Profiles: prof}, OK, "ADE runs without AI", ""},
		{"nothing configured", config.AI{Mode: "ask"}, OK, "no profile configured", ""},
		{"none selected", config.AI{Mode: "ask", Profiles: prof}, Warn, "no profile is selected", "set ai.profile to one of: sub"},
		{"selected one missing", config.AI{Mode: "auto", Profile: "gone", Profiles: prof}, Warn, `"gone" is not defined`, "set ai.profile to one of: sub"},
	} {
		got := aiRun(t, tc.ai, func(string) (string, error) { return "/bin/omp", nil })
		if len(got) != 1 {
			t.Errorf("%s: only the mode is checked, got %+v", tc.name, got)
			continue
		}
		c := byName(got, "mode")
		if c.Status != tc.status || !strings.Contains(c.Detail, tc.detail) || c.Fix != tc.fix {
			t.Errorf("%s: %+v", tc.name, c)
		}
	}
}

func TestAIChecksOMPDriverBinary(t *testing.T) {
	a := config.AI{Mode: "ask", Profile: "sub", Profiles: map[string]config.AIProfile{"sub": {Driver: "omp", Command: "my-omp", Models: map[string]string{"explain": "claude-x"}}}}
	var asked string
	got := aiRun(t, a, func(bin string) (string, error) { asked = bin; return "/opt/my-omp", nil })
	if asked != "my-omp" || byName(got, "driver").Status != OK || byName(got, "profile").Detail != "sub: driver omp, explain model claude-x" {
		t.Errorf("found: %+v", got)
	}
	got = aiRun(t, a, func(string) (string, error) { return "", errors.New("not found") })
	c := byName(got, "driver")
	if c.Status != Warn || !strings.Contains(c.Detail, "my-omp is not installed") || !strings.Contains(c.Fix, "ai.mode") || byName(got, "mode").Status != OK {
		t.Errorf("missing binary: %+v", c)
	}
	// No explain model: named, with the key to set.
	a.Profiles["sub"] = config.AIProfile{Driver: "omp"}
	if c := byName(aiRun(t, a, func(string) (string, error) { return "/bin/omp", nil }), "profile"); c.Status != Warn || !strings.Contains(c.Fix, "models.explain") {
		t.Errorf("no model: %+v", c)
	}
}

func TestAIChecksGatewayKeyAndReachability(t *testing.T) {
	const key = "sk-doctor-secret-key"
	var gotAuth, gotMethod, gotPath string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotMethod, gotPath = r.Header.Get("Authorization"), r.Method, r.URL.Path
		if status != http.StatusOK {
			http.Error(w, `{"error":{"message":"bad key `+key+`"}}`, status)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"echo"},{"id":"mini"}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DOCTOR_AI_KEY", key)
	a := config.AI{Mode: "ask", Profile: "gw", Profiles: map[string]config.AIProfile{"gw": {
		Driver: "openai-compat", BaseURL: srv.URL + "/v1", KeyEnv: "DOCTOR_AI_KEY", Models: map[string]string{"explain": "echo"},
	}}}

	got := aiRun(t, a, nil)
	if c := byName(got, "key"); c.Status != OK || c.Detail != "resolved via $DOCTOR_AI_KEY" {
		t.Errorf("key: %+v", c)
	}
	if c := byName(got, "gateway"); c.Status != OK || !strings.Contains(c.Detail, "2 models") {
		t.Errorf("gateway: %+v", c)
	}
	if gotMethod != http.MethodGet || gotPath != "/v1/models" || gotAuth != "Bearer "+key {
		t.Errorf("the probe is a read-only GET of /models: %s %s %q", gotMethod, gotPath, gotAuth)
	}

	// A refused key: the cause and fix, never the gateway's body or the key.
	status = http.StatusUnauthorized
	got = aiRun(t, a, nil)
	c := byName(got, "gateway")
	if c.Status != Warn || !strings.Contains(c.Detail, "refused the key") || !strings.Contains(c.Fix, "key_env") {
		t.Errorf("401: %+v", c)
	}
	for _, c := range got {
		if strings.Contains(c.Detail+c.Fix, key) || strings.Contains(c.Detail, "bad key") {
			t.Errorf("check %q leaks the key or the gateway's body: %+v", c.Name, c)
		}
	}

	// Unreachable gateway.
	srv.Close()
	if c := byName(aiRun(t, a, nil), "gateway"); c.Status != Warn || c.Detail == "" || c.Fix == "" {
		t.Errorf("closed gateway: %+v", c)
	}
}

func TestAIChecksMissingKeyIsAWarningAndSkipsTheGateway(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	t.Cleanup(srv.Close)
	t.Setenv("DOCTOR_AI_EMPTY", "")
	for name, p := range map[string]config.AIProfile{
		"nothing configured": {Driver: "openai-compat", BaseURL: srv.URL, Models: map[string]string{"explain": "m"}},
		"empty env":          {Driver: "anthropic", BaseURL: srv.URL, KeyEnv: "DOCTOR_AI_EMPTY", Models: map[string]string{"explain": "m"}},
		"failing command":    {Driver: "openai-compat", BaseURL: srv.URL, KeyCmd: "echo oops >&2; exit 3", Models: map[string]string{"explain": "m"}},
	} {
		got := aiRun(t, config.AI{Mode: "ask", Profile: "p", Profiles: map[string]config.AIProfile{"p": p}}, nil)
		c := byName(got, "key")
		if c.Status != Warn || !strings.Contains(c.Fix, "key_env or key_cmd") {
			t.Errorf("%s: %+v", name, c)
		}
		if strings.Contains(c.Detail, "oops") {
			t.Errorf("%s: the key command's output is shown: %q", name, c.Detail)
		}
		if g := byName(got, "gateway"); g.Status != "absent" {
			t.Errorf("%s: a gateway with no key is not asked: %+v", name, g)
		}
	}
	if hit {
		t.Error("the gateway was called without a key")
	}
}

func TestRunIncludesTheAIGroupLast(t *testing.T) {
	cfg := &config.Config{AI: config.AI{Mode: "off"}}
	checks := Run(context.Background(), Options{Config: cfg, Timeout: 5 * time.Second, LookPath: func(string) (string, error) { return "", errors.New("none") }})
	if last := checks[len(checks)-1]; last.Group != aiGroup || last.Name != "mode" || last.Status != OK {
		t.Errorf("last check: %+v", last)
	}
}
