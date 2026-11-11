package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalPath(t *testing.T) {
	for in, want := range map[string]string{
		"/c/config.toml":   "/c/config.local.toml",
		"/p/acme.ade.toml": "/p/acme.ade.local.toml",
		"/c/ade.conf":      "/c/ade.conf.local.toml",
	} {
		if got := LocalPath(in); got != want {
			t.Errorf("LocalPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigLocalOverrideMergesTablesAndReplacesValues(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "config.toml")
	write(t, base, `
[user]
handle   = "jdoe"
timezone = "UTC"

[forges.work]
kind      = "gitlab"
url       = "https://gitlab.example.com"
token_cmd = "pass show ade/gitlab"

[forges.personal]
kind = "github"

[notify]
toast = ["ci.*", "mr.*"]

[[projects]]
source = "/shared/acme.ade.toml"

[[projects]]
source = "/shared/other.ade.toml"
`)
	write(t, filepath.Join(dir, "config.local.toml"), `
[user]
timezone = "Europe/Berlin"

[forges.work]
token_cmd = "echo local"

[notify]
toast = ["budget.*"]

[host]
editor = "code"

[[projects]]
source = "/home/me/acme.ade.toml"
`)
	cfg, err := Load(base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Missing || cfg.Overlay != filepath.Join(dir, "config.local.toml") {
		t.Fatalf("Missing=%v Overlay=%q", cfg.Missing, cfg.Overlay)
	}
	if cfg.User.Handle != "jdoe" || cfg.User.Timezone != "Europe/Berlin" {
		t.Errorf("user: table keys merge, scalars replace: %+v", cfg.User)
	}
	w := cfg.Forges["work"]
	if w.Kind != "gitlab" || w.URL != "https://gitlab.example.com" || w.TokenCmd != "echo local" {
		t.Errorf("forges.work must keep kind/url and take the overlay's token_cmd: %+v", w)
	}
	if _, ok := cfg.Forges["personal"]; !ok {
		t.Error("a forge only in the base disappeared")
	}
	if !slices.Equal(cfg.Notify.Toast, []string{"budget.*"}) {
		t.Errorf("arrays replace, not extend: %v", cfg.Notify.Toast)
	}
	if cfg.Host.Editor != "code" {
		t.Errorf("a key only in the overlay: host.editor = %q", cfg.Host.Editor)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Source != "/home/me/acme.ade.toml" {
		t.Errorf("arrays of tables replace as a whole: %+v", cfg.Projects)
	}
	if cfg.Update.Concurrency != 4 {
		t.Errorf("defaults still apply under both files: %+v", cfg.Update)
	}
}

func TestConfigLocalOverrideAloneIsNotMissing(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.local.toml"), "[user]\nhandle = \"solo\"\n")
	cfg, err := Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Missing || cfg.User.Handle != "solo" {
		t.Fatalf("%+v", cfg)
	}
}

func TestConfigLocalOverrideErrorsNameTheOverlayFile(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "config.toml")
	write(t, base, "[user]\nhandle = \"a\"\n")
	write(t, filepath.Join(dir, "config.local.toml"), "[user\nhandle = ")
	_, err := Load(base)
	if err == nil || !strings.Contains(err.Error(), "config.local.toml") {
		t.Fatalf("err = %v", err)
	}
	write(t, filepath.Join(dir, "config.local.toml"), "[user]\nhandel = \"typo\"\n")
	cfg, err := Load(base)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.Undecoded, "user.handel") {
		t.Errorf("unknown overlay key must be reported: %v", cfg.Undecoded)
	}
}

func TestManifestLocalOverrideKeepsTheRestOfARepo(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "acme.ade.toml")
	write(t, base, `
name = "acme"
[repos.api]
path = "acme/api"
default_branch = "main"
[repos.api.tasks.dev]
cmd  = "make run"
port = "auto"
env  = { A = "1", B = "2" }
[repos.web]
path = "acme/web"
[groups]
dev = ["api.dev"]
`)
	write(t, filepath.Join(dir, "acme.ade.local.toml"), `
[repos.api]
url = "git@example.com:me/api.git"
[repos.api.tasks.dev]
port = 8080
env  = { B = "local" }
[repos.api.tasks.lint]
cmd = "make lint"
`)
	m, err := ReadManifestFile(base)
	if err != nil {
		t.Fatal(err)
	}
	api := m.Repos["api"]
	if api.Path != "acme/api" || api.DefaultBranch != "main" || api.URL != "git@example.com:me/api.git" {
		t.Errorf("repo keys merge: %+v", api)
	}
	dev := api.Tasks["dev"]
	if dev.Cmd != "make run" || dev.Port.Fixed != 8080 || dev.Port.Auto {
		t.Errorf("task: cmd kept, port replaced: %+v", dev)
	}
	if dev.Env["A"] != "1" || dev.Env["B"] != "local" {
		t.Errorf("env tables merge per key: %v", dev.Env)
	}
	if api.Tasks["lint"].Cmd != "make lint" || m.Repos["web"].Path != "acme/web" {
		t.Errorf("additions and untouched repos: %+v", m.Repos)
	}
	if m.Overlay != filepath.Join(dir, "acme.ade.local.toml") || m.Source != base {
		t.Errorf("Overlay=%q Source=%q", m.Overlay, m.Source)
	}
}

func TestManifestWithoutOverlayIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "acme.ade.toml")
	write(t, base, "name = \"acme\"\n")
	m, err := ReadManifestFile(base)
	if err != nil || m.Name != "acme" || m.Overlay != "" {
		t.Fatalf("%+v %v", m, err)
	}
}
