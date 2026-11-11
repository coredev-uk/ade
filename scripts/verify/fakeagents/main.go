// Command fakeagents seeds agent runs in every state into the item record of
// tracker#1, for scripts/verify/capture-agents.sh: no agent is ever started.
// It reads the isolated environment (ADE_CONFIG, XDG_*) like ade itself and
// makes one real git worktree, so the review run has a real diff.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/coredev-uk/ade/internal/agent"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/forge"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagents:", err)
		os.Exit(1)
	}
}

func git(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Demo", "GIT_AUTHOR_EMAIL=demo@example.com", "GIT_COMMITTER_NAME=Demo", "GIT_COMMITTER_EMAIL=demo@example.com")
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "git", args, err)
		os.Exit(1)
	}
	return string(out)
}

func run() error {
	a, err := app.Load("user")
	if err != nil {
		return err
	}
	defer a.Close()
	m, err := agent.ForApp(a)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "ade-fakeagents-*")
	if err != nil {
		return err
	}
	main := filepath.Join(tmp, "api")
	git(tmp, "init", "-q", "-b", "main", main)
	if err := os.WriteFile(filepath.Join(main, "README.md"), []byte("api\n"), 0o644); err != nil {
		return err
	}
	git(main, "add", ".")
	git(main, "commit", "-q", "-m", "init")
	wt := filepath.Join(tmp, "1-rate-limit-export")
	git(main, "worktree", "add", "-q", "-b", "1-rate-limit-export", wt)
	base := git(wt, "rev-parse", "HEAD")
	for name, body := range map[string]string{
		"limit/bucket.go":        "package limit\n\n// Bucket is a token bucket.\ntype Bucket struct{ tokens int64 }\n\nfunc (b *Bucket) Allow() bool { return b.tokens > 0 }\n",
		"export/handler.go":      "package export\n\nfunc Handle() {}\n",
		"export/handler_test.go": "package export\n",
	} {
		p := filepath.Join(wt, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}

	now := time.Now()
	tracker := forge.Ref{Project: "ade-demo-env/tracker", IID: 1, Kind: forge.KindIssue}
	mr := forge.Ref{Project: "ade-demo-env/api", IID: 1, Kind: forge.KindMR}
	mk := func(id string, kind agent.Kind, st agent.State, cost float64, spent time.Duration, stop string) agent.Run {
		r := agent.Run{
			ID: id, Kind: kind, State: st, Forge: "sandbox-gitlab", Project: "demo-gl", Tracker: tracker, Repo: "api",
			Worktree: wt, Branch: "1-rate-limit-export", MR: &mr, Profile: "p", Driver: "omp", Model: "anthropic/claude-haiku-5-5",
			CapUSD: 0.5, CostUSD: cost, Created: now.Add(-time.Hour), Updated: now,
			Calls: []agent.Call{{Kind: agent.KindPlan, Driver: "omp", Started: now.Add(-spent), Ended: now, CostUSD: cost, Turns: 4, Source: "reported", StoppedBy: stop}},
		}
		return r
	}
	save := func(r agent.Run) error { return m.Store.Save(r) }
	dir := func(r agent.Run) string { return m.Store.Dir(r) }

	running := mk("20261111-141500-c4d", agent.KindPlan, agent.StateRunning, 0.0712, 95*time.Second, "")
	running.Calls[0].Kind, running.Calls[0].Ended = agent.KindRun, time.Time{}
	running.Approved = true
	review := mk("20261111-132000-b2e", agent.KindPlan, agent.StateReview, 0.0634, 143*time.Second, "")
	awaiting := mk("20261111-124000-a9f", agent.KindPlan, agent.StateAwaiting, 0.0123, 38*time.Second, "")
	failed := mk("20261111-110000-f01", agent.KindPlan, agent.StateFailed, 0.5, 7*time.Minute, agent.StopCostCap)
	failed.Error = "cost cap reached (spent $0.5012)"
	failed.Calls[0].Exit = 137 // SIGKILL from the cap
	pushed := mk("20261110-160000-e77", agent.KindAddress, agent.StatePushed, 0.0301, 61*time.Second, "")
	pushed.Commits = []string{"3f9a2c1"}

	for _, r := range []agent.Run{running, review, awaiting, failed, pushed} {
		if err := save(r); err != nil {
			return err
		}
	}
	plan := "# Plan: rate-limit the export endpoint\n\nAdd a token bucket in limit/bucket.go and wire it into export/handler.go so that no tenant exceeds 10 exports a minute.\n\n" +
		"- limit/bucket.go: Bucket with Allow() and a refill per second\n- export/handler.go: reject with 429 and a Retry-After header when the bucket is empty\n- export/handler_test.go: table test for the 11th request in a minute\n\n" +
		"Verification: go test ./export/... ./limit/...\n\nOpen question: should the limit be per tenant or per token?\n"
	if err := os.WriteFile(filepath.Join(dir(awaiting), agent.FilePlan), []byte(plan), 0o644); err != nil {
		return err
	}
	// The review run's base is the real base commit.
	review.BaseSHA = trim(base)
	if err := save(review); err != nil {
		return err
	}
	fmt.Println(tmp)
	return nil
}

func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
