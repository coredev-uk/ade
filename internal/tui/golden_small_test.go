package tui

import (
	"testing"

	"github.com/coredev-uk/ade/internal/goldentest"
)

// Golden frames at the small sizes (see layout.go for the breakpoints): 60x20
// is the smallest screen laid out, 100x30 the widest single pane. The files are
// testdata/golden/<name>_<w>x<h>.golden; `go test ./internal/tui -run TestGolden
// -update` rewrites them.

var goldenSmallSizes = []struct{ w, h int }{{60, 20}, {100, 30}}

func TestGoldenSmallScreens(t *testing.T) {
	for _, s := range goldenSmallSizes {
		golden{name: "home", w: s.w, h: s.h, asciiToo: true, noColorToo: true, build: func(t *testing.T, a bool) (model, goldentest.Drive) {
			return homeGolden(t, a, false)
		}}.run(t)
		golden{name: "home_preview", w: s.w, h: s.h, asciiToo: true, build: func(t *testing.T, a bool) (model, goldentest.Drive) {
			return homeGolden(t, a, true) // the preview is a tab away
		}}.run(t)
		golden{name: "mr_overview", w: s.w, h: s.h, asciiToo: true, build: func(t *testing.T, a bool) (model, goldentest.Drive) {
			return mrGolden(t, a, "1", true)
		}}.run(t)
		golden{name: "mr_threads", w: s.w, h: s.h, build: func(t *testing.T, a bool) (model, goldentest.Drive) {
			return mrGolden(t, a, "2", true)
		}}.run(t)
		golden{name: "startwork_choose", w: s.w, h: s.h, asciiToo: true, build: func(t *testing.T, a bool) (model, goldentest.Drive) {
			return swGolden(t, a, false)
		}}.run(t)
		golden{name: "startwork_confirm", w: s.w, h: s.h, build: func(t *testing.T, a bool) (model, goldentest.Drive) {
			return swGolden(t, a, true)
		}}.run(t)
		golden{name: "help", w: s.w, h: s.h, asciiToo: true, build: func(t *testing.T, a bool) (model, goldentest.Drive) {
			m, d := homeGolden(t, a, false)
			m.help = true
			return m, d
		}}.run(t)
		golden{name: "budget", w: s.w, h: s.h, asciiToo: true, build: func(t *testing.T, a bool) (model, goldentest.Drive) {
			useUTC(t)
			g := unicodeGlyphs
			if a {
				g = asciiGlyphs
			}
			return budModel(g, s.w, s.h), nil
		}}.run(t)
		golden{name: "standup", w: s.w, h: s.h, asciiToo: true, build: func(t *testing.T, a bool) (model, goldentest.Drive) {
			g := unicodeGlyphs
			if a {
				g = asciiGlyphs
			}
			m := agileModel(g)
			m.ag = agileUI{kind: agileStandup, standup: standupFixture()}
			return m, nil
		}}.run(t)
	}
}
