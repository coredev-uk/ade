package onboard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
)

func TestRenderRoundTripsThroughConfigLoad(t *testing.T) {
	tests := []struct {
		name string
		a    Answers
		want config.Forge
	}{
		{
			"self-hosted gitlab with env token",
			Answers{Handle: "jdoe", ForgeName: "work", Kind: "gitlab", URL: "https://gitlab.example.com", TokenEnv: "GITLAB_TOKEN", GitProtocol: "https", Root: "~/ade"},
			config.Forge{Kind: "gitlab", URL: "https://gitlab.example.com", TokenEnv: "GITLAB_TOKEN", GitProtocol: "https"},
		},
		{
			"github with command token and the public host implied",
			Answers{Handle: "jdoe", ForgeName: "personal", Kind: "github", TokenCmd: "gh auth token", GitProtocol: "ssh", Root: "/srv/ade"},
			config.Forge{Kind: "github", URL: "https://github.com", TokenCmd: "gh auth token", GitProtocol: "ssh"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := Render(tt.a)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("rendered config does not load: %v\n%s", err, data)
			}
			if len(cfg.Undecoded) > 0 {
				t.Errorf("unknown keys written: %v", cfg.Undecoded)
			}
			if got := cfg.Forges[tt.a.ForgeName]; got != tt.want {
				t.Errorf("forge = %+v, want %+v", got, tt.want)
			}
			if cfg.User.Handle != tt.a.Handle || cfg.Paths.Root != tt.a.Root {
				t.Errorf("user/paths = %+v %+v", cfg.User, cfg.Paths)
			}
			if strings.Contains(string(data), "token = ") {
				t.Errorf("a literal token key was written:\n%s", data)
			}
		})
	}
}

func TestRenderRejectsIncompleteAnswers(t *testing.T) {
	ok := Answers{ForgeName: "f", Kind: "gitlab", TokenEnv: "T", GitProtocol: "ssh", Root: "~/ade"}
	if _, err := Render(ok); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	for name, mutate := range map[string]func(*Answers){
		"kind":     func(a *Answers) { a.Kind = "bitbucket" },
		"name":     func(a *Answers) { a.ForgeName = "" },
		"token":    func(a *Answers) { a.TokenEnv = "" },
		"protocol": func(a *Answers) { a.GitProtocol = "ftp" },
		"root":     func(a *Answers) { a.Root = "" },
	} {
		a := ok
		mutate(&a)
		if _, err := Render(a); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestWriteConfig(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 30, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "nested", "ade", "config.toml")

	if _, err := WriteConfig(path, []byte("first\n"), false, now); err != nil {
		t.Fatalf("fresh write: %v", err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600", st.Mode().Perm())
	}

	// No force: refuse, and leave the file alone.
	_, err := WriteConfig(path, []byte("second\n"), false, now)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "first\n" {
		t.Errorf("refused write still changed the file: %q", got)
	}

	// Force: backed up first, then replaced.
	backup, err := WriteConfig(path, []byte("second\n"), true, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := path + ".bak-20261111-093000"; backup != want {
		t.Errorf("backup = %s, want %s", backup, want)
	}
	if got, _ := os.ReadFile(backup); string(got) != "first\n" {
		t.Errorf("backup holds %q, want the original", got)
	}
	if got, _ := os.ReadFile(path); string(got) != "second\n" {
		t.Errorf("config holds %q after force", got)
	}

	// A second forced write in the same second must not clobber the first backup.
	backup2, err := WriteConfig(path, []byte("third\n"), true, now)
	if err != nil {
		t.Fatal(err)
	}
	if backup2 == backup {
		t.Fatal("second backup reused the first backup's name")
	}
	if got, _ := os.ReadFile(backup); string(got) != "first\n" {
		t.Errorf("first backup overwritten: %q", got)
	}
	if got, _ := os.ReadFile(backup2); string(got) != "second\n" {
		t.Errorf("second backup holds %q", got)
	}

	// No stray temp files beside the config.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	tests := []struct {
		kind, in, want string
		wantErr        bool
	}{
		{"gitlab", "", "", false},
		{"gitlab", "gitlab.com", "", false},
		{"gitlab", "https://gitlab.com/", "", false},
		{"github", "github.com", "", false},
		{"gitlab", "gitlab.example.com", "https://gitlab.example.com", false},
		{"gitlab", " https://git.example.com:8443/ ", "https://git.example.com:8443", false},
		{"gitlab", "http://127.0.0.1:9000", "http://127.0.0.1:9000", false},
		{"gitlab", "gitlab.com", "", false},
		{"github", "gitlab.com", "https://gitlab.com", false}, // kind decides the public host, not the name
		{"gitlab", "ssh://git.example.com", "", true},
		{"gitlab", "https://", "", true},
	}
	for _, tt := range tests {
		got, err := NormalizeHost(tt.kind, tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("NormalizeHost(%s, %q) = %q, %v; want %q (err %v)", tt.kind, tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestParseTokenSource(t *testing.T) {
	tests := []struct {
		in, env, cmd string
		wantErr      bool
	}{
		{"GITLAB_TOKEN", "GITLAB_TOKEN", "", false},
		{"  MY_TOKEN ", "MY_TOKEN", "", false},
		{"!gh auth token", "", "gh auth token", false},
		{"! pass show ade/gitlab", "", "pass show ade/gitlab", false},
		{"", "", "", true},
		{"!", "", "", true},
		{"gh auth token", "", "", true}, // a command without the marker is a mistake, not a variable name
		{"$GITLAB_TOKEN", "", "", true},
	}
	for _, tt := range tests {
		env, cmd, err := ParseTokenSource(tt.in)
		if (err != nil) != tt.wantErr || env != tt.env || cmd != tt.cmd {
			t.Errorf("ParseTokenSource(%q) = %q, %q, %v", tt.in, env, cmd, err)
		}
	}
}

func TestForgeName(t *testing.T) {
	tests := []struct {
		named   []string
		want    string
		wantErr bool
	}{
		{nil, "gitlab", false},
		{[]string{""}, "gitlab", false},
		{[]string{"work"}, "work", false},
		{[]string{"work", "", "work"}, "work", false},
		{[]string{"work", "personal"}, "", true},
	}
	for _, tt := range tests {
		got, err := ForgeName(tt.named, "gitlab")
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ForgeName(%v) = %q, %v", tt.named, got, err)
		}
	}
}
