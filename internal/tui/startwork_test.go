package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	hostedit "github.com/coredev-uk/ade/internal/editor"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/startwork/swtest"
	"github.com/coredev-uk/ade/internal/workspace"
)

// The Start work dialog is driven the way the user drives it: key presses into
// the model, every command it returns run to completion against the fake
// GitLab and real git remotes, its answers fed back in.

const swTemplate = "## What\n\n/label ~\"type::story\"\n/assign_reviewer @reviewer-rae\n"

type swEnvT struct {
	t      *testing.T
	srv    *fake.Server
	forge  *swtest.Mirror
	m      model
	app    *app.App
	remote map[string]*swtest.Remote
	root   string
}

func newSWEnv(t *testing.T) *swEnvT { return newSWEnvWith(t, func(s string) string { return s }) }

// newSWEnvWith is newSWEnv with the sandbox manifest text edited first.
func newSWEnvWith(t *testing.T, edit func(string) string) *swEnvT {
	t.Helper()
	swtest.GitEnv(t)
	dir := t.TempDir()
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	mirror := swtest.NewMirror(gitlab.New(srv.URL, fake.Token))

	manifest, err := os.ReadFile("../../scripts/sandbox/manifests/gitlab.ade.toml")
	if err != nil {
		t.Fatal(err)
	}
	mpath := filepath.Join(dir, "demo.ade.toml")
	if err := os.WriteFile(mpath, []byte(edit(string(manifest))), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Forges["sandbox-gitlab"] = config.Forge{Kind: "gitlab"}
	cfg.Projects = []config.ProjectRef{{Source: mpath}}
	al, err := audit.Open(filepath.Join(dir, "records"), "user")
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{Paths: paths.Paths{CacheDir: t.TempDir()}, Config: &cfg, Root: filepath.Join(dir, "root"), Audit: al, Bus: events.NewBus()}
	t.Cleanup(func() { a.Close() })
	st, err := a.Cache()
	if err != nil {
		t.Fatal(err)
	}

	e := &swEnvT{t: t, srv: srv, forge: mirror, app: a, remote: map[string]*swtest.Remote{}, root: a.Root}
	for name, proj := range map[string]string{"api": grp + "/api", "web": grp + "/web"} {
		r := swtest.NewRemote(t, dir, name)
		e.remote[name] = r
		mirror.Remotes[proj] = r
		swtest.Clone(t, a.Root, "demo-gl", name, r)
	}
	srv.AddFile(grp+"/api", startwork.TemplateDir+"/Default.md", swTemplate)

	// No zellij here: the workspace host is not part of this surface.
	old := swWorkspace
	swWorkspace = func(*app.App) func(context.Context, startwork.WorkspaceRequest) error {
		return func(context.Context, startwork.WorkspaceRequest) error { return nil }
	}
	t.Cleanup(func() { swWorkspace = old })

	m := model{
		ctx: context.Background(), app: a, cache: st, g: unicodeGlyphs, w: 140, h: 48,
		nc: newNotifCenter(notify.NewInbox(st)),
		sync: &forgesync.Service{App: a, Now: time.Now, Open: func(context.Context, string, config.Forge) (forge.Forge, error) {
			return mirror, nil
		}},
	}
	res := forgesync.Result{Project: "demo-gl", Tracker: grp + "/tracker", Items: []domain.WorkItem{
		e.item(3, "Retry failed webhook deliveries", "api"),
		e.item(4, "Remove the legacy CSV export path", "api"),
		e.item(8, "Scheduled exports", "api", "web"),
	}}
	m.setResults([]forgesync.Result{res})
	m.homeLoaded = true
	e.m = m
	return e
}

func (e *swEnvT) item(iid int, title string, repos ...string) domain.WorkItem {
	ref := forge.Ref{Project: grp + "/tracker", IID: iid, Kind: forge.KindIssue}
	return domain.WorkItem{
		Project: "demo-gl", Ref: ref, Issue: forge.Issue{Ref: ref, Title: title}, Stage: domain.StageAssigned,
		Court: domain.CourtYou, Assigned: true, Repos: repos, Apps: repos,
	}
}

func (e *swEnvT) settle(cmd tea.Cmd) {
	e.t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 500 {
			e.t.Fatal("commands never settled")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		done := make(chan tea.Msg, 1)
		go func() { done <- c() }()
		var msg tea.Msg
		select {
		case msg = <-done:
		case <-time.After(20 * time.Second):
			e.t.Fatal("a command did not return")
		}
		switch msg := msg.(type) {
		case nil, tickMsg, mrSpinMsg:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case resultsMsg, statusMsg, inboxMsg, updateDoneMsg:
			// The refresh after a start reads the cache and the forge; the
			// dialog does not depend on it.
		default:
			next, c2 := e.m.Update(msg)
			e.m = next.(model)
			queue = append(queue, c2)
		}
	}
}

func (e *swEnvT) press(keys ...string) {
	e.t.Helper()
	for _, k := range keys {
		next, cmd := e.m.Update(keyMsg(k))
		e.m = next.(model)
		e.settle(cmd)
	}
}

func (e *swEnvT) selectRow(ref string) {
	e.t.Helper()
	for i, r := range e.m.homeRows {
		if r.ref() == ref {
			e.m.homeSel = i
			return
		}
	}
	e.t.Fatalf("no Home row for %s", ref)
}

func (e *swEnvT) screen() string { return ansi.Strip(e.m.render()) }

func (e *swEnvT) want(s ...string) {
	e.t.Helper()
	out := e.screen()
	for _, w := range s {
		if !strings.Contains(out, w) {
			e.t.Errorf("screen lacks %q:\n%s", w, out)
		}
	}
}

func (e *swEnvT) wantNot(s ...string) {
	e.t.Helper()
	out := e.screen()
	for _, w := range s {
		if strings.Contains(out, w) {
			e.t.Errorf("screen has %q:\n%s", w, out)
		}
	}
}

func (e *swEnvT) writes() int {
	n := 0
	for _, r := range e.srv.Requests() {
		if r.Method != "GET" && r.Operation != "query" {
			n++
		}
	}
	return n
}

func trk(iid int) forge.Ref {
	return forge.Ref{Project: grp + "/tracker", IID: iid, Kind: forge.KindIssue}
}

func TestStartDialogDraftFlow(t *testing.T) {
	e := newSWEnv(t)
	e.selectRow(trk(3).String())
	e.press("s")
	if e.m.sw == nil || e.m.sw.stage != swChoose {
		t.Fatalf("dialog stage after s = %+v", e.m.sw)
	}
	// Choose: the app label preselected api; Draft MR is the default mode.
	e.want("Start work · "+trk(3).String(), "Retry failed webhook deliveries", "Repos", "app::api routes here",
		"[x] api", "[ ] web", "[ ] tracker", "work lives in the tracker project",
		"Draft MR", "Branch only", "Browse", "Child issue", "auto")
	if got := e.m.sw.chosen(); !slices.Equal(got, []string{"api"}) {
		t.Errorf("preselected repos = %v", got)
	}
	e.wantNot("requires api") // the hint appears only when a chosen repo needs an unchosen one
	e.press("enter")
	if e.m.sw.stage != swConfirm {
		t.Fatalf("stage after enter = %v (warn %q, err %v)", e.m.sw.stage, e.m.sw.warn, e.m.sw.err)
	}

	// Confirm: every write as a station line, the body, the template's quick actions.
	e.want("Confirm", "5 forge writes", "api", "ade-demo-env/api",
		"+ forge", "create child issue in ade-demo-env/api", "set workflow::in progress on "+trk(3).String(),
		"create branch "+startwork.ChildIIDPlaceholder+"-retry-failed-webhook-deliveries from main", "open draft MR",
		"MR · api", "template Default", "Closes "+startwork.ChildPlaceholder, "Part of "+trk(3).String(),
		"Quick actions", "keep  add labels", "keep  request review from @reviewer-rae", "/assign_reviewer @reviewer-rae")
	if w := e.writes(); w != 0 {
		t.Fatalf("the confirm screen made %d forge writes", w)
	}

	// Strip the reviewer request only.
	e.press("j", "x")
	e.want("strip request review from @reviewer-rae", "keep  add labels")
	e.press("enter")
	if e.m.sw.stage != swDone {
		t.Fatalf("stage after confirm = %v (err %v)", e.m.sw.stage, e.m.sw.err)
	}
	e.want("created", "child issue", "draft MR")

	res := e.m.sw.result
	api := res.Targets[0]
	if api.Child == nil || api.MR == nil || api.Worktree == "" {
		t.Fatalf("target = %+v", api)
	}
	if rev, _ := e.srv.MRReviewers(*api.MR); len(rev) != 0 {
		t.Errorf("stripped quick action still requested review from %v", rev)
	}
	if labels, _ := e.srv.MRLabels(*api.MR); !slices.Contains(labels, "type::story") {
		t.Errorf("kept /label did not run: %v", labels)
	}
	if _, err := os.Stat(api.Worktree); err != nil {
		t.Errorf("worktree: %v", err)
	}

	e.press("enter")
	if e.m.sw != nil {
		t.Error("enter did not close the finished dialog")
	}
	// Starting again finds everything: the confirm screen says so and writes nothing.
	before := e.writes()
	e.selectRow(trk(3).String())
	e.press("s", "enter")
	e.want("nothing to create: everything exists", "exists")
	e.wantNot("+ forge")
	e.press("esc", "esc")
	if e.writes() != before {
		t.Errorf("re-opening wrote to the forge")
	}
}

func TestStartDialogAskOffersTheLabelMove(t *testing.T) {
	e := newSWEnvWith(t, func(s string) string {
		out := strings.Replace(s, `sync    = "auto"`, `sync    = "ask"`, 1)
		if out == s {
			t.Fatal("sandbox manifest has no sync = \"auto\" line to edit")
		}
		return out
	})
	e.selectRow(trk(3).String())
	e.press("s", "enter")
	e.want("4 forge writes", "labels.workflow.sync = ask: press l to move the workflow label", "[ ]")
	e.wantNot("set workflow::in progress")
	e.press("l")
	e.want("5 forge writes", "set workflow::in progress on "+trk(3).String(), "[x]")
}

func TestStartDialogAskResultNamesTheDialogKey(t *testing.T) {
	e := newSWEnvWith(t, func(s string) string { return strings.Replace(s, `sync    = "auto"`, `sync    = "ask"`, 1) })
	e.selectRow(trk(3).String())
	e.press("s", "enter", "enter")
	if e.m.sw.stage != swDone {
		t.Fatalf("stage = %v, err %v", e.m.sw.stage, e.m.sw.err)
	}
	e.want("press l in the start") // the note wraps in the dialog
	e.wantNot("--move-label")
}

func TestStartDialogBrowseWritesNothing(t *testing.T) {
	e := newSWEnv(t)
	e.selectRow(trk(3).String())
	e.press("s", "m", "m") // Draft MR → Branch only → Browse
	e.want("Browse")
	if e.m.sw.ch.Mode != startwork.ModeBrowse {
		t.Fatalf("mode = %v", e.m.sw.ch.Mode)
	}
	e.press("enter")
	e.want("no forge writes")
	e.wantNot("create child", "Quick actions")
	e.press("enter")
	if e.m.sw.stage != swDone {
		t.Fatalf("stage = %v, err %v", e.m.sw.stage, e.m.sw.err)
	}
	if got := e.m.sw.result.Targets[0].Worktree; !strings.HasSuffix(got, "/api/main") {
		t.Errorf("browse worktree = %s", got)
	}
	if w := e.writes(); w != 0 {
		t.Errorf("browse wrote %d times to the forge", w)
	}
}

func TestStartDialogCrossRepoOffersRequiredRepo(t *testing.T) {
	e := newSWEnv(t)
	e.selectRow(trk(8).String())
	e.press("s")
	if got := e.m.sw.chosen(); !slices.Equal(got, []string{"api", "web"}) {
		t.Fatalf("two app labels must preselect two repos, got %v", got)
	}
	// Unselect api: web requires it, and the dialog says so.
	e.press(" ")
	e.want("[ ] api", "web requires api: also a child here?")
	e.press(" ") // select api again
	e.press("enter")
	e.want("◎ "+trk(8).String(), "┣━● api", "┗━● web", "create child issue in ade-demo-env/api", "create child issue in ade-demo-env/web")
	e.press("p")
	e.want("MR · web", "no template in ade-demo-env/web")
}

func TestStartDialogNothingChosenAndEscape(t *testing.T) {
	e := newSWEnv(t)
	e.selectRow(trk(3).String())
	e.press("s", " ", "enter") // deselect api, continue
	e.want("choose at least one repo")
	if e.m.sw.stage != swChoose {
		t.Errorf("stage = %v", e.m.sw.stage)
	}
	e.press("esc")
	if e.m.sw != nil {
		t.Error("esc did not close")
	}
	// Keys belong to the dialog while it is open: q must not quit the dashboard.
	e.press("s")
	next, cmd := e.m.Update(keyMsg("q"))
	e.m = next.(model)
	if cmd != nil || e.m.sw != nil {
		t.Errorf("q in the dialog: cmd=%v sw=%v", cmd, e.m.sw)
	}
}

func TestStartDialogShowsFailureAndResumes(t *testing.T) {
	e := newSWEnv(t)
	e.selectRow(trk(3).String())
	e.forge.FailNext("CreateBranch", errTest)
	e.press("s", "enter", "enter")
	if e.m.sw.stage != swDone || e.m.sw.err == nil {
		t.Fatalf("stage %v err %v, want a finished dialog carrying the failure", e.m.sw.stage, e.m.sw.err)
	}
	e.want("failed", "boom", "s resumes where it stopped")
	e.press("enter")

	// Resume: the child is reused, only what is missing is created.
	e.press("s", "enter")
	e.want("exists", "create branch")
	e.press("enter")
	if e.m.sw.err != nil || e.m.sw.result.Targets[0].MR == nil {
		t.Fatalf("resume: err %v, result %+v", e.m.sw.err, e.m.sw.result)
	}
	if n := countChildren(t, e, 3); n != 1 {
		t.Errorf("%d children after resume, want 1", n)
	}
}

func TestStartDialogResolveError(t *testing.T) {
	e := newSWEnv(t)
	e.selectRow(trk(3).String())
	e.m.sync.Open = func(context.Context, string, config.Forge) (forge.Forge, error) { return nil, errTest }
	e.press("s")
	if e.m.sw.stage != swFailed {
		t.Fatalf("stage = %v", e.m.sw.stage)
	}
	e.want("boom")
	e.press("esc")
	if e.m.sw != nil {
		t.Error("esc did not close the failed dialog")
	}
}

// `M` creates the MR for work started as Branch only, from the template.
func TestCreateMRKey(t *testing.T) {
	e := newSWEnv(t)
	e.selectRow(trk(4).String())

	// Nothing started yet: M says so instead of starting work.
	e.press("M")
	if e.m.sw.stage != swFailed {
		t.Fatalf("M before any branch: stage %v", e.m.sw.stage)
	}
	e.want("no repo has a branch without its MR")
	e.press("esc")

	// Branch only through the dialog, then M.
	e.press("s", "m", "enter") // Branch only
	e.want("create branch")
	e.wantNot("open draft MR")
	e.press("enter")
	if e.m.sw.stage != swDone || e.m.sw.result.Targets[0].MR != nil {
		t.Fatalf("branch only: stage %v, %+v", e.m.sw.stage, e.m.sw.result)
	}
	e.press("enter")

	before := e.writes()
	e.press("M")
	if e.m.sw.stage != swConfirm {
		t.Fatalf("M after branch only: stage %v, err %v", e.m.sw.stage, e.m.sw.err)
	}
	e.want("Create MR · "+trk(4).String(), "open draft MR", "template Default", "Closes #", "Quick actions")
	e.wantNot("create child", "create branch")
	if e.writes() != before {
		t.Error("the M confirm screen wrote to the forge")
	}
	e.press("enter")
	api := e.m.sw.result.Targets[0]
	if api.MR == nil {
		t.Fatalf("no MR: %+v err %v", e.m.sw.result, e.m.sw.err)
	}
	if got := e.writes() - before; got != 1 {
		t.Errorf("M made %d writes, want exactly the MR", got)
	}
	if labels, _ := e.srv.MRLabels(*api.MR); !slices.Contains(labels, "type::story") {
		t.Errorf("MR labels = %v: the template was not used", labels)
	}
	e.press("enter")

	// And again: the MR exists, so M has nothing to do.
	e.press("M")
	e.want("no repo has a branch without its MR")
}

func TestEditedBodyIsSent(t *testing.T) {
	e := newSWEnv(t)
	old := swEditorCmd
	edited := filepath.Join(t.TempDir(), "edited.md")
	if err := os.WriteFile(edited, []byte("Closes "+startwork.ChildPlaceholder+"\n\nwritten by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	swEditorCmd = func(_ hostedit.Editor, path string) *exec.Cmd {
		return exec.Command("cp", edited, path)
	}
	t.Cleanup(func() { swEditorCmd = old })
	e.selectRow(trk(3).String())
	e.press("s", "enter")

	// ExecProcess needs a running program: run the editor and feed its answer in.
	e.runEdit()
	e.want("edited by hand", "written by hand")
	e.press("x")
	e.want("the body was edited by hand") // quick action toggles do not apply to an edited body
	e.press("enter")
	api := e.m.sw.result.Targets[0]
	mr, err := e.forge.MR(context.Background(), *api.MR)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mr.Description, "written by hand") || strings.Contains(mr.Description, "## What") {
		t.Errorf("MR description = %q", mr.Description)
	}
	if labels, _ := e.srv.MRLabels(*api.MR); len(labels) != 0 {
		t.Errorf("template quick actions ran though the body was replaced: %v", labels)
	}
}

func TestDialogGlyphSetsAndNarrow(t *testing.T) {
	unicode := "●○◉━┣┗┃✓▲✕⟳▸"
	for _, tc := range []struct {
		name   string
		g      glyphs
		w, h   int
		unicod bool
	}{
		{"unicode wide", unicodeGlyphs, 140, 48, true},
		{"ascii wide", asciiGlyphs, 140, 48, false},
		{"unicode narrow", unicodeGlyphs, 80, 40, true},
		{"ascii narrow", asciiGlyphs, 80, 40, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSWEnv(t)
			e.m.g, e.m.w, e.m.h = tc.g, tc.w, tc.h
			e.selectRow(trk(3).String())
			e.press("s")
			choose := e.screen()
			e.press("enter")
			confirm := e.screen()
			for name, out := range map[string]string{"choose": choose, "confirm": confirm} {
				if !strings.Contains(out, "Start work") {
					t.Errorf("%s: no dialog:\n%s", name, out)
				}
				if !tc.unicod && strings.ContainsAny(out, unicode) {
					t.Errorf("%s: unicode glyphs in the ascii set:\n%s", name, out)
				}
				for i, ln := range strings.Split(out, "\n") {
					if w := ansi.StringWidth(ln); w > tc.w {
						t.Errorf("%s: line %d is %d cells wide in a %d-wide terminal", name, i, w, tc.w)
					}
				}
			}
			// Glyph and word for every state: the words alone carry the meaning.
			for _, word := range []string{"keep", "forge", "Draft MR"} {
				if !strings.Contains(confirm+choose, word) {
					t.Errorf("no %q word on screen", word)
				}
			}
		})
	}
}

// The dialog is a blank field replacing the body at a fixed top row, whatever
// the stage: no dashboard row or preview shows around it and it never jumps.
func TestStartDialogIsABlankFieldAtAFixedRow(t *testing.T) {
	e := newSWEnv(t)
	e.m.w, e.m.h = 140, 48
	e.selectRow(trk(3).String())
	top := func(name string) {
		t.Helper()
		lines := strings.Split(e.screen(), "\n")
		row := slices.IndexFunc(lines, func(ln string) bool { return strings.Contains(ln, e.m.g.Border.TopLeft) })
		if row != 1 {
			t.Errorf("%s: dialog top at row %d, want 1:\n%s", name, row, strings.Join(lines, "\n"))
		}
		for i, ln := range lines {
			if i == 0 || i == len(lines)-1 {
				continue // header and footer
			}
			for _, home := range []string{"Scheduled exports", "My work", "unweighted"} {
				if strings.Contains(ln, home) {
					t.Errorf("%s: the dashboard shows through (%q on row %d):\n%s", name, home, i, strings.Join(lines, "\n"))
				}
			}
			if w := ansi.StringWidth(strings.TrimRight(ln, " ")); w > 120 { // 100 cols centred in 140
				t.Errorf("%s: row %d spills past the 100-column measure (%d cells)", name, i, w)
			}
		}
	}
	e.press("s")
	top("choose")
	e.press("enter")
	top("confirm")
	e.press("enter")
	top("done")
	e.press("enter")
	e.selectRow(trk(4).String())
	e.press("M")
	top("nothing to create")
}

// A narrow confirm shows what will be sent: the body clipped with an ellipsis,
// folded only when the terminal is too short for two lines of it.
func TestNarrowConfirmClipsTheBody(t *testing.T) {
	for _, tc := range []struct {
		h            int
		wantBody     bool
		wantClip     bool
		wantFoldOnly bool
	}{
		{h: 26, wantBody: true},
		{h: 24, wantBody: true},
		{h: 20, wantBody: true, wantClip: true},
		{h: 18, wantFoldOnly: true, wantClip: true},
	} {
		e := newSWEnv(t)
		e.m.w, e.m.h = 84, tc.h
		e.selectRow(trk(3).String())
		e.press("s", "enter")
		out := e.screen()
		if got := strings.Contains(out, "Closes "+startwork.ChildPlaceholder); got != tc.wantBody {
			t.Errorf("h=%d: body visible = %v, want %v:\n%s", tc.h, got, tc.wantBody, out)
		}
		if got := strings.Contains(out, "… ") && strings.Contains(out, "more line"); got != tc.wantClip {
			t.Errorf("h=%d: ellipsis for a clipped body = %v, want %v:\n%s", tc.h, got, tc.wantClip, out)
		}
		if tc.wantBody && !tc.wantClip && !strings.Contains(out, "Part of "+trk(3).String()) {
			t.Errorf("h=%d: the body is clipped before its second paragraph:\n%s", tc.h, out)
		}
		if tc.wantFoldOnly && strings.Contains(out, "Part of") {
			t.Errorf("h=%d: a too-short terminal still shows body lines:\n%s", tc.h, out)
		}
		if lines := strings.Count(out, "\n") + 1; lines > tc.h {
			t.Errorf("h=%d: the screen is %d rows", tc.h, lines)
		}
		e.want("keep", "esc back")
	}
}

// A recovery command in a done-state warning is wrapped, never cut, so the path
// to type is readable.
func TestDoneWarningWrapsTheRecoveryCommand(t *testing.T) {
	e := newSWEnv(t)
	swWorkspace = func(*app.App) func(context.Context, startwork.WorkspaceRequest) error {
		return func(_ context.Context, r startwork.WorkspaceRequest) error {
			return errors.New("cd '" + r.Worktree + "' && ade ws up --no-zellij")
		}
	}
	e.m.w, e.m.h = 84, 40
	e.selectRow(trk(3).String())
	e.press("s", "enter", "enter")
	if e.m.sw == nil || e.m.sw.stage != swDone {
		t.Fatalf("stage = %+v", e.m.sw)
	}
	out := e.screen()
	wt := e.m.sw.result.Targets[0].Worktree
	// Every path component survives on the screen, across however many lines.
	flat := strings.NewReplacer("\n", "", " ", "", e.m.g.Border.Left, "").Replace(out)
	if !strings.Contains(flat, strings.ReplaceAll(wt, " ", "")) || !strings.Contains(out, "workspace not opened:") || !strings.Contains(flat, "adewsup--no-zellij") {
		t.Errorf("the recovery command is cut (worktree %s):\n%s", wt, out)
	}
}

// A closed dialog's late answers are ignored, not applied to a new dialog.
func TestStaleDialogMessagesAreDropped(t *testing.T) {
	e := newSWEnv(t)
	e.selectRow(trk(3).String())
	e.press("s")
	old := e.m.sw
	e.press("esc")
	e.press("s")
	cur := e.m.sw
	next, _ := e.m.Update(swResolvedMsg{d: old, err: errTest})
	e.m = next.(model)
	if e.m.sw != cur || cur.stage != swChoose {
		t.Errorf("stale message changed the open dialog: %+v", e.m.sw)
	}
}

// Only work items open the dialog; review rows and other pages do not.
func TestStartKeyNeedsAWorkItemOnHome(t *testing.T) {
	e := newSWEnv(t)
	e.m.page = pageTrees
	e.press("s")
	if e.m.sw != nil {
		t.Error("s on the Worktrees page opened the dialog")
	}
	e.m.page = pageHome
	e.m.homeRows = nil
	e.press("s")
	if e.m.sw != nil {
		t.Error("s without a Home row opened the dialog")
	}
}

func countChildren(t *testing.T, e *swEnvT, tracker int) int {
	t.Helper()
	issues, err := e.forge.ListIssues(context.Background(), forge.IssueQuery{Project: grp + "/api", State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, is := range issues {
		if strings.HasPrefix(is.Description, "Parent: "+trk(tracker).String()) {
			n++
		}
	}
	return n
}

type testErr string

func (e testErr) Error() string { return string(e) }

const errTest = testErr("boom")

// runEdit runs the editor of an open edit exactly as tea.ExecProcess would, and
// feeds the file back as the dialog's answer.
func (e *swEnvT) runEdit() {
	e.t.Helper()
	d := e.m.sw
	tp := d.plan.Targets[d.preview]
	path := filepath.Join(e.t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte(tp.MRBody(forge.Ref{}, d.plan.Item)), 0o644); err != nil {
		e.t.Fatal(err)
	}
	if out, err := swEditorCmd(hostedit.Editor{}, path).CombinedOutput(); err != nil {
		e.t.Fatalf("editor: %v\n%s", err, out)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatal(err)
	}
	next, _ := e.m.Update(swEditedMsg{d: d, body: string(b)})
	e.m = next.(model)
}

// Two repos on a 24-row terminal: the step lists collapse to a line per repo so
// the keys that confirm and the last quick action stay on screen.
func TestNarrowCrossRepoConfirmKeepsItsKeys(t *testing.T) {
	e := newSWEnv(t)
	e.m.w, e.m.h = 84, 24
	e.selectRow(trk(8).String())
	e.press("s", "enter")
	out := e.screen()
	e.want("◎ "+trk(8).String(), "┣━● api", "┗━● web", "+ forge child issue, link, branch, draft MR", "Quick actions",
		"keep  add labels", "keep  request review", "enter confirm", "x keep/strip", "p next repo")
	if !strings.Contains(out, e.m.g.Border.BottomLeft) {
		t.Errorf("the dialog's bottom border is cut off:\n%s", out)
	}
	if lines := strings.Count(out, "\n") + 1; lines > 24 {
		t.Errorf("the screen is %d rows in a 24-row terminal:\n%s", lines, out)
	}
}

// The guide counts exactly the `+ local` rows the list prints, in every mode.
func TestConfirmGuideCountsEveryLocalRow(t *testing.T) {
	for _, keys := range [][]string{{"s", "enter"}, {"s", "m", "m", "enter"}} { // Draft MR, Browse
		e := newSWEnv(t)
		e.m.w, e.m.h = 140, 60
		e.selectRow(trk(3).String())
		e.press(keys...)
		out := e.screen()
		rows := strings.Count(out, "+ local")
		want := "· " + countWord(rows, "local step")
		if !strings.Contains(out, want) {
			t.Errorf("keys %v: %d `+ local` rows but the guide lacks %q:\n%s", keys, rows, want, out)
		}
	}
}

// A narrow terminal spends the warning's spacer before the gaps that hold the
// section rules apart.
func TestNarrowChooseKeepsGapsAboveRules(t *testing.T) {
	e := newSWEnv(t)
	e.m.w, e.m.h = 84, 26 // the Main pane row made the dialog two rows taller
	e.selectRow(trk(3).String())
	e.press("s", " ", "enter") // deselect api, continue: the warning appears
	e.want("choose at least one repo")
	lines := strings.Split(e.screen(), "\n")
	rules := 0
	for i, ln := range lines {
		for _, rule := range []string{"What to create ─", "Child issue ─", "Repos ─"} {
			if !strings.Contains(ln, rule) {
				continue
			}
			rules++
			if prev := strings.Trim(lines[i-1], e.m.g.Border.Left+" "); prev == "" {
				continue
			}
			t.Errorf("no blank row above the %q rule:\n%s", rule, strings.Join(lines, "\n"))
		}
	}
	if rules != 3 {
		t.Errorf("found %d section rules, want 3:\n%s", rules, strings.Join(lines, "\n"))
	}
}

// The main pane starts on the configured default, `a` cycles editor → omp →
// shell, and the choice reaches the workspace request.
func TestStartDialogMainPaneToggleReachesTheWorkspace(t *testing.T) {
	e := newSWEnv(t)
	e.m.app.Config.Host.Layout = workspace.LayoutAgent // the user's default: omp
	var got []startwork.WorkspaceRequest
	swWorkspace = func(*app.App) func(context.Context, startwork.WorkspaceRequest) error {
		return func(_ context.Context, r startwork.WorkspaceRequest) error {
			got = append(got, r)
			return nil
		}
	}
	e.m.w, e.m.h = 140, 60
	e.selectRow(trk(3).String())
	e.press("s")
	e.want("Main pane")
	if e.m.sw.ch.Layout != workspace.LayoutAgent {
		t.Fatalf("host.layout is the default, got %q", e.m.sw.ch.Layout)
	}
	e.press("a") // omp → shell
	if e.m.sw.ch.Layout != workspace.LayoutManual {
		t.Fatalf("a cycles to the shell, got %q", e.m.sw.ch.Layout)
	}
	e.press("a", "a") // shell → editor → omp
	e.press("a")      // omp → shell
	e.press("a")      // shell → editor
	if e.m.sw.ch.Layout != workspace.LayoutCode {
		t.Fatalf("got %q", e.m.sw.ch.Layout)
	}
	out := e.screen()
	if !strings.Contains(out, "● editor") || !strings.Contains(out, "○ omp") {
		t.Errorf("the chosen pane is marked:\n%s", out)
	}
	e.press("enter", "enter") // plan, then create
	if len(got) != 1 || got[0].Layout != workspace.LayoutCode {
		t.Fatalf("workspace request = %+v", got)
	}
	if got[0].Item.Ref.IID != 3 || got[0].Forge == "" {
		t.Errorf("the request carries the item for the overview and skill: %+v", got[0])
	}
}

// E edits the MR body in the dialog's own editor (bubbles textarea): enter is
// a newline there, ctrl+s keeps the text and it is what the MR is created with;
// esc throws the change away. Paste lands in it too.
func TestInlineBodyEditorIsKeptAndSent(t *testing.T) {
	e := newSWEnv(t)
	e.selectRow(trk(3).String())
	e.press("s", "enter")
	e.want("e/E edit body")

	e.press("E")
	e.want("MR body · api", "editing", "Closes "+startwork.ChildPlaceholder, "ctrl+s keep", "enter newline")
	e.press("esc")
	if e.m.sw == nil || e.m.sw.body != nil || e.m.sw.ch.MRBody != "" {
		t.Fatalf("esc should drop the edit and keep the dialog: %+v", e.m.sw)
	}
	e.wantNot("editing")

	e.press("E")
	e.press("enter")
	e.typ("written inline")
	next, _ := e.m.Update(tea.PasteMsg{Content: "\n- pasted one\n- pasted two"})
	e.m = next.(model)
	next, cmd := e.m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	e.m = next.(model)
	e.settle(cmd)
	if e.m.sw.body != nil {
		t.Fatal("ctrl+s should close the editor")
	}
	e.want("edited by hand", "written inline", "- pasted one", "- pasted two")

	e.press("enter")
	api := e.m.sw.result.Targets[0]
	mr, err := e.forge.MR(context.Background(), *api.MR)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mr.Description, "written inline") || !strings.Contains(mr.Description, "- pasted two") || !strings.Contains(mr.Description, "Part of ade-demo-env/tracker#3") {
		t.Errorf("MR description = %q", mr.Description)
	}
}

func TestInlineBodyEditorNeedsAnMRStep(t *testing.T) {
	e := newSWEnv(t)
	e.selectRow(trk(3).String())
	e.press("s", "m", "enter") // branch only: no MR in this run
	if e.m.sw.stage != swConfirm {
		t.Fatalf("stage = %v", e.m.sw.stage)
	}
	e.press("E")
	if e.m.sw.body != nil {
		t.Error("E opened a body editor though no MR is created")
	}
}

func (e *swEnvT) typ(s string) {
	e.t.Helper()
	for _, r := range s {
		e.press(string(r))
	}
}
