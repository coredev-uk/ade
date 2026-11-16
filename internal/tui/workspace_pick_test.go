package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/workspace"
)

// multiHarness is the workspace harness over a project with three repos.
func multiHarness(t *testing.T) *wsHarness {
	t.Helper()
	h := newWsHarness(t)
	for _, r := range []string{"web", "infra"} {
		if err := os.MkdirAll(filepath.Join(h.root, "demo", r, "main"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manifest := "name = \"demo\"\n" +
		"[repos.api]\npath = \"g/api\"\ndefault_branch = \"main\"\napp = [\"api\"]\n" +
		"[repos.web]\npath = \"g/web\"\ndefault_branch = \"main\"\napp = [\"web\"]\n" +
		"[repos.infra]\npath = \"g/infra\"\ndefault_branch = \"main\"\napp = [\"infra\"]\n"
	snap := "source = \"x\"\nmanifest = '''\n" + manifest + "'''\n"
	if err := os.WriteFile(filepath.Join(h.root, "demo", ".project.toml"), []byte(snap), 0o644); err != nil {
		t.Fatal(err)
	}
	return h
}

func threeRepoItem() domain.WorkItem {
	mr := func(repo string, iid int, branch string) forge.MR {
		return forge.MR{Ref: forge.Ref{Project: "g/" + repo, IID: iid, Kind: forge.KindMR}, SourceBranch: branch, State: "opened"}
	}
	return domain.WorkItem{
		Ref:   forge.Ref{Project: "g/tracker", IID: 3},
		Repos: []string{"api", "web", "infra"},
		MRs:   []forge.MR{mr("api", 5, "1-rate-limit"), mr("web", 6, "2-ui"), mr("infra", 7, "3-iac")},
	}
}

func TestMRPageWOpensTheShownMRsTree(t *testing.T) {
	h := multiHarness(t)
	it := threeRepoItem()
	h.m.mr = &mrPage{project: "demo", item: &it, mrs: it.MRs, cur: 1}
	h.press()
	want := wsCall{"demo", "web", filepath.Join(h.root, "demo", "web", "main")}
	if len(h.calls) != 1 || h.calls[0] != want {
		t.Fatalf("calls = %+v, want %+v", h.calls, want)
	}
	h.m.mr.cur = 2
	h.calls = nil
	h.press()
	if len(h.calls) != 1 || h.calls[0].repo != "infra" {
		t.Fatalf("calls = %+v, want infra", h.calls)
	}
}

func stubItemWorktrees(t *testing.T, wts []workspace.ItemWorktree) {
	t.Helper()
	old := wsItemWorktrees
	wsItemWorktrees = func(context.Context, string, *config.Manifest, domain.WorkItem) []workspace.ItemWorktree { return wts }
	t.Cleanup(func() { wsItemWorktrees = old })
}

func homeWithItem(t *testing.T, h *wsHarness) {
	t.Helper()
	h.selectRef("g/tracker#3")
	it := threeRepoItem()
	h.m.homeRows[h.m.homeSel].item = &it
}

func threeTrees(h *wsHarness) []workspace.ItemWorktree {
	var out []workspace.ItemWorktree
	for i, p := range [][2]string{{"api", "1-rate-limit"}, {"web", "2-ui"}, {"infra", "3-iac"}} {
		out = append(out, workspace.ItemWorktree{Repo: p[0], Path: filepath.Join(h.root, "demo", p[0], p[1]), Branch: p[1], Primary: i == 0})
	}
	return out
}

// run feeds a command's message back, as the runtime does.
func (h *wsHarness) run(cmd tea.Cmd) {
	h.t.Helper()
	next, _ := h.m.Update(cmd())
	h.m = next.(model)
}

func TestHomeWWithSeveralTreesAsksWhichOne(t *testing.T) {
	h := multiHarness(t)
	stubItemWorktrees(t, threeTrees(h))
	homeWithItem(t, h)
	next, cmd := h.m.Update(keyPress("w"))
	h.m = next.(model)
	h.run(cmd)
	if h.m.hs.pal == nil || len(h.calls) != 0 {
		t.Fatalf("picker = %v, calls = %v", h.m.hs.pal, h.calls)
	}
	var names []string
	for _, e := range h.m.hs.pal.all {
		names = append(names, ansi.Strip(e.name))
	}
	if len(names) != 4 || !strings.Contains(names[0], "api") || !strings.Contains(names[0], "1-rate-limit") || !strings.Contains(names[0], "!5 open") || !strings.HasPrefix(names[3], "all") {
		t.Fatalf("entries = %q", names)
	}
	// esc cancels, nothing opens.
	next, _ = h.m.Update(keyMsg("esc"))
	h.m = next.(model)
	if h.m.hs.pal != nil || len(h.calls) != 0 {
		t.Fatal("esc must cancel the picker")
	}
}

func TestHomeWPickerEnterOpensTheChosenRepo(t *testing.T) {
	h := multiHarness(t)
	stubItemWorktrees(t, threeTrees(h))
	homeWithItem(t, h)
	next, cmd := h.m.Update(keyPress("w"))
	h.m = next.(model)
	h.run(cmd)
	next, _ = h.m.Update(keyMsg("down"))
	h.m = next.(model)
	next, cmd = h.m.Update(keyMsg("enter"))
	h.m = next.(model)
	h.run(cmd)
	want := wsCall{"demo", "web", filepath.Join(h.root, "demo", "web", "2-ui")}
	if len(h.calls) != 1 || h.calls[0] != want {
		t.Fatalf("calls = %+v, want %+v", h.calls, want)
	}
}

func TestHomeWPickerAllOpensEveryTree(t *testing.T) {
	h := multiHarness(t)
	stubItemWorktrees(t, threeTrees(h))
	homeWithItem(t, h)
	next, cmd := h.m.Update(keyPress("w"))
	h.m = next.(model)
	h.run(cmd)
	for range 3 {
		next, _ = h.m.Update(keyMsg("down"))
		h.m = next.(model)
	}
	next, cmd = h.m.Update(keyMsg("enter"))
	h.m = next.(model)
	h.run(cmd)
	if len(h.calls) != 3 || h.calls[0].repo != "api" || h.calls[1].repo != "web" || h.calls[2].repo != "infra" {
		t.Fatalf("calls = %+v", h.calls)
	}
}

func TestHomeWWithOneTreeOpensAtOnce(t *testing.T) {
	h := multiHarness(t)
	stubItemWorktrees(t, threeTrees(h)[:1])
	homeWithItem(t, h)
	next, cmd := h.m.Update(keyPress("w"))
	h.m = next.(model)
	h.run(cmd)
	if h.m.hs.pal != nil || len(h.calls) != 1 {
		t.Fatalf("picker = %v, calls = %v", h.m.hs.pal, h.calls)
	}
}

func TestRowBadgeShowsTheWorstPipeline(t *testing.T) {
	m := model{g: unicodeGlyphs}
	now := time.Now()
	mr := func(status string, age time.Duration) forge.MR {
		return forge.MR{State: "opened", Pipeline: &forge.Pipeline{Status: status, UpdatedAt: now.Add(-age)}}
	}
	for _, c := range []struct {
		name string
		mrs  []forge.MR
		want string
	}{
		{"failed beats a newer success", []forge.MR{mr("failed", time.Hour), mr("success", 0)}, m.g.Bad},
		{"running beats success", []forge.MR{mr("running", time.Hour), mr("success", 0)}, m.g.Sync},
		{"canceled beats success", []forge.MR{mr("success", 0), mr("canceled", time.Hour)}, m.g.Ahead},
		{"success alone", []forge.MR{mr("success", 0)}, m.g.OK},
	} {
		it := &domain.WorkItem{MRs: c.mrs}
		if got := ansi.Strip(m.rowBadge(it)); !strings.Contains(got, c.want) {
			t.Errorf("%s: badge %q lacks %q", c.name, got, c.want)
		}
	}
}
