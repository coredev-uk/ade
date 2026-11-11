package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/workspace"
)

// codeHarness records the requests `v` makes instead of writing a workspace.
type codeHarness struct {
	*wsHarness
	reqs []workspace.CodeRequest
	res  workspace.CodeOutcome
	err  error
}

func newCodeHarness(t *testing.T) *codeHarness {
	t.Helper()
	h := &codeHarness{wsHarness: newWsHarness(t)}
	old := codeWorkspace
	codeWorkspace = func(_ context.Context, _ *app.App, r workspace.CodeRequest) (workspace.CodeOutcome, error) {
		h.reqs = append(h.reqs, r)
		return h.res, h.err
	}
	t.Cleanup(func() { codeWorkspace = old })
	return h
}

// pressV sends v and feeds the command's answer back.
func (h *codeHarness) pressV() {
	h.t.Helper()
	next, cmd := h.m.Update(keyPress("v"))
	h.m = next.(model)
	if cmd != nil {
		h.send(cmd())
	}
}

func TestVWritesAndOpensTheItemsWorkspace(t *testing.T) {
	h := newCodeHarness(t)
	h.res = workspace.CodeOutcome{CodeResult: workspace.CodeResult{Path: "/records/3-retry.code-workspace"}, Opened: true}
	h.selectRef("g/tracker#3")
	h.pressV()
	if len(h.reqs) != 1 || h.reqs[0].Item == nil || h.reqs[0].Item.Item.Ref.IID != 3 || h.reqs[0].Item.Project != "demo" || !h.reqs[0].Open || h.reqs[0].Worktree != "" {
		t.Fatalf("requests = %+v", h.reqs)
	}
	e := h.lastToast()
	if e.Kind != "workspace.vscode" || e.Severity != events.Success || e.Title != "VS Code workspace: /records/3-retry.code-workspace" {
		t.Fatalf("pop-up: %+v", e)
	}
}

func TestVWithoutCodeShowsThePathAndTheHint(t *testing.T) {
	h := newCodeHarness(t)
	h.res = workspace.CodeOutcome{CodeResult: workspace.CodeResult{Path: "/records/x.code-workspace"}, Hint: "code is not on PATH: open /records/x.code-workspace in VS Code"}
	h.selectRef("g/tracker#3")
	h.pressV()
	e := h.lastToast()
	if e.Severity != events.Warn || !strings.Contains(e.Title, "/records/x.code-workspace") || !strings.Contains(e.Subject, "not on PATH") {
		t.Fatalf("pop-up: %+v", e)
	}
}

func TestVFailureIsAPopup(t *testing.T) {
	h := newCodeHarness(t)
	h.err = errors.New("existing workspace file is not plain JSON\nmore")
	h.selectRef("g/tracker#3")
	h.pressV()
	e := h.lastToast()
	if e.Severity != events.Error || e.Subject != "existing workspace file is not plain JSON" {
		t.Fatalf("pop-up: %+v", e)
	}
}

func TestVOnAnMRRowUsesItsItemElseItsTree(t *testing.T) {
	h := newCodeHarness(t)
	mr := forge.MR{Ref: forge.Ref{Project: "g/api", IID: 4, Kind: forge.KindMR}, SourceBranch: "4-x"}
	h.m.homeRows = []homeRow{{project: "demo", mr: &mr}}
	h.m.homeSel = 0
	h.pressV()
	if len(h.reqs) != 1 || h.reqs[0].Item != nil || h.reqs[0].Worktree != filepath.Join(h.root, "demo", "api", "main") {
		t.Fatalf("an MR without an item anchors on its repo's tree: %+v", h.reqs)
	}

	// Once an item owns the MR, the item's workspace is generated.
	h.m.results[0].Items[0].MRs = []forge.MR{mr}
	h.pressV()
	if len(h.reqs) != 2 || h.reqs[1].Item == nil || h.reqs[1].Item.Item.Ref.IID != 3 {
		t.Fatalf("an MR of an item: %+v", h.reqs[1:])
	}
}

func TestVIsInThePaletteAndHelp(t *testing.T) {
	h := newCodeHarness(t)
	h.selectRef("g/tracker#1")
	var found bool
	for _, e := range h.m.paletteEntries() {
		if e.key == "v" {
			found = true
			if e.name != "VS Code workspace for g/tracker#1" || !strings.Contains(e.cli, "ade vscode workspace") {
				t.Errorf("entry = %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("no palette entry for v")
	}
	h.keys(":")
	h.keys("v", "s", "c", "o", "d", "e")
	if v := h.m.hs.pal.visible(); len(v) == 0 || !strings.HasPrefix(v[0].name, "VS Code workspace") {
		t.Fatalf("palette finds %+v", v)
	}
	h.keys("enter")
	if len(h.reqs) != 0 { // the key press returns a command; run it
		t.Fatalf("ran before the command executed: %+v", h.reqs)
	}
	h.m.hs.pal = nil
	h.keys("?")
	if out := h.screen(); !strings.Contains(out, "VS Code workspace") {
		t.Errorf("help does not list v:\n%s", out)
	}
}
