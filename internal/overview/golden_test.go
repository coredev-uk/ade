package overview

import (
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/coredev-uk/ade/internal/goldentest"
)

// Golden files (testdata/golden/*.golden): the pane runs in a real tea.Program
// through teatest at 80x24 and 160x45; the default files are the screen as
// text, the nocolor ones the frame after NO_COLOR downsampling with its escape
// sequences, the ascii ones the ascii glyph set. `go test ./internal/overview
// -run TestGolden -update` rewrites them.

const goldenDescription = "Exports hammer the API during the nightly batch.\n\n" +
	"## Plan\n\n- **measure** the current rate with `ade items`\n- [x] pick a limiter\n- [ ] add the middleware, see [the design](https://example.com/design/rate-limit)\n\n" +
	"> Keep the limit per tenant, not per token.\n\n" +
	"```go\nlimiter := rate.NewLimiter(50, 10)\n```\n\n## Acceptance criteria\n\n- [x] 429 over the limit\n- [ ] documented, with the `Retry-After` header explained in the reference\n- [ ] dashboard shows it\n"

const goldenNotes = "ask support about **50 req/min**\n\n1. check the tenant list\n2. then `ade sync`\n"

func paneModel(glyphs string) model {
	it := item()
	it.Issue.Description = goldenDescription
	s := snap(it)
	s.Notes = goldenNotes
	m := newModel(Options{Loader: &Loader{Ref: it.Ref}, Glyphs: glyphs, Now: func() time.Time { return now }})
	next, _ := m.Update(loadedMsg{snap: s, reloaded: true})
	return next.(model)
}

func TestGoldenPane(t *testing.T) {
	t.Setenv("NO_COLOR", "") // the colour frames are compared as text
	for _, s := range []struct{ w, h int }{{80, 24}, {160, 45}} {
		size := fmt.Sprintf("%dx%d", s.w, s.h)
		run := func(glyphs string, drive goldentest.Drive) string {
			return goldentest.Run(t, paneModel(glyphs), s.w, s.h, nil, drive)
		}
		goldentest.Equal(t, "pane_"+size, goldentest.Plain(run("", nil)))
		goldentest.Equal(t, "pane_ascii_"+size, goldentest.Plain(run("ascii", nil)))
		t.Run("nocolor_"+size, func(t *testing.T) {
			t.Setenv("NO_COLOR", "1")
			frame := goldentest.NoColor(goldentest.Run(t, paneModel(""), s.w, s.h, nil, nil))
			if goldentest.HasColour(frame) {
				t.Errorf("a colour sequence survived NO_COLOR:\n%q", frame)
			}
			goldentest.Equal(t, "pane_nocolor_"+size, frame)
		})
		// The pane scrolls: G ends on the notes.
		goldentest.Equal(t, "pane_end_"+size, goldentest.Plain(run("", func(tm *teatest.TestModel) { tm.Send(tea.KeyPressMsg{Code: 'G', Text: "G"}) })))
	}
}
