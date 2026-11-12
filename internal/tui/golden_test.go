package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/goldentest"
)

// Golden-file tests (internal/tui/testdata/golden/*.golden): the dashboard
// runs in a real tea.Program through teatest at 80x24 and 160x45, and the
// frame it ends on is compared with the file. The default files are the screen
// as text; the nocolor files are the frame after Bubble Tea's NO_COLOR
// downsampling with the escape sequences kept (so they show what survives:
// weight and faint, never colour); the ascii files run the ascii glyph set.
// Rewrite them with `go test ./internal/tui -run TestGolden -update`.

var goldenSizes = []struct{ w, h int }{{80, 24}, {160, 45}}

// goldenDrop leaves out the clock and the spinner: a frame must not depend on
// when the test ran.
func goldenDrop(msg tea.Msg) bool {
	switch msg.(type) {
	case tickMsg, mrSpinMsg:
		return true
	}
	return false
}

func tkey(k rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: k} }

const richDescription = "Exports hammer the API during the nightly batch.\n\n" +
	"## Plan\n\n- **measure** the current rate with `ade items`\n- [x] pick a limiter\n- [ ] add the middleware, see [the design](https://example.com/design/rate-limit)\n\n" +
	"> Keep the limit per tenant, not per token.\n\n" +
	"```go\nlimiter := rate.NewLimiter(50, 10)\n```\n\n## Acceptance criteria\n\n- [x] 429 over the limit\n- [ ] documented\n"

// frame runs a scenario and returns the three renderings of its last frame.
type golden struct {
	name       string
	w, h       int
	build      func(t *testing.T, ascii bool) (model, goldentest.Drive)
	asciiToo   bool // also run with the ascii glyph set
	noColorToo bool // also run under NO_COLOR
}

func (g golden) run(t *testing.T) {
	t.Helper()
	size := fmt.Sprintf("%dx%d", g.w, g.h)
	t.Run(g.name+"_"+size, func(t *testing.T) {
		t.Setenv("NO_COLOR", "") // the frame is compared as text; code colours are not the point here
		m, drive := g.build(t, false)
		frame := goldentest.Run(t, m, g.w, g.h, goldenDrop, drive)
		goldentest.Equal(t, g.name+"_"+size, goldentest.Plain(frame))
	})
	if g.noColorToo {
		t.Run(g.name+"_nocolor_"+size, func(t *testing.T) {
			t.Setenv("NO_COLOR", "1")
			m, drive := g.build(t, false)
			frame := goldentest.NoColor(goldentest.Run(t, m, g.w, g.h, goldenDrop, drive))
			if goldentest.HasColour(frame) {
				t.Errorf("a colour sequence survived NO_COLOR:\n%q", frame)
			}
			goldentest.Equal(t, g.name+"_nocolor_"+size, frame)
		})
	}
	if g.asciiToo {
		t.Run(g.name+"_ascii_"+size, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			m, drive := g.build(t, true)
			frame := goldentest.Plain(goldentest.Run(t, m, g.w, g.h, goldenDrop, drive))
			goldentest.Equal(t, g.name+"_ascii_"+size, frame)
		})
	}
}

// --- Home -----------------------------------------------------------------------

func homeGolden(t *testing.T, ascii bool, preview bool) (model, goldentest.Drive) {
	h := homeHarness(t, 160)
	if ascii {
		h.m.g = asciiGlyphs
	}
	res := sampleResult()
	res.Items[1].Issue.Description = richDescription
	next, _ := h.m.Update(resultsMsg{results: []forgesync.Result{res}})
	h.m = next.(model)
	h.m.homeLoaded = true
	h.m.syncing = false
	return h.m, func(tm *teatest.TestModel) {
		if preview {
			tm.Send(tkey(tea.KeyTab))
		}
	}
}

func TestGoldenHome(t *testing.T) {
	for _, s := range goldenSizes {
		golden{name: "home", w: s.w, h: s.h, asciiToo: true, noColorToo: true, build: func(t *testing.T, a bool) (model, goldentest.Drive) {
			return homeGolden(t, a, false)
		}}.run(t)
	}
	// The preview markdown: the issue description rendered by glamour. On a
	// narrow terminal the preview is a tab away.
	golden{name: "home_preview", w: 80, h: 24, asciiToo: true, noColorToo: true, build: func(t *testing.T, a bool) (model, goldentest.Drive) {
		return homeGolden(t, a, true) // the selected row is #1, which carries the description
	}}.run(t)
}

// --- MR page ----------------------------------------------------------------------

// mrGolden opens tracker#1's MR page, reads every tab (the page loads each
// lazily) and gives the work item and the MR a rich markdown description, so
// the frame needs no network and the tab key only switches what is shown.
func mrGolden(t *testing.T, ascii bool, tab string, itemDescription bool) (model, goldentest.Drive) {
	e := newMREnv(t, fake.Premium)
	if ascii {
		e.m.g = asciiGlyphs
	}
	e.open(grp + "/tracker#1")
	e.press("6", "5", "4", "3", "2", "1")
	if itemDescription {
		e.m.mr.item.Issue.Description = richDescription
	}
	e.m.mr.data.mr.Description = richDescription
	return e.m, func(tm *teatest.TestModel) {
		tm.Type(tab)
		if !itemDescription {
			tm.Type(strings.Repeat("j", 40)) // scrolled to the end: the MR's own description
		}
	}
}

func TestGoldenMRPage(t *testing.T) {
	tabs := []struct{ key, name string }{{"1", "overview"}, {"2", "threads"}, {"3", "diff"}, {"4", "review"}, {"5", "ci"}, {"6", "history"}}
	for _, s := range goldenSizes {
		for _, tb := range tabs {
			golden{
				name: "mr_" + tb.name, w: s.w, h: s.h, asciiToo: tb.key == "1" || tb.key == "3", noColorToo: tb.key == "1" || tb.key == "3",
				build: func(t *testing.T, a bool) (model, goldentest.Drive) { return mrGolden(t, a, tb.key, true) },
			}.run(t)
		}
	}
}

// The MR's own description (and AI summary text) takes the page's top when
// the work item has none of its own.
func TestGoldenMRDescription(t *testing.T) {
	for _, s := range goldenSizes {
		golden{
			name: "mr_description", w: s.w, h: s.h, asciiToo: true, noColorToo: true,
			build: func(t *testing.T, a bool) (model, goldentest.Drive) { return mrGolden(t, a, "1", false) },
		}.run(t)
	}
}

// --- Job log ----------------------------------------------------------------------

// logGolden opens the failed lint job's log from the CI tab: the log fills the screen.
func logGolden(t *testing.T, ascii bool) (model, goldentest.Drive) {
	e := newMREnv(t, fake.Free)
	if ascii {
		e.m.g = asciiGlyphs
	}
	e.open(mrRef("web", 1).String())
	e.press("5", "j", "l")
	return e.m, nil
}

func TestGoldenJobLog(t *testing.T) {
	for _, s := range goldenSizes {
		golden{
			name: "job_log", w: s.w, h: s.h,
			build: logGolden,
		}.run(t)
	}
}

// --- Start work dialog ------------------------------------------------------------

func swGolden(t *testing.T, ascii bool, confirm bool) (model, goldentest.Drive) {
	e := newSWEnv(t)
	if ascii {
		e.m.g = asciiGlyphs
	}
	e.selectRow(trk(3).String())
	e.press("s")
	if confirm {
		e.press("enter")
	}
	return e.m, nil
}

func TestGoldenStartWork(t *testing.T) {
	for _, s := range goldenSizes {
		golden{
			name: "startwork_choose", w: s.w, h: s.h, asciiToo: true, noColorToo: true,
			build: func(t *testing.T, a bool) (model, goldentest.Drive) { return swGolden(t, a, false) },
		}.run(t)
		golden{
			name: "startwork_confirm", w: s.w, h: s.h, asciiToo: true, noColorToo: true,
			build: func(t *testing.T, a bool) (model, goldentest.Drive) { return swGolden(t, a, true) },
		}.run(t)
	}
}
