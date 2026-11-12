package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/store"
)

// promptEnv points ADE at a temp config, root and cache and builds one worktree
// of acme/api for item 412 by hand: the files `ade start` leaves behind.
func promptEnv(t *testing.T) (root, tree string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "projects")
	cfgFile := filepath.Join(base, "config.toml")
	t.Setenv("ADE_CONFIG", cfgFile)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	if err := os.WriteFile(cfgFile, []byte("[paths]\nroot = \""+root+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(root, "acme", "api", ".bare", "worktrees", "412-rate-limit")
	tree = filepath.Join(root, "acme", "api", "412-rate-limit")
	for dir, files := range map[string]map[string]string{
		gitDir: {
			"HEAD":            "ref: refs/heads/412-rate-limit\n",
			"config.worktree": "[ade]\n\tparent = 412\n\tcommitPrefix = GL-412\n",
		},
		tree: {".git": "gitdir: " + gitDir + "\n"},
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root, tree
}

func promptCache(t *testing.T) {
	t.Helper()
	st, err := store.Open(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "ade", "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := forge.Ref{Project: "g/tracker", IID: 1, Kind: forge.KindIssue}
	wi := domain.WorkItem{Project: "acme", Ref: r, Issue: forge.Issue{Ref: r}, Court: domain.CourtYou, Stage: domain.StageReview, Assigned: true}
	b, _ := json.Marshal(wi)
	if _, err := st.DB.Exec(`INSERT INTO sync_state (project, json) VALUES ('acme', '{"project":"acme","reviews":[{"ref":{"project":"g/api","iid":7}}]}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO work_items (project, ref, json) VALUES ('acme', ?, ?)`, wi.Ref.String(), string(b)); err != nil {
		t.Fatal(err)
	}
}

func runPrompt(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return (&cliEnv{t: t}).run(newPromptCmd, args...)
}

func TestPromptInAndOutOfTree(t *testing.T) {
	root, tree := promptEnv(t)
	promptCache(t)

	out, err := runPrompt(t, "--path", tree)
	if err != nil || out != "GL-412 !2\n" {
		t.Fatalf("in tree: %q, %v", out, err)
	}
	out, err = runPrompt(t, "--path", tree, "--format", "{project}/{repo} {branch} {items}+{reviews}")
	if err != nil || out != "acme/api 412-rate-limit 1+1\n" {
		t.Fatalf("format: %q, %v", out, err)
	}

	// The working directory is the default.
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) }) //nolint:errcheck
	if err := os.Chdir(tree); err != nil {
		t.Fatal(err)
	}
	if out, err := runPrompt(t); err != nil || out != "GL-412 !2\n" {
		t.Fatalf("cwd: %q, %v", out, err)
	}

	// Outside every ADE tree: nothing on stdout, no error.
	if out, err := runPrompt(t, "--path", filepath.Dir(root)); err != nil || out != "" {
		t.Fatalf("outside: %q, %v", out, err)
	}

	asJSON(t)
	out, err = runPrompt(t, "--path", tree)
	var got struct {
		InTree    bool           `json:"in_tree"`
		Ticket    string         `json:"ticket"`
		Parent    int            `json:"parent"`
		Branch    string         `json:"branch"`
		Cache     string         `json:"cache"`
		Attention map[string]any `json:"attention"`
	}
	if err != nil || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("json: %q, %v", out, err)
	}
	if !got.InTree || got.Ticket != "GL-412" || got.Parent != 412 || got.Branch != "412-rate-limit" || got.Cache != "ok" || got.Attention["total"] != float64(2) {
		t.Errorf("json = %+v", got)
	}
	out, err = runPrompt(t, "--path", filepath.Dir(root))
	if err != nil || strings.Contains(out, "ticket") || !strings.Contains(out, `"in_tree": false`) {
		t.Errorf("json outside a tree: %q, %v", out, err)
	}
}

// A prompt runs on every command line: it must neither need the network or a
// valid forge setup, nor leave anything behind (no audit line, no cache).
func TestPromptLeavesNothingBehind(t *testing.T) {
	_, tree := promptEnv(t) // no cache.db was made
	out, err := runPrompt(t, "--path", tree)
	if err != nil || out != "GL-412\n" {
		t.Fatalf("without a cache: %q, %v", out, err)
	}
	for _, dir := range []string{os.Getenv("XDG_CACHE_HOME"), os.Getenv("XDG_DATA_HOME")} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s was created (%v)", dir, err)
		}
	}
}

func TestPromptRejectsABadFormat(t *testing.T) {
	_, tree := promptEnv(t)
	if _, err := runPrompt(t, "--path", tree, "--format", "{nope}"); err == nil {
		t.Error("an unknown placeholder was accepted")
	}
	if _, err := runPrompt(t, "--path", tree, "--scope", "everything"); err == nil {
		t.Error("a bad scope was accepted")
	}
}
