package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestVSCodeSnippetIsValidSettingsJSON(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal([]byte(VSCodeSnippet()), &got); err != nil {
		t.Fatal(err)
	}
	skip, _ := got["terminal.integrated.commandsToSkipShell"].([]any)
	var removed []string
	for _, s := range skip {
		removed = append(removed, s.(string))
	}
	for _, want := range []string{"-workbench.action.quickOpen", "-workbench.action.toggleSidebarVisibility", "-workbench.action.togglePanel"} {
		if !contains(removed, want) {
			t.Errorf("snippet does not release %s: %v", want, removed)
		}
	}
	for _, s := range removed {
		if !strings.HasPrefix(s, "-") {
			t.Errorf("%q would ADD a command to the skip list; the snippet only releases keys", s)
		}
	}
	if got["terminal.integrated.allowChords"] != false {
		t.Error("chords must stay with the terminal program")
	}
}

func TestVSCodeHostCheck(t *testing.T) {
	cases := []struct {
		name string
		mode string
		env  func(string) string
		want string
	}{
		{"vscode terminal, no zellij", "auto", env("TERM_PROGRAM", "vscode"), "code <worktree>"},
		{"zellij inside vscode", "auto", env("TERM_PROGRAM", "vscode", "ZELLIJ", "0"), "zellij tabs"},
		{"forced vscode", "vscode", env(), "host.mode = vscode"},
		{"forced zellij inside vscode", "zellij", env("TERM_PROGRAM", "vscode"), "forced"},
		{"plain terminal", "auto", env(), "not the host"},
	}
	for _, tc := range cases {
		c := vscodeHostCheck(Options{Config: &config.Config{Host: config.Host{Mode: tc.mode}}, Getenv: tc.env})
		if c.Status != OK || !strings.Contains(c.Detail, tc.want) {
			t.Errorf("%s: %+v", tc.name, c)
		}
	}
}

func TestTasksCheck(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "proj", "web", "main")
	os.MkdirAll(filepath.Join(wt, ".vscode"), 0o755) //nolint:errcheck
	o := Options{Root: root, Getenv: env()}

	c, ok := tasksCheck(o, wt)
	if !ok || c.Status != Warn || c.Fix != "ade vscode tasks" {
		t.Fatalf("missing in a worktree: %+v %v", c, ok)
	}
	if _, ok := tasksCheck(o, t.TempDir()); ok {
		t.Fatal("a directory outside the projects root has nothing to report")
	}
	path := filepath.Join(wt, ".vscode", "tasks.json")
	os.WriteFile(path, []byte(`{"tasks":[{"label":"lint"}]}`), 0o644) //nolint:errcheck
	if c, _ := tasksCheck(o, wt); c.Status != Warn || !strings.Contains(c.Detail, "no generated ADE tasks") {
		t.Fatalf("only user tasks: %+v", c)
	}
	os.WriteFile(path, []byte(`{"tasks":[{"label":"ade: web.dev"},{"label":"ade: up"},{"label":"lint"}]}`), 0o644) //nolint:errcheck
	if c, _ := tasksCheck(o, wt); c.Status != OK || !strings.Contains(c.Detail, "2 generated") {
		t.Fatalf("generated: %+v", c)
	}
	os.WriteFile(path, []byte("// mine\n{\"tasks\":[]}"), 0o644) //nolint:errcheck
	if c, _ := tasksCheck(o, wt); c.Status != Warn || !strings.Contains(c.Detail, "not plain JSON") {
		t.Fatalf("commented file: %+v", c)
	}
}

func TestSettingsCheck(t *testing.T) {
	home := t.TempDir()
	o := Options{Getenv: env()}
	if c := settingsCheck(o, home); c.Status != Warn || !strings.Contains(c.Detail, "no VS Code settings.json") {
		t.Fatalf("none: %+v", c)
	}
	dir := filepath.Join(home, ".config", "Code", "User")
	os.MkdirAll(dir, 0o755) //nolint:errcheck
	settings := filepath.Join(dir, "settings.json")
	os.WriteFile(settings, []byte(`{"editor.fontSize": 14}`), 0o644) //nolint:errcheck
	if c := settingsCheck(o, home); c.Status != Warn || !strings.Contains(c.Detail, "does not release") || !strings.Contains(c.Fix, "ade doctor --vscode") {
		t.Fatalf("unconfigured: %+v", c)
	}
	// JSONC with comments is read as text.
	os.WriteFile(settings, []byte("{\n// keys for zellij\n\"terminal.integrated.commandsToSkipShell\": [\"-workbench.action.quickOpen\"]\n}"), 0o644) //nolint:errcheck
	if c := settingsCheck(o, home); c.Status != OK {
		t.Fatalf("configured: %+v", c)
	}
}
