package doctor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
)

func byName(checks []Check, name string) Check {
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	return Check{Name: name, Status: "absent"}
}

func forgeServer(t *testing.T, h http.HandlerFunc) config.Forge {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return config.Forge{URL: srv.URL}
}

const secret = "s3cr3t-token-value"

func runForge(t *testing.T, kind string, f config.Forge) []Check {
	t.Helper()
	t.Setenv("DOCTOR_TEST_TOKEN", secret)
	f.Kind, f.TokenEnv = kind, "DOCTOR_TEST_TOKEN"
	cfg := &config.Config{Forges: map[string]config.Forge{"f": f}}
	checks := forgeOnly(cfg, 5*time.Second)
	for _, c := range checks {
		if strings.Contains(c.Detail+c.Fix, secret) {
			t.Errorf("check %q leaks the token", c.Name)
		}
	}
	return checks
}

// forgeOnly runs just the forge checks; the full Run would also spawn every
// tool's --version, which is slow and irrelevant here.
func forgeOnly(cfg *config.Config, timeout time.Duration) []Check {
	o := Options{Config: cfg, Timeout: timeout}
	o.defaults()
	var all []Check
	for _, fn := range forgeChecks(o) {
		all = append(all, fn(context.Background())...)
	}
	return all
}

func TestGitLabScopes(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status Status
		detail string
	}{
		{"api", `{"scopes":["api"],"active":true}`, OK, "scopes: api"},
		{"read only", `{"scopes":["read_api","read_user"],"active":true}`, Warn, "read-only"},
		{"missing read_user", `{"scopes":["read_api"],"active":true}`, Error, "missing read_user"},
		{"revoked", `{"scopes":["api"],"active":false,"revoked":true}`, Error, ""},
		{"expired", `{"scopes":["api"],"active":true,"expires_at":"2001-01-01"}`, Error, ""},
		{"expires soon", `{"scopes":["api"],"active":true,"expires_at":"` + time.Now().Add(24*time.Hour).Format("2006-01-02") + `"}`, Warn, ""},
		{"far expiry", `{"scopes":["api"],"active":true,"expires_at":"` + time.Now().AddDate(1, 0, 0).Format("2006-01-02") + `"}`, OK, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := forgeServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v4/personal_access_tokens/self" || r.Header.Get("PRIVATE-TOKEN") != secret {
					http.NotFound(w, r)
					return
				}
				w.Write([]byte(tt.body))
			})
			auth := byName(runForge(t, "gitlab", f), "f auth")
			if auth.Status != tt.status || !strings.Contains(auth.Detail, tt.detail) {
				t.Errorf("got %s %q, want %s containing %q", auth.Status, auth.Detail, tt.status, tt.detail)
			}
			if auth.Status != OK && auth.Fix == "" {
				t.Error("non-ok check has no fix hint")
			}
		})
	}
}

func TestGitLabFallsBackToUserEndpoint(t *testing.T) {
	f := forgeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/user" {
			w.Write([]byte(`{"username":"bot"}`))
			return
		}
		http.NotFound(w, r)
	})
	auth := byName(runForge(t, "gitlab", f), "f auth")
	if auth.Status != OK || !strings.Contains(auth.Detail, "authenticated as bot") {
		t.Errorf("got %s %q", auth.Status, auth.Detail)
	}
}

func TestGitLabRejectedToken(t *testing.T) {
	f := forgeServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	auth := byName(runForge(t, "gitlab", f), "f auth")
	if auth.Status != Error || !strings.Contains(auth.Detail, "401") {
		t.Errorf("got %s %q", auth.Status, auth.Detail)
	}
}

func TestGitHubScopes(t *testing.T) {
	tests := []struct {
		name   string
		header map[string]string
		status Status
		detail string
	}{
		{"classic with repo", map[string]string{"X-OAuth-Scopes": "repo, read:org"}, OK, "scopes: repo, read:org"},
		{"classic without repo", map[string]string{"X-OAuth-Scopes": "read:org"}, Warn, "scopes: read:org"},
		{"classic empty", map[string]string{"X-OAuth-Scopes": ""}, Warn, "scopes: none"},
		{"fine grained", nil, OK, "not reported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := forgeServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v3/user" || r.Header.Get("Authorization") != "Bearer "+secret {
					http.Error(w, "no", http.StatusUnauthorized)
					return
				}
				for k, v := range tt.header {
					w.Header().Set(k, v)
				}
				w.Write([]byte(`{"login":"octo"}`))
			})
			auth := byName(runForge(t, "github", f), "f auth")
			if auth.Status != tt.status || !strings.Contains(auth.Detail, tt.detail) || !strings.Contains(auth.Detail, "octo") {
				t.Errorf("got %s %q, want %s containing %q", auth.Status, auth.Detail, tt.status, tt.detail)
			}
		})
	}
}

func TestForgeTokenUnresolvable(t *testing.T) {
	cfg := &config.Config{Forges: map[string]config.Forge{"f": {Kind: "github", TokenEnv: "DOCTOR_TEST_UNSET"}}}
	checks := forgeOnly(cfg, time.Second)
	tok := byName(checks, "f token")
	if tok.Status != Error || tok.Fix == "" {
		t.Errorf("got %+v", tok)
	}
	if byName(checks, "f auth").Status != "absent" {
		t.Error("auth must not run without a token")
	}
}

func TestForgeUnreachableTimesOut(t *testing.T) {
	block := make(chan struct{})
	f := forgeServer(t, func(http.ResponseWriter, *http.Request) { <-block })
	t.Cleanup(func() { close(block) })
	t.Setenv("DOCTOR_TEST_TOKEN", secret)
	f.Kind, f.TokenEnv = "gitlab", "DOCTOR_TEST_TOKEN"
	cfg := &config.Config{Forges: map[string]config.Forge{"f": f}}
	start := time.Now()
	checks := forgeOnly(cfg, 200*time.Millisecond)
	if auth := byName(checks, "f auth"); auth.Status != Error {
		t.Errorf("got %+v", auth)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("network check ignored its timeout")
	}
}

func TestConfigChecks(t *testing.T) {
	dflt := config.Defaults()
	missing := config.Defaults()
	missing.Missing = true
	bad := config.Defaults()
	bad.Update.FeatureBranches = "merge"
	bad.Undecoded = []string{"user.handel"}

	tests := []struct {
		name   string
		opts   Options
		check  string
		status Status
	}{
		{"parsed", Options{ConfigFile: "c.toml", Config: &dflt}, "config file", OK},
		{"missing", Options{ConfigFile: "c.toml", Config: &missing}, "config file", Warn},
		{"parse error", Options{ConfigFile: "c.toml", ConfigErr: errors.New("line 3: bad")}, "config file", Error},
		{"invalid value", Options{ConfigFile: "c.toml", Config: &bad}, "config update.feature_branches", Error},
		{"unknown key", Options{ConfigFile: "c.toml", Config: &bad}, "config user.handel", Warn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if c := byName(configChecks(tt.opts), tt.check); c.Status != tt.status {
				t.Errorf("got %+v, want %s", c, tt.status)
			}
		})
	}
	if c := byName(configChecks(Options{ConfigFile: "/x/c.toml", Config: &missing}), "config file"); !strings.Contains(c.Detail, "/x/c.toml") {
		t.Errorf("missing-config warning must name the path: %+v", c)
	}
}

func fakeTool(t *testing.T, dir, name, output string) {
	t.Helper()
	script := "#!/bin/sh\necho '" + output + "'\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestToolChecks(t *testing.T) {
	bin := t.TempDir()
	fakeTool(t, bin, "git", "git version 2.37.9")
	fakeTool(t, bin, "lazygit", "commit=abc123, build date=2024-01-01, version=0.40.2, os=linux")
	fakeTool(t, bin, "nvim", "no version here")
	t.Setenv("PATH", bin)

	checks := toolChecks(context.Background(), Options{Timeout: 5 * time.Second})
	tests := []struct {
		name   string
		status Status
		detail string
	}{
		{"git", Error, "2.37.9"},        // below 2.38
		{"zellij", Warn, "not found"},   // recommended, absent
		{"lazygit", OK, "0.40.2"},       // version parsed past the commit hash
		{"nvim", OK, "version unknown"}, // present, unparsable, no minimum
		{"gh", OK, "optional"},          // optional, absent
	}
	for _, tt := range tests {
		c := byName(checks, tt.name)
		if c.Status != tt.status || !strings.Contains(c.Detail, tt.detail) {
			t.Errorf("%s: got %s %q, want %s containing %q", tt.name, c.Status, c.Detail, tt.status, tt.detail)
		}
	}
	if byName(checks, "git").Fix == "" {
		t.Error("old git needs a fix hint")
	}

	fakeTool(t, bin, "git", "git version 2.43.0")
	if c := byName(toolChecks(context.Background(), Options{Timeout: 5 * time.Second}), "git"); c.Status != OK {
		t.Errorf("git 2.43 should pass: %+v", c)
	}
}

func TestToolMissingGitIsError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	c := byName(toolChecks(context.Background(), Options{Timeout: time.Second}), "git")
	if c.Status != Error {
		t.Errorf("got %+v", c)
	}
}

func TestCheckWritable(t *testing.T) {
	dir := t.TempDir()
	if c := checkWritable("x", dir); c.Status != OK {
		t.Errorf("existing dir: %+v", c)
	}
	c := checkWritable("x", filepath.Join(dir, "a", "b"))
	if c.Status != OK || !strings.Contains(c.Detail, "will be created") {
		t.Errorf("missing dir under writable parent: %+v", c)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); err == nil {
		t.Error("doctor must not create directories")
	}
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o600)
	if c := checkWritable("x", file); c.Status != Error {
		t.Errorf("file as dir: %+v", c)
	}
	if os.Geteuid() != 0 {
		ro := filepath.Join(dir, "ro")
		os.Mkdir(ro, 0o500)
		if c := checkWritable("x", ro); c.Status != Error || c.Fix == "" {
			t.Errorf("read-only dir: %+v", c)
		}
	}
}

func TestHostDetection(t *testing.T) {
	tests := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"ZELLIJ": "0"}, "zellij"},
		{map[string]string{"TERM_PROGRAM": "vscode"}, "vscode"},
		{nil, "plain"},
	}
	for _, tt := range tests {
		got := hostChecks(Options{Getenv: func(k string) string { return tt.env[k] }})
		if len(got) != 2 || got[0].Name != "host" || !strings.Contains(got[0].Detail, tt.want) {
			t.Errorf("env %v: got %+v, want %q", tt.env, got, tt.want)
		}
	}
}

func TestHostReportsTheResolvedEditor(t *testing.T) {
	env := map[string]string{"TERM_PROGRAM": "vscode", "EDITOR": "nano"}
	cfg := config.Defaults()
	cfg.Host.Editor = "$EDITOR"
	got := hostChecks(Options{Config: &cfg, Getenv: func(k string) string { return env[k] }})
	if len(got) != 2 || got[1].Name != "editor" || !strings.Contains(got[1].Detail, "$EDITOR (nano)") {
		t.Fatalf("%+v", got)
	}
}

func TestRecordsInsideGitWorkTreeWarns(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(repo, "data", "ade") // does not exist yet: the check must still see the repo above it
	c := checkRecordsOutsideGit(inside)
	if c.Status != Warn || c.Fix == "" || !strings.Contains(c.Detail, repo) {
		t.Fatalf("inside a work tree: %+v", c)
	}
	// A linked worktree or submodule has a .git *file*.
	linked := t.TempDir()
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := checkRecordsOutsideGit(filepath.Join(linked, "records")); c.Status != Warn {
		t.Errorf("linked worktree: %+v", c)
	}
	// Through a symlink that points into the repository.
	link := filepath.Join(t.TempDir(), "records")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	if c := checkRecordsOutsideGit(link); c.Status != Warn {
		t.Errorf("symlinked into a work tree: %+v", c)
	}
	if c := checkRecordsOutsideGit(filepath.Join(t.TempDir(), "ade")); c.Status != OK {
		t.Errorf("outside any repository: %+v", c)
	}
}

func TestRunOrderIsStable(t *testing.T) {
	cfg := config.Defaults()
	cfg.Forges = map[string]config.Forge{"b": {Kind: "github"}, "a": {Kind: "github"}}
	opts := Options{ConfigFile: "c", Config: &cfg, Getenv: func(string) string { return "" }}
	var groups []string
	for _, c := range Run(context.Background(), opts) {
		if len(groups) == 0 || groups[len(groups)-1] != c.Group {
			groups = append(groups, c.Group)
		}
	}
	if got := strings.Join(groups, ","); got != "config,tools,forge,host" {
		t.Errorf("group order %s", got)
	}
	var forge []string
	for _, c := range Run(context.Background(), opts) {
		if c.Group == "forge" {
			forge = append(forge, c.Name)
		}
	}
	if got := strings.Join(forge, ","); got != "a token,b token" {
		t.Errorf("forge order %s", got)
	}
}

func TestHasErrors(t *testing.T) {
	if HasErrors([]Check{{Status: Warn}, {Status: OK}}) {
		t.Error("warn is not an error")
	}
	if !HasErrors([]Check{{Status: OK}, {Status: Error}}) {
		t.Error("error missed")
	}
}
