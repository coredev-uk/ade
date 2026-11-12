package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/agent"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// nonASCII lists the runes of s above 127 with the line each is on.
func nonASCII(s string) string {
	var bad []string
	for i, ln := range strings.Split(ansi.Strip(s), "\n") {
		for _, r := range ln {
			if r > 127 {
				bad = append(bad, fmt.Sprintf("line %d: %q in %q", i, r, ln))
				break
			}
		}
	}
	return strings.Join(bad, "\n")
}

func assertASCII(t *testing.T, name, screen string) {
	t.Helper()
	if bad := nonASCII(screen); bad != "" {
		t.Errorf("%s draws non-ASCII chrome under host.glyphs=ascii:\n%s\n--- screen:\n%s", name, bad, ansi.Strip(screen))
	}
}

// With host.glyphs = "ascii" every page, dialog and pop-up draws pure ASCII
// (the fixtures are ASCII too, so anything else is chrome).
func TestAsciiGlyphSetDrawsOnlyASCII(t *testing.T) {
	for _, w := range []int{140, 80} {
		// Home, its pages and every dialog opened from an item row.
		keys := []string{"", "?", ":", "i", "s", "n", "=", ">", "c", "a", "A", "S", "V", "b", "L", "w", "v", "W", "M", "f", "/", "e", "tab"}
		for _, k := range keys {
			e := newSWEnv(t)
			e.m.g, e.m.w, e.m.h = asciiGlyphs, w, 44
			e.selectRow(trk(3).String())
			if k != "" {
				e.press(k)
			}
			assertASCII(t, fmt.Sprintf("home key %q w=%d", k, w), e.screen())
			if k == "s" {
				e.press("enter")
				assertASCII(t, fmt.Sprintf("start work confirm w=%d", w), e.screen())
			}
		}
		// Toasts (every severity) and the notification centre.
		e := newSWEnv(t)
		e.m.g, e.m.w, e.m.h = asciiGlyphs, w, 44
		for _, sev := range []events.Severity{events.Info, events.Warn, events.Error} {
			e.m.nc.push(events.New("test.toast", sev, "a toast to look at"))
			assertASCII(t, fmt.Sprintf("toast %v w=%d", sev, w), e.screen())
		}
		e.press("i")
		assertASCII(t, fmt.Sprintf("centre w=%d", w), e.screen())

		// The MR page: every tab and dialog.
		for _, k := range []string{"1", "2", "3", "4", "5", "6", "?", ":", "m", "S", "a", "A", "R", "y", "tab"} {
			e := newMREnv(t, fake.Premium)
			e.m.g, e.m.w, e.m.h = asciiGlyphs, w, 44
			e.open(mrRef("api", 1).String())
			e.press("6", "5", "4", "3", "2", "1")
			e.press(k)
			assertASCII(t, fmt.Sprintf("mr key %q w=%d", k, w), e.screen())
		}

		// The agent dialog and its run list.
		ae := newAgEnv(t, asciiGlyphs, w)
		ae.seed("20261111-100000-aaa", agent.StateAwaiting, 0.0123)
		for _, k := range []string{"A", "enter", "esc", "a", "enter"} {
			ae.press(k)
			assertASCII(t, fmt.Sprintf("agent after %q w=%d", k, w), ae.view())
		}

		// Agile pages, budget and the log.
		am := agileModel(asciiGlyphs)
		for _, kind := range []agileKind{agileStandup, agileReview} {
			am.ag = agileUI{kind: kind, standup: standupFixture(), review: reviewFixture()}
			assertASCII(t, fmt.Sprintf("agile %d w=%d", kind, w), am.agileBody(w, 40))
		}
		assertASCII(t, fmt.Sprintf("budget w=%d", w), budModel(asciiGlyphs, w, 40).render())
		l, _ := logFixture(t)
		assertASCII(t, fmt.Sprintf("log w=%d", w), openLogPage(t, asciiGlyphs, l, w, 40).render())
	}
}
