package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/agent"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/records"
)

type agEnv struct {
	t    *testing.T
	m    model
	mgr  *agent.Manager
	tmp  string
	bare string
	wt   string
}

func agGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newAgEnv builds the dashboard over a real run store, with a start-work-like
// worktree behind a bare origin so push is real. Agent processes are never
// started: runs are seeded in the states under test.
func newAgEnv(t *testing.T, g glyphs, w int) *agEnv {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "origin.git")
	agGit(t, tmp, "init", "-q", "--bare", "-b", "main", bare)
	main := filepath.Join(tmp, "api")
	agGit(t, tmp, "init", "-q", "-b", "main", main)
	agGit(t, main, "config", "user.name", "Test User")
	agGit(t, main, "config", "user.email", "test@example.com")
	agGit(t, main, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(main, "README.md"), []byte("api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agGit(t, main, "add", ".")
	agGit(t, main, "commit", "-q", "-m", "init")
	agGit(t, main, "push", "-q", "origin", "main")
	wt := filepath.Join(tmp, "1-rate-limit")
	agGit(t, main, "worktree", "add", "-q", "-b", "1-rate-limit", wt)

	cfg := config.Defaults()
	a := &app.App{Paths: paths.Paths{CacheDir: filepath.Join(tmp, "cache")}, Config: &cfg, Bus: events.NewBus(), RecordsDir: filepath.Join(tmp, "rec")}
	t.Cleanup(func() { a.Close() })
	al, err := audit.Open(a.RecordsDir, "user")
	if err != nil {
		t.Fatal(err)
	}
	a.Audit = al
	cache, err := a.Cache()
	if err != nil {
		t.Fatal(err)
	}
	mgr := agent.NewManager(&cfg, agent.NewStore(cache, records.New(a.RecordsDir), a.RecordsDir), al, a.Bus)
	mgr.GitFor = func(context.Context, agent.Run) (git.Client, error) { return git.Client{}, nil }
	old := agentOpen
	agentOpen = func(*app.App) (*agent.Manager, error) { return mgr, nil }
	t.Cleanup(func() { agentOpen = old })

	m := model{app: a, cache: cache, ctx: context.Background(), g: g, nc: newNotifCenter(notify.NewInbox(cache)), w: w, h: 44}
	res := sampleResult()
	mr := forge.MR{Ref: forge.Ref{Project: "g/api", IID: 3, Kind: forge.KindMR}, State: "opened", Unresolved: 2}
	res.Items[1].MRs = []forge.MR{mr}
	next, _ := m.Update(resultsMsg{results: []forgesync.Result{res}})
	m = next.(model)
	m.homeSel = 0
	for i, r := range m.homeRows {
		if r.ref() == "g/tracker#1" {
			m.homeSel = i
		}
	}
	return &agEnv{t: t, m: m, mgr: mgr, tmp: tmp, bare: bare, wt: wt}
}

func (e *agEnv) seed(id string, state agent.State, cost float64) agent.Run {
	e.t.Helper()
	now := time.Now()
	r := agent.Run{
		ID: id, Kind: agent.KindPlan, State: state, Forge: "gl", Project: "demo",
		Tracker: forge.Ref{Project: "g/tracker", IID: 1, Kind: forge.KindIssue}, Repo: "api",
		Worktree: e.wt, Branch: "1-rate-limit", Driver: "omp", Model: "m", CapUSD: 0.5, CostUSD: cost,
		Created: now, Updated: now,
		Calls: []agent.Call{{Kind: agent.KindPlan, Started: now.Add(-72 * time.Second), Ended: now, CostUSD: cost, Turns: 3}},
	}
	if err := e.mgr.Store.Save(r); err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *agEnv) writePlan(r agent.Run, text string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.mgr.Store.Dir(r), agent.FilePlan), []byte(text), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// settle runs a command and feeds its answers back, skipping the one-second
// poll tick (a command that does not answer in time is a timer).
func (e *agEnv) settle(cmd tea.Cmd) {
	e.t.Helper()
	var run func(c tea.Cmd, depth int)
	run = func(c tea.Cmd, depth int) {
		if c == nil || depth > 6 {
			return
		}
		ch := make(chan tea.Msg, 1)
		go func() { ch <- c() }()
		select {
		case msg := <-ch:
			if b, ok := msg.(tea.BatchMsg); ok {
				for _, sub := range b {
					run(sub, depth+1)
				}
				return
			}
			if msg == nil {
				return
			}
			next, c2 := e.m.Update(msg)
			e.m = next.(model)
			run(c2, depth+1)
		case <-time.After(300 * time.Millisecond):
		}
	}
	run(cmd, 0)
}

func (e *agEnv) press(key string) {
	e.t.Helper()
	next, cmd := e.m.Update(keyPress(key))
	e.m = next.(model)
	e.settle(cmd)
}

func (e *agEnv) view() string { return ansi.Strip(e.m.render()) }

func TestAgentMenuListsRunsWithStateCostAndElapsed(t *testing.T) {
	e := newAgEnv(t, unicodeGlyphs, 120)
	e.seed("20261111-100000-aaa", agent.StateAwaiting, 0.0123)
	e.seed("20261111-110000-bbb", agent.StateFailed, 0.31)
	e.seed("20261111-120000-ccc", agent.StateRunning, 0.05)
	e.press("a")
	out := e.view()
	for _, want := range []string{
		"Agent · g/tracker#1", "Rate-limit the export endpoint",
		"p plan", "c address comments", "g/api!3 · 2 unresolved",
		"▲ needs approval", "✕ failed", "⟳ running",
		"$0.0123/$0.50", "$0.3100/$0.50", "1m12s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("agent menu lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "20261111-120000-ccc") {
		t.Errorf("runs newest first: %s", out)
	}
	// Esc closes; the dashboard is back.
	e.press("esc")
	if e.m.agt != nil || strings.Contains(e.view(), "Agent ·") {
		t.Error("esc did not close the dialog")
	}
}

func TestAgentMenuScopesToTheItemAndAllRunsListEverything(t *testing.T) {
	e := newAgEnv(t, unicodeGlyphs, 120)
	e.seed("20261111-100000-aaa", agent.StateReview, 0.04)
	other := e.seed("20261111-110000-bbb", agent.StateReview, 0.02)
	other.Tracker.IID = 3
	if err := e.mgr.Store.Save(other); err != nil {
		t.Fatal(err)
	}
	e.press("a")
	out := e.view()
	if !strings.Contains(out, "20261111-100000-aaa") || strings.Contains(out, "20261111-110000-bbb") {
		t.Errorf("the menu of #1 must list only #1's runs:\n%s", out)
	}
	e.press("esc")
	e.press("A")
	out = e.view()
	// Every-item rows drop the date from the id to keep the spend and time in view.
	if !strings.Contains(out, "all runs") || !strings.Contains(out, "100000-aaa") || !strings.Contains(out, "110000-bbb") || !strings.Contains(out, "1m12s") || strings.Contains(out, "p plan") {
		t.Errorf("A lists every item's runs and offers no start:\n%s", out)
	}
}

func TestAgentAddressNeedsUnresolvedThreads(t *testing.T) {
	e := newAgEnv(t, unicodeGlyphs, 120)
	// Item #3 has no MR: address comments is not offered, and c explains.
	for i, r := range e.m.homeRows {
		if r.ref() == "g/tracker#3" {
			e.m.homeSel = i
		}
	}
	e.press("a")
	e.press("c")
	out := e.view()
	if !strings.Contains(out, "no open MR with unresolved threads") {
		t.Errorf("c without threads must say why:\n%s", out)
	}
	if e.m.agt.busy != "" {
		t.Error("nothing may start without threads to address")
	}
}

func TestAgentPlanReviewAndApprove(t *testing.T) {
	e := newAgEnv(t, unicodeGlyphs, 120)
	r := e.seed("20261111-100000-aaa", agent.StateAwaiting, 0.012)
	e.writePlan(r, "# Plan\n\nAdd a token bucket in limiter.go and wire it into the export handler.\n")
	e.press("a")
	e.press("enter")
	out := e.view()
	for _, want := range []string{"Plan", "awaiting your approval", "token bucket in limiter.go", "enter approve", "e edit plan"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan review lacks %q:\n%s", want, out)
		}
	}
	e.press("enter") // approve
	got, err := e.mgr.Store.Get(r.ID)
	if err != nil || !got.Approved || got.PlanSHA == "" {
		t.Fatalf("approve in the dialog must record the approval: %+v %v", got, err)
	}
	out = e.view()
	if !strings.Contains(out, "approved") || !strings.Contains(out, "run it in the worktree") {
		t.Errorf("after approving, enter offers to run:\n%s", out)
	}
	if got.State != agent.StateAwaiting {
		t.Errorf("approving must not start the run by itself: %s", got.State)
	}
	// Discarding cancels the run.
	e.press("x")
	if got, _ := e.mgr.Store.Get(r.ID); got.State != agent.StateCancelled {
		t.Errorf("x discards: %s", got.State)
	}
}

func TestAgentReviewShowsDiffAndPushesAfterConfirm(t *testing.T) {
	e := newAgEnv(t, unicodeGlyphs, 120)
	r := e.seed("20261111-100000-aaa", agent.StateReview, 0.04)
	base := agGit(t, e.wt, "rev-parse", "HEAD")
	r.BaseSHA = base
	if err := e.mgr.Store.Save(r); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.wt, "limiter.go"), []byte("package api\n\nfunc Limit() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lazy := ""
	old := agLazygit
	agLazygit = func(wt string) tea.Cmd { lazy = wt; return nil }
	t.Cleanup(func() { agLazygit = old })

	e.press("a")
	e.press("enter")
	out := e.view()
	for _, want := range []string{"Run 20261111-100000-aaa", "review", "limiter.go", "+3", "l lazygit", "P push"} {
		if !strings.Contains(out, want) {
			t.Errorf("review page lacks %q:\n%s", want, out)
		}
	}
	e.press("l")
	if lazy != e.wt {
		t.Errorf("l opens lazygit on the worktree, got %q", lazy)
	}
	e.press("P")
	if out := e.view(); !strings.Contains(out, "commit and push") || !strings.Contains(out, "1-rate-limit") {
		t.Errorf("push must be confirmed first:\n%s", out)
	}
	if got := agGit(t, e.bare, "branch", "--list", "1-rate-limit"); got != "" {
		t.Fatalf("nothing may be pushed before the confirm: %q", got)
	}
	e.press("n")
	if got := agGit(t, e.bare, "branch", "--list", "1-rate-limit"); got != "" || e.m.agt.stage != agDetail {
		t.Errorf("n backs out: %q stage %d", got, e.m.agt.stage)
	}
	e.press("P")
	e.press("y")
	got, _ := e.mgr.Store.Get(r.ID)
	if got.State != agent.StatePushed || len(got.Commits) != 1 {
		t.Fatalf("after y: %+v", got)
	}
	if sha := agGit(t, e.bare, "rev-parse", "1-rate-limit"); sha != got.Commits[0] {
		t.Errorf("origin has %s, run says %s", sha, got.Commits[0])
	}
}

func TestAgentFailedRunShowsItsError(t *testing.T) {
	e := newAgEnv(t, unicodeGlyphs, 100)
	r := e.seed("20261111-100000-aaa", agent.StateFailed, 0.5)
	r.Error = "cost cap reached (spent $0.5012)"
	r.Calls[0].StoppedBy = agent.StopCostCap
	if err := e.mgr.Store.Save(r); err != nil {
		t.Fatal(err)
	}
	e.press("a")
	e.press("enter")
	out := e.view()
	for _, want := range []string{"✕ failed", "cost cap reached", "$0.5000"} {
		if !strings.Contains(out, want) {
			t.Errorf("failed run lacks %q:\n%s", want, out)
		}
	}
	e.press("P")
	if !strings.Contains(e.view(), "only a run in review can be pushed") {
		t.Error("push must refuse a failed run in the dialog")
	}
}

func TestAgentDialogFitsNarrowAndAsciiTerminals(t *testing.T) {
	for _, tc := range []struct {
		g glyphs
		w int
	}{{unicodeGlyphs, 60}, {unicodeGlyphs, 80}, {asciiGlyphs, 80}, {asciiGlyphs, 130}} {
		e := newAgEnv(t, tc.g, tc.w)
		r := e.seed("20261111-100000-aaa", agent.StateAwaiting, 0.0123)
		e.writePlan(r, strings.Repeat("A long plan sentence that has to wrap inside the dialog. ", 12))
		e.seed("20261111-110000-bbb", agent.StateReview, 0.2)
		for _, key := range []string{"a", "enter", "esc"} {
			e.press(key)
			for i, ln := range strings.Split(e.m.render(), "\n") {
				if w := ansi.StringWidth(ln); w > tc.w {
					t.Fatalf("w=%d after %q: line %d is %d wide: %q", tc.w, key, i, w, ansi.Strip(ln))
				}
			}
		}
		if tc.g == asciiGlyphs {
			for e.m.agt != nil {
				e.press("esc")
			}
			e.press("A")
			out := e.view()
			for _, glyph := range []string{"▲", "◉", "⟳", "✕", "✓", "▸", "●"} {
				if strings.Contains(out, glyph) {
					t.Errorf("ascii set leaked %q:\n%s", glyph, out)
				}
			}
			if !strings.Contains(out, "! needs approval") && !strings.Contains(out, "needs approval") {
				t.Errorf("state words must remain:\n%s", out)
			}
		}
	}
}

func TestAgentKeyOnMRPageOpensTheItemsMenu(t *testing.T) {
	e := newAgEnv(t, unicodeGlyphs, 120)
	e.seed("20261111-100000-aaa", agent.StateReview, 0.04)
	it := e.m.homeRows[e.m.homeSel].item
	e.m.mr = &mrPage{item: it}
	// `a` on the MR page stays approve; the agent menu is `A`.
	e.press("A")
	if e.m.agt == nil || e.m.agt.item == nil || e.m.agt.item.Ref != it.Ref {
		t.Fatalf("A on the MR page must open the item's agent menu: %+v", e.m.agt)
	}
	if !strings.Contains(e.view(), "20261111-100000-aaa") {
		t.Error("the menu lists the item's runs")
	}
	// A review-requested MR has no tracker item: no menu.
	e.m.agt = nil
	e.m.mr = &mrPage{}
	e.press("A")
	if e.m.agt != nil {
		t.Error("an MR without a work item has no agent menu")
	}
}

func TestAgentHomeKeyNeedsAnItem(t *testing.T) {
	m := model{g: unicodeGlyphs, nc: newNotifCenter(nil), w: 120, h: 40, page: pageHome}
	next, _ := m.key(keyPress("a"))
	if next.(model).agt != nil {
		t.Error("a with no row selected must not open anything")
	}
	_ = domain.WorkItem{}
}
