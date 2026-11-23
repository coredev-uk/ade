package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/workspace"
)

func TestEveryRefusedCommandExists(t *testing.T) {
	root := newRoot()
	for path := range agentRefused {
		cmd, _, err := root.Find(strings.Fields(path))
		if err != nil || cmd.CommandPath() != "ade "+path {
			t.Errorf("agentRefused lists %q, which is not a command (renamed?): %v", path, err)
			continue
		}
		if cmd.Annotations[annAgentRefused] == "" {
			t.Errorf("%q is not marked", path)
		}
	}
}

func TestAgentRunGuardRefusesWritesApprovalsAndTriage(t *testing.T) {
	root := newRoot()
	find := func(path string) *cobra.Command {
		t.Helper()
		cmd, _, err := root.Find(strings.Fields(path))
		if err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	refused := []string{"review submit", "agent approve", "agent push", "ai triage accept", "mr merge", "mr reply", "mr approve", "issue weight", "start"}
	reads := []string{"mr show", "mr diff", "review list", "agent list", "agent show", "agent diff", "item", "inbox"}

	t.Setenv(workspace.AgentRunEnv, "1")
	for _, path := range refused {
		err := checkAgentRun(find(path))
		if err == nil || !strings.Contains(err.Error(), "refused while an agent run is in progress") || !strings.Contains(err.Error(), "ade "+path) {
			t.Errorf("%q in an agent run: %v", path, err)
		}
	}
	for _, path := range reads {
		if err := checkAgentRun(find(path)); err != nil {
			t.Errorf("reading %q must stay possible for an agent: %v", path, err)
		}
	}
	// Triage's own list is a read; only its decisions are refused.
	if err := checkAgentRun(find("ai triage")); err != nil {
		t.Errorf("triage's list is a read: %v", err)
	}

	t.Setenv(workspace.AgentRunEnv, "")
	for _, path := range refused {
		if err := checkAgentRun(find(path)); err != nil {
			t.Errorf("%q outside an agent run: %v", path, err)
		}
	}
}

// Through the real root: the check runs after the app loads and leaves its
// cli.* audit line, and the command never starts.
func TestAgentRunGuardThroughTheRoot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte("[user]\nhandle = \"a\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADE_CONFIG", cfg)
	t.Setenv(workspace.AgentRunEnv, "1")

	for _, args := range [][]string{
		{"review", "submit", "g/api!1", "--summary", "x"},
		{"agent", "approve", "nope"},
		{"ai", "triage", "accept", "g/api!1", "--all"},
		{"mr", "merge", "g/api!1", "--yes"},
	} {
		cmd := newRoot()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		if err == nil || !strings.Contains(err.Error(), "refused while an agent run is in progress") {
			t.Errorf("ade %s with ADE_AGENT_RUN=1: %v", strings.Join(args, " "), err)
		}
	}
}
