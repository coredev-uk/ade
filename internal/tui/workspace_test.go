package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/workspace"
)

type wsCall struct{ project, repo, worktree string }

// wsHarness is a model over a project on disk (a manifest snapshot and a
// baseline api tree) with workspace.Open replaced by a recorder.
type wsHarness struct {
	t     *testing.T
	m     model
	root  string
	calls []wsCall
	ret   error
	res   workspace.OpenResult
}

func newWsHarness(t *testing.T) *wsHarness {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "demo", "api", "main"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "name = \"demo\"\n[repos.api]\npath = \"g/api\"\ndefault_branch = \"main\"\napp = [\"api\"]\n"
	snap := "source = \"x\"\nmanifest = '''\n" + manifest + "'''\n"
	if err := os.WriteFile(filepath.Join(root, "demo", ".project.toml"), []byte(snap), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	a := &app.App{Paths: paths.Paths{CacheDir: t.TempDir()}, Config: &cfg, Root: root}
	t.Cleanup(func() { a.Close() })
	st, err := a.Cache()
	if err != nil {
		t.Fatal(err)
	}
	h := &wsHarness{t: t, root: root}
	h.m = model{app: a, ctx: context.Background(), cache: st, g: unicodeGlyphs, nc: newNotifCenter(notify.NewInbox(st)), w: 160, h: 40}
	next, _ := h.m.Update(resultsMsg{results: []forgesync.Result{sampleResult()}})
	h.m = next.(model)
	old := wsOpen
	wsOpen = func(_ context.Context, _ *app.App, p, r, wt string, _ ...workspace.OpenOption) (workspace.OpenResult, error) {
		h.calls = append(h.calls, wsCall{p, r, wt})
		return h.res, h.ret
	}
	t.Cleanup(func() { wsOpen = old })
	return h
}

// press sends w and runs the command it returns, feeding the answer back.
func (h *wsHarness) press() {
	h.t.Helper()
	next, cmd := h.m.Update(keyPress("w"))
	h.m = next.(model)
	if cmd == nil {
		return
	}
	next, _ = h.m.Update(cmd())
	h.m = next.(model)
}

func (h *wsHarness) selectRef(ref string) {
	h.t.Helper()
	for i, r := range h.m.homeRows {
		if r.ref() == ref {
			h.m.homeSel = i
			return
		}
	}
	h.t.Fatalf("no row %s", ref)
}

func (h *wsHarness) lastToast() events.Event {
	h.t.Helper()
	if len(h.m.nc.toasts) == 0 {
		h.t.Fatal("no pop-up")
	}
	return h.m.nc.toasts[len(h.m.nc.toasts)-1].ev
}

func TestWOpensTheRepoBaselineWhenTheItemHasNoWorktree(t *testing.T) {
	h := newWsHarness(t)
	h.res = workspace.OpenResult{Created: true}
	h.selectRef("g/tracker#3")
	h.press()
	want := wsCall{"demo", "api", filepath.Join(h.root, "demo", "api", "main")}
	if len(h.calls) != 1 || h.calls[0] != want {
		t.Fatalf("calls = %+v, want %+v", h.calls, want)
	}
	if e := h.lastToast(); e.Kind != "workspace.opened" || !strings.Contains(e.Title, "opened workspace api/main") {
		t.Fatalf("pop-up: %+v", e)
	}
}

func TestWWithoutASessionIsAHintNotAFailure(t *testing.T) {
	h := newWsHarness(t)
	h.ret = workspace.ErrNoSession
	h.selectRef("g/tracker#3")
	h.press()
	e := h.lastToast()
	if e.Kind != "workspace.hint" || e.Severity != events.Warn || !e.Transient {
		t.Fatalf("a missing session is a transient warning: %+v", e)
	}
	if !strings.Contains(e.Title, "no ade session") || !strings.Contains(e.Subject, "start ADE with `ade`") || !strings.Contains(e.Detail, "is not running") {
		t.Fatalf("hint text: %+v", e)
	}
}

func TestWFailureStaysUntilDismissed(t *testing.T) {
	h := newWsHarness(t)
	h.ret = context.DeadlineExceeded
	h.selectRef("g/tracker#3")
	h.press()
	e := h.lastToast()
	if e.Kind != "workspace.failed" || e.Severity != events.Error {
		t.Fatalf("%+v", e)
	}
	if !h.m.nc.toasts[0].expires.IsZero() {
		t.Fatal("a failure pop-up must not expire")
	}
}

func TestWPlainHostShowsItsHint(t *testing.T) {
	h := newWsHarness(t)
	h.res = workspace.OpenResult{Hint: "cd /x && ade ws up --no-zellij"}
	h.selectRef("g/tracker#3")
	h.press()
	if e := h.lastToast(); !strings.Contains(e.Title, "api/main: no tab on this host") || !strings.Contains(e.Subject, "command copied") {
		t.Fatalf("%+v", e)
	}
}

func TestWOnAnItemWithoutARepoSaysSo(t *testing.T) {
	h := newWsHarness(t)
	for i := range h.m.results[0].Items {
		h.m.results[0].Items[i].Repos = nil
	}
	h.m.rebuildHome("")
	h.selectRef("g/tracker#3")
	h.press()
	if len(h.calls) != 0 {
		t.Fatal("nothing to open")
	}
	if e := h.lastToast(); e.Severity != events.Error || !strings.Contains(e.Subject, "add an app label") {
		t.Fatalf("%+v", e)
	}
}

func TestWOnTheWorktreesPageOpensTheTreeUnderTheCursor(t *testing.T) {
	h := newWsHarness(t)
	h.res = workspace.OpenResult{Created: true}
	h.m.page = pageTrees
	tree := filepath.Join(h.root, "demo", "api", "main")
	h.m.rows = []row{{project: "demo", repo: project.RepoStatus{Name: "api"}, tree: project.TreeStatus{Name: "main", Path: tree}, base: true}}
	h.press()
	if len(h.calls) != 1 || h.calls[0] != (wsCall{"demo", "api", tree}) {
		t.Fatalf("calls = %+v", h.calls)
	}
}

func TestWIsIgnoredWhereAnotherPageOwnsTheKey(t *testing.T) {
	h := newWsHarness(t)
	h.m.page = pageBudget
	h.press()
	if len(h.calls) != 0 {
		t.Fatal("w on the budget page must not open a workspace")
	}
}

func TestBuildChipsGroupsInstancesUnderTheirItems(t *testing.T) {
	feature := "/p/demo/api/3-limit"
	main := "/p/demo/api/main"
	web := "/p/demo/web/12-x"
	trees := []row{
		{project: "demo", repo: project.RepoStatus{Name: "api"}, tree: project.TreeStatus{Name: "main", Path: main}, base: true},
		{project: "demo", repo: project.RepoStatus{Name: "api"}, tree: project.TreeStatus{Name: "3-limit", Path: feature, Branch: "3-limit"}},
		{project: "demo", repo: project.RepoStatus{Name: "web"}, tree: project.TreeStatus{Name: "12-x", Path: web, Branch: "12-x"}},
	}
	issue := func(p string, iid int) forge.Issue { return forge.Issue{Ref: forge.Ref{Project: p, IID: iid}} }
	item := domain.WorkItem{Ref: forge.Ref{Project: "g/tracker", IID: 7}, Children: []forge.Issue{issue("g/api", 3), issue("g/web", 12)}}
	other := domain.WorkItem{Ref: forge.Ref{Project: "g/tracker", IID: 8}, Children: []forge.Issue{issue("g/web", 99)}}
	items := []workspace.ItemRef{{Project: "demo", Item: item}, {Project: "demo", Item: other}}
	repoPath := func(_, repo string) string { return map[string]string{"api": "g/api", "web": "g/web"}[repo] }

	inst := []workspace.Row{
		{ID: "api.check@x", Node: "api.check", Kind: "oneshot", Holders: []string{web}},
		{ID: "api.dev@feature", Node: "api.dev", Kind: "service", Tree: feature, Port: 5031, State: workspace.StateReady, Alive: true, Holders: []string{web, web + "#task:web.dev"}},
		{ID: "web.dev@web", Node: "web.dev", Kind: "service", Tree: web, Port: 5044, State: workspace.StateStarting, Alive: true, Holders: []string{web}},
		{ID: "db@proj", Node: "services.db", Kind: "service", Tree: "/p/demo", Port: 5012, Alive: false, Holders: []string{web}},
		{ID: "api.dev@main", Node: "api.dev", Kind: "service", Tree: main, Port: 5099, State: workspace.StateReady, Alive: true, Holders: []string{"/p/elsewhere/unknown"}},
	}
	got := buildChips(inst, trees, items, repoPath)
	chips := got["g/tracker#7"]
	if len(chips) != 3 {
		t.Fatalf("item 7 chips: %+v (oneshots, unknown holders and duplicate holders add none)", chips)
	}
	if c := chips[0]; c.node != "api.dev" || c.port != 5031 || c.state != "ready" || !c.feature || c.shared {
		t.Errorf("api in the feature tree: %+v", c)
	}
	if c := chips[1]; c.node != "web.dev" || c.state != "starting" || c.feature {
		t.Errorf("web in its own tree is not a 'feature tree': %+v", c)
	}
	if c := chips[2]; c.node != "services.db" || c.state != "stopped" {
		t.Errorf("a dead process is stopped: %+v", c)
	}
	if len(got["g/tracker#8"]) != 0 {
		t.Errorf("another item's chips: %+v", got["g/tracker#8"])
	}

	// A baseline instance shared by two items shows under both, unlabelled.
	baseInst := []workspace.Row{{ID: "api.dev@main", Node: "api.dev", Kind: "service", Tree: main, Port: 5099, State: workspace.StateReady, Alive: true, Holders: []string{web, "/p/demo/web/99-y"}}}
	trees = append(trees, row{project: "demo", repo: project.RepoStatus{Name: "web"}, tree: project.TreeStatus{Name: "99-y", Path: "/p/demo/web/99-y", Branch: "99-y"}})
	shared := buildChips(baseInst, trees, items, repoPath)
	if len(shared["g/tracker#7"]) != 1 || len(shared["g/tracker#8"]) != 1 || shared["g/tracker#7"][0].feature ||
		!shared["g/tracker#7"][0].shared || !shared["g/tracker#8"][0].shared {
		t.Fatalf("shared baseline instance: %+v", shared)
	}
}

func TestServicesStripReadsWithoutColourInBothGlyphSets(t *testing.T) {
	chips := []svcChip{
		{node: "services.db", port: 5012, state: "ready"},
		{node: "api.dev", port: 5031, state: "ready", feature: true},
		{node: "web.dev", state: "starting"},
		{node: "worker.dev", state: "stopped"},
	}
	for name, g := range map[string]glyphs{"unicode": unicodeGlyphs, "ascii": asciiGlyphs} {
		m := model{g: g, ws: wsState{chips: map[string][]svcChip{"g/tracker#1": chips}}}
		lines := m.serviceLines("g/tracker#1", 200)
		out := ansi.Strip(strings.Join(lines, "\n"))
		// Attention first (stopped, starting), then ready, never on one line.
		want := "services  worker.dev " + g.Bad + " stopped " + g.Sep + " web.dev " + g.Ahead + " starting\n" +
			"          services.db " + g.Done + " 5012 " + g.Sep + " api.dev " + g.Done + " 5031 (feature tree)"
		if out != want {
			t.Errorf("%s strip:\n got %q\nwant %q", name, out, want)
		}
		if g == asciiGlyphs && !isASCII(out) {
			t.Errorf("ascii strip has non-ascii runes: %q", out)
		}
	}
	m := model{g: unicodeGlyphs, ws: wsState{chips: map[string][]svcChip{"g/tracker#1": chips}}}
	for _, l := range m.serviceLines("g/tracker#1", 40) {
		if ansi.StringWidth(l) > 40 {
			t.Errorf("line wider than the pane: %q", l)
		}
	}
	if m.serviceLines("g/tracker#2", 100) != nil {
		t.Error("an item whose workspaces run nothing gets no strip")
	}
}

func TestItemPreviewShowsTheStrip(t *testing.T) {
	h := newWsHarness(t)
	h.selectRef("g/tracker#3")
	h.m.ws.chips = map[string][]svcChip{"g/tracker#3": {{node: "api.dev", port: 5031, state: "ready"}}}
	h.m.w = 120 // two regions: at 140+ the services move to the rail
	out := ansi.Strip(h.m.homeDetail(60))
	if !strings.Contains(out, "services  api.dev ● 5031") {
		t.Fatalf("preview lacks the services strip:\n%s", out)
	}
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

func TestBuildChipsFailedMeansDiedWhileStarting(t *testing.T) {
	wt := "/p/demo/web/12-x"
	trees := []row{{project: "demo", repo: project.RepoStatus{Name: "web"}, tree: project.TreeStatus{Name: "12-x", Path: wt, Branch: "12-x"}}}
	item := domain.WorkItem{Ref: forge.Ref{Project: "g/tracker", IID: 7}, Children: []forge.Issue{{Ref: forge.Ref{Project: "g/web", IID: 12}}}}
	items := []workspace.ItemRef{{Project: "demo", Item: item}}
	inst := []workspace.Row{
		{ID: "a", Node: "worker.dev", Kind: "service", Tree: wt, State: workspace.StateStarting, Alive: false, Holders: []string{wt}},
		{ID: "b", Node: "cache.dev", Kind: "service", Tree: wt, State: workspace.StateReady, Alive: false, Holders: []string{wt}},
		{ID: "c", Node: "web.dev", Kind: "service", Tree: wt, State: workspace.StateBlocked, BlockedBy: "api.dev", Alive: true, Holders: []string{wt}},
	}
	got := buildChips(inst, trees, items, func(_, _ string) string { return "g/web" })["g/tracker#7"]
	if len(got) != 3 || got[0].state != "failed" || got[1].state != "stopped" || got[2].state != "blocked" || got[2].blocker != "api.dev" {
		t.Fatalf("%+v", got)
	}
}

func TestServicesStripWrapsBetweenInstancesAndNamesSharedAndFailed(t *testing.T) {
	chips := []svcChip{
		{node: "services.db", port: 5012, state: "ready"},
		{node: "api.dev", port: 5031, state: "ready", feature: true, shared: true},
		{node: "web.dev", port: 5044, state: "ready"},
		{node: "worker.dev", state: "starting"},
		{node: "queue.dev", state: "failed"},
		{node: "cache.dev", state: "stopped"},
		{node: "web2.dev", state: "blocked", blocker: "api.dev"},
	}
	for name, g := range map[string]glyphs{"unicode": unicodeGlyphs, "ascii": asciiGlyphs} {
		m := model{g: g, ws: wsState{chips: map[string][]svcChip{"x": chips}}}
		lines := m.serviceLines("x", 50)
		if len(lines) < 3 {
			t.Fatalf("%s: six instances at 50 cells must wrap: %q", name, lines)
		}
		joined := ansi.Strip(strings.Join(lines, "\n"))
		for _, want := range []string{"api.dev " + g.Done + " 5031 (feature tree, shared)", "worker.dev " + g.Ahead + " starting", "queue.dev " + g.Bad + " failed", "cache.dev " + g.Bad + " stopped", "web2.dev " + g.Warn + " blocked by api.dev"} {
			if !strings.Contains(joined, want) {
				t.Errorf("%s: chip %q was split or missing:\n%s", name, want, joined)
			}
		}
		for i, l := range lines {
			if ansi.StringWidth(l) > 50 {
				t.Errorf("%s: line %d is %d wide", name, i, ansi.StringWidth(l))
			}
			if i > 0 && !strings.HasPrefix(ansi.Strip(l), strings.Repeat(" ", len("services  "))) {
				t.Errorf("%s: continuation not aligned: %q", name, ansi.Strip(l))
			}
		}
	}
}

// A failure pop-up stays until dismissed, so its recovery text wraps instead of being cut.
func TestFailurePopupWrapsRecoveryText(t *testing.T) {
	e := events.New("workspace.failed", events.Error, "workspace not opened")
	e.Subject = "project demo-gl is not cloned: open /tmp/ade-demo/projects/demo-gl/.project.toml: no such file or directory"
	box := ansi.Strip(toastBox(unicodeGlyphs, toast{ev: e}, 44))
	for _, want := range []string{"project demo-gl is not", "no such file or directory", "esc dismiss"} {
		if !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(box, unicodeGlyphs.Border.Left, " ")), " "), want) {
			t.Errorf("recovery text lost %q:\n%s", want, box)
		}
	}
	for i, l := range strings.Split(box, "\n") {
		if ansi.StringWidth(l) > 44 {
			t.Errorf("line %d overflows: %q", i, l)
		}
	}
	// A pop-up that expires wraps too: the end of a title is often the instruction.
	e.Severity = events.Info
	e.Title = "workspace api/1-rate-limit-the-export-endpoint is not in a tab here"
	e.Subject = "restart it with ade ws up --no-zellij"
	got := strings.Join(strings.Fields(strings.ReplaceAll(ansi.Strip(toastBox(unicodeGlyphs, toast{ev: e, expires: toastNow()}, 44)), unicodeGlyphs.Border.Left, " ")), " ")
	if !strings.Contains(got, "ade ws up --no-zellij") || strings.Contains(got, "esc dismiss") {
		t.Errorf("an info pop-up keeps its command whole and does not ask to be dismissed: %s", got)
	}
}

func toastNow() time.Time { return time.Now().Add(time.Minute) }

func TestFailurePopupKeepsALongTaskFailureReadable(t *testing.T) {
	e := events.New("task.failed", events.Error,
		"api.dev failed: process exited before ready (http http://127.0.0.1:37007/healthz), last log line: listen tcp 127.0.0.1:37007: bind: address already in use")
	box := strings.Join(strings.Fields(strings.ReplaceAll(ansi.Strip(toastBox(unicodeGlyphs, toast{ev: e}, 44)), unicodeGlyphs.Border.Left, " ")), " ")
	if !strings.Contains(box, "address already in use") {
		t.Fatalf("the recovery hint at the end of the title was cut: %s", box)
	}
}

func TestWarningPopupWrapsItsReason(t *testing.T) {
	e := events.New("task.blocked", events.Warn,
		"web.dev not started: blocked: api.dev not ready (timeout 60s, last log line: listen tcp 127.0.0.1:37007: address already in use)")
	box := toast{ev: e, expires: toastNow()}
	got := strings.Join(strings.Fields(strings.ReplaceAll(ansi.Strip(toastBox(unicodeGlyphs, box, 44)), unicodeGlyphs.Border.Left, " ")), " ")
	if !strings.Contains(got, "address already in use)") || strings.Contains(got, "esc dismiss") {
		t.Fatalf("a warning wraps and does not ask to be dismissed: %s", got)
	}
}

func TestUnclonedProjectFailureSaysHowToRecoverAndKeepsTheCauseInTheDetail(t *testing.T) {
	h := newWsHarness(t)
	if err := os.Remove(filepath.Join(h.root, "demo", ".project.toml")); err != nil {
		t.Fatal(err)
	}
	h.selectRef("g/tracker#3")
	h.press()
	e := h.lastToast()
	if e.Kind != "workspace.failed" || e.Subject != "project demo is not cloned\nrun ade project add" {
		t.Fatalf("%+v", e)
	}
	if !strings.Contains(e.Detail, ".project.toml") || strings.Contains(e.Subject, ".project.toml") {
		t.Fatalf("the path belongs in the detail only: %+v", e)
	}
}

func TestBlockedTaskPopupStaysUntilDismissed(t *testing.T) {
	h := newWsHarness(t)
	h.m.nc.push(events.New("task.blocked", events.Warn, "web.dev waits for api.dev"))
	h.m.nc.push(events.New("task.exited", events.Warn, "api.dev exited"))
	h.m.nc.push(events.New("task.ready", events.Success, "api.dev ready on :1"))
	if !h.m.nc.toasts[0].expires.IsZero() || !h.m.nc.toasts[1].expires.IsZero() {
		t.Fatal("a blocked or exited task names what to do next; its pop-up must not expire")
	}
	if h.m.nc.toasts[2].expires.IsZero() {
		t.Fatal("a success pop-up expires")
	}
}

func TestPlainHostHintIsCopiedNotShownAsALongCommand(t *testing.T) {
	h := newWsHarness(t)
	cmd := h.m.onWsOpened(wsOpenedMsg{label: "api/main", res: workspace.OpenResult{Hint: "cd '/very/long/path' && ade ws up --no-zellij"}})
	if cmd == nil {
		t.Fatal("the command must be copied to the clipboard")
	}
	if e := h.lastToast(); strings.Contains(e.Title+e.Subject, "/very/long/path") {
		t.Fatalf("the pop-up must not show the long command: %+v", e)
	}
	if cmd := h.m.onWsOpened(wsOpenedMsg{label: "api/main", res: workspace.OpenResult{Created: true}}); cmd != nil {
		t.Fatal("nothing to copy when the tab opened")
	}
}
