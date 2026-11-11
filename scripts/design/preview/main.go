// Command preview renders ADE's candidate visual directions full screen, with
// the Start-work dialog and inbox drawer, so a direction can be chosen in a
// real terminal. Throwaway: deleted once DESIGN.md is written.
//
//	go run ./scripts/design/preview [-d 1..5] [-dialog] [-inbox]
//	keys: 1-5 / tab switch direction · d dialog · i inbox · j/k move · q quit
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type world interface {
	name() string
	home(w, h, sel int) string
	dialog(w int) string
	inbox(h int) string
}

var worlds = []world{interchange{}, sortingFrame{}, departures{}, overprint{}, stripBoard{}}

type model struct {
	w, h           int
	dir, sel       int
	dialog, drawer bool
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.KeyPressMsg:
		switch k := msg.String(); k {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab":
			m.dir = (m.dir + 1) % len(worlds)
		case "shift+tab":
			m.dir = (m.dir + len(worlds) - 1) % len(worlds)
		case "1", "2", "3", "4", "5":
			m.dir = int(k[0] - '1')
		case "d", "s":
			m.dialog = !m.dialog
		case "i":
			m.drawer = !m.drawer
		case "esc":
			m.dialog, m.drawer = false, false
		case "j", "down":
			m.sel = min(m.sel+1, len(myWork)-1)
		case "k", "up":
			m.sel = max(m.sel-1, 0)
		}
	}
	return m, nil
}

func (m model) View() tea.View {
	v := tea.NewView(render(m))
	v.AltScreen = true
	return v
}

func render(m model) string {
	w, h := max(m.w, 100), max(m.h, 30)
	wd := worlds[m.dir]
	base := lipgloss.NewLayer(fit(wd.home(w, h, m.sel), w, h))
	layers := []*lipgloss.Layer{base}
	if m.drawer {
		d := wd.inbox(h - 2)
		layers = append(layers, lipgloss.NewLayer(d).X(w-lipgloss.Width(d)).Y(1).Z(1))
	}
	if m.dialog {
		d := wd.dialog(min(96, w-8))
		layers = append(layers, lipgloss.NewLayer(d).X((w-lipgloss.Width(d))/2).Y(max(2, (h-lipgloss.Height(d))/2)).Z(2))
	}
	return lipgloss.NewCompositor(layers...).Render()
}

func main() {
	dir := flag.Int("d", 1, "direction 1..5")
	dlg := flag.Bool("dialog", false, "open the Start-work dialog")
	inb := flag.Bool("inbox", false, "open the inbox drawer")
	flag.Parse()
	m := model{dir: (*dir - 1) % len(worlds), dialog: *dlg, drawer: *inb}
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// --- layout helpers ---------------------------------------------------------

// pad right-pads (or truncates) s to exactly n cells.
func pad(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) > n {
		return ansi.Truncate(s, n, "…")
	}
	return s + strings.Repeat(" ", n-lipgloss.Width(s))
}

// rpad left-pads s to n cells.
func rpad(s string, n int) string {
	if lipgloss.Width(s) >= n {
		return ansi.Truncate(s, n, "…")
	}
	return strings.Repeat(" ", n-lipgloss.Width(s)) + s
}

// fit forces a block to exactly w×h cells.
func fit(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	out := make([]string, h)
	for i := range out {
		if i < len(lines) {
			out[i] = pad(lines[i], w)
		} else {
			out[i] = strings.Repeat(" ", w)
		}
	}
	return strings.Join(out, "\n")
}

// columns joins blocks side by side with a separator column.
func columns(sep string, h int, blocks ...string) string {
	split := make([][]string, len(blocks))
	widths := make([]int, len(blocks))
	for i, b := range blocks {
		split[i] = strings.Split(b, "\n")
		widths[i] = lipgloss.Width(b)
	}
	var sb strings.Builder
	for y := range h {
		for i := range blocks {
			if i > 0 {
				sb.WriteString(sep)
			}
			line := ""
			if y < len(split[i]) {
				line = split[i][y]
			}
			sb.WriteString(pad(line, widths[i]))
		}
		if y < h-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func c(hex string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)) }

var (
	plain = lipgloss.NewStyle()
	bold  = lipgloss.NewStyle().Bold(true)
	faint = lipgloss.NewStyle().Faint(true)
)

func appsLabel(it item) string { return strings.Join(it.apps, "+") }
