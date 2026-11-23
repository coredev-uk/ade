package tui

import (
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

func helpKeys(rows [][2]string) []string {
	var ks []string
	for _, r := range rows {
		ks = append(ks, strings.Fields(strings.ReplaceAll(r[0], "/", " "))...)
	}
	return ks
}

// Every key the MR page's help lists has a palette entry, so a terminal that
// swallows keys still reaches each action by name.
func TestMRPalettePageReachesEveryHelpKey(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.enableAI("ask")
	e.open(mrRef("api", 2).String())
	e.press(":")
	if e.m.hs.pal == nil {
		t.Fatal(": did not open the palette on the MR page")
	}
	have := map[string]bool{}
	for _, pe := range e.m.hs.pal.all {
		have[pe.key] = true
	}
	for _, k := range helpKeys(mrHelpRows(true)) {
		if !have[k] {
			t.Errorf("MR help lists %q but the palette has no entry for it", k)
		}
	}
	for _, k := range []string{"1", "2", "3", "4", "5", "6"} {
		if !have[k] {
			t.Errorf("no palette entry for tab key %q", k)
		}
	}
}

func TestMRPaletteRunsAnActionByName(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.open(mrRef("api", 2).String())
	e.press(":")
	e.typ("Tab: CI")
	e.press("enter")
	if e.m.hs.pal != nil || e.m.mr == nil || e.m.mr.tab != tabCI {
		t.Fatalf("palette did not switch to CI: pal=%v mr=%v", e.m.hs.pal, e.m.mr)
	}
	e.press(":")
	e.typ("Merge")
	e.press("enter")
	if e.m.mr.modal == nil || e.m.mr.modal.kind != modalMerge {
		t.Fatal("palette Merge did not open the merge dialog")
	}
}

func TestWorktreesPaletteHasWorkspaceAndCopyEntries(t *testing.T) {
	h := homeHarness(t, 120)
	h.m.page = pageTrees
	have := map[string]bool{}
	for _, e := range h.m.paletteEntries() {
		have[e.key] = true
	}
	for _, k := range []string{"w", "W", "v", "y"} {
		if !have[k] {
			t.Errorf("Worktrees palette lacks %q", k)
		}
	}
	for _, g := range helpGroups(true, true) {
		for _, k := range helpKeys(g.rows) {
			switch k {
			case "w", "W", "v", "y":
				if !have[k] {
					t.Errorf("help lists %q for Worktrees without a palette entry", k)
				}
			}
		}
	}
}

// Without AI the Home help and palette stop offering what only a model does;
// what has a non-AI form (digest as a list, classify by keywords) stays.
func TestHomeHelpAndPaletteHideDeadAIEntriesWithoutAI(t *testing.T) {
	rows := func(ai bool) string {
		var b strings.Builder
		for _, full := range []bool{true, false} {
			for _, g := range helpGroups(full, ai) {
				for _, r := range g.rows {
					b.WriteString(r[0] + " " + r[1] + "\n")
				}
			}
		}
		return b.String()
	}
	if !strings.Contains(rows(true), "research") {
		t.Error("the help lists R with AI on")
	}
	if got := rows(false); strings.Contains(got, "research") || strings.Contains(got, "(AI)") {
		t.Errorf("the help lists a model-only key with AI off:\n%s", got)
	}
	if !strings.Contains(rows(false), "classify") {
		t.Error("classify has a keyword form and stays in the help")
	}

	e := newMREnv(t, fake.Free)
	e.m.mr = nil
	names := func() string {
		var b strings.Builder
		for _, en := range e.m.paletteEntries() {
			b.WriteString(en.name + "\n")
		}
		return b.String()
	}
	if got := names(); strings.Contains(got, "stand-in") || strings.Contains(got, "Research") || strings.Contains(got, "AI digest") ||
		!strings.Contains(got, "New comments since you last looked") || !strings.Contains(got, "Classify") {
		t.Errorf("palette with AI off:\n%s", got)
	}
	e.aiTextStub("auto")
	if got := names(); !strings.Contains(got, "stand-in") || !strings.Contains(got, "Research") || !strings.Contains(got, "AI digest") {
		t.Errorf("palette with AI on:\n%s", got)
	}
}
