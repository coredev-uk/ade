package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/host"
)

// fakeLauncherZellij puts a `zellij` on PATH that logs its arguments and answers the
// queries the launcher makes; the returned func reads the logged calls.
func fakeLauncherZellij(t *testing.T, sessions, tabs, panes string) (calls func() []string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> "$FAKEZ_LOG"
[ "$1" = list-sessions ] && printf '%s' "$FAKEZ_SESSIONS"
for a in "$@"; do
	case "$a" in
	query-tab-names) printf '%s' "$FAKEZ_TABS" ;;
	list-panes) printf '%s' "$FAKEZ_PANES" ;;
	esac
done
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "zellij"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKEZ_LOG", log)
	t.Setenv("FAKEZ_SESSIONS", sessions)
	t.Setenv("FAKEZ_TABS", tabs)
	t.Setenv("FAKEZ_PANES", panes)
	return func() []string {
		data, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

func launchCmd() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())
	return cmd, &out
}

const (
	liveSession = "ade [Created 5s ago]\n"
	deadHome    = `[{"is_plugin":false,"exited":true,"tab_id":4,"tab_name":"home"}]`
	liveHome    = `[{"is_plugin":false,"exited":false,"tab_id":4,"tab_name":"home"}]`
)

func TestShowHomeTabDryRunTouchesNothing(t *testing.T) {
	calls := fakeLauncherZellij(t, liveSession, "home\n", liveHome)
	cmd, out := launchCmd()
	layoutFile := filepath.Join(t.TempDir(), "layout.kdl")
	if err := showHomeTab(cmd, host.OpenHome, "mine", "layout {}", layoutFile, true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`zellij cannot nest sessions`, `"mine"`, "# layout file: " + layoutFile, `# focus tab "home" of session "mine"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(layoutFile); err == nil {
		t.Error("a dry run wrote the layout")
	}
	if c := calls(); len(c) != 1 || c[0] != "" {
		t.Errorf("a dry run called zellij: %v", c)
	}
}

func TestShowHomeTabFocusesALiveDashboardAndRecreatesADeadOne(t *testing.T) {
	cases := []struct {
		name, panes string
		want        []string
	}{
		{"live", liveHome, []string{"--session ade action go-to-tab-name home"}},
		{"exited", deadHome, []string{"--session ade action new-tab --layout LAYOUT --name home", "--session ade action close-tab-by-id 4"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := fakeLauncherZellij(t, liveSession, "home\n", c.panes)
			cmd, out := launchCmd()
			layoutFile := filepath.Join(t.TempDir(), "layout.kdl")
			if err := showHomeTab(cmd, host.FocusHome, "ade", "layout {}", layoutFile, false); err != nil {
				t.Fatal(err)
			}
			if out.Len() != 0 {
				t.Errorf("FocusHome is silent: %q", out)
			}
			if data, err := os.ReadFile(layoutFile); err != nil || string(data) != "layout {}" {
				t.Errorf("layout file: %q %v", data, err)
			}
			got := strings.Join(calls(), "\n")
			pos := -1
			for _, w := range c.want {
				i := strings.Index(got, strings.ReplaceAll(w, "LAYOUT", layoutFile))
				if i <= pos {
					t.Fatalf("calls lack %q in order:\n%s", w, got)
				}
				pos = i
			}
		})
	}
}

func TestShowHomeTabFailsWhenTheSessionIsGone(t *testing.T) {
	fakeLauncherZellij(t, "", "", "")
	cmd, _ := launchCmd()
	err := showHomeTab(cmd, host.FocusHome, "ade", "layout {}", filepath.Join(t.TempDir(), "layout.kdl"), false)
	if err == nil || !strings.Contains(err.Error(), "open the home tab") {
		t.Fatalf("err = %v", err)
	}
}

func TestFocusHomeBeforeAttachWarnsAndCarriesOn(t *testing.T) {
	calls := fakeLauncherZellij(t, liveSession, "agent\nhome\n", liveHome)
	var warn bytes.Buffer
	focusHomeBeforeAttach(context.Background(), &warn, "/tmp/layout.kdl")
	if warn.Len() != 0 || !strings.Contains(strings.Join(calls(), "\n"), "--session ade action go-to-tab-name home") {
		t.Fatalf("warn %q calls %v", &warn, calls())
	}

	fakeLauncherZellij(t, "", "", "")
	warn.Reset()
	focusHomeBeforeAttach(context.Background(), &warn, "/tmp/layout.kdl")
	if !strings.Contains(warn.String(), "could not focus the home tab") {
		t.Fatalf("a failed focus must warn: %q", &warn)
	}
}
