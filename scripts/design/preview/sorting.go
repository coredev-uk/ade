package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Sorting Frame: work is post. Items are slips addressed to whoever must act,
// sorted into pigeonholes and franked with the day they last moved.
type sortingFrame struct{}

var (
	sfRed   = lipgloss.Color("#c8102e")
	sfCream = lipgloss.Color("#efe6d2")
	sfInk   = c("#2f5d8a")
	sfRedFg = c("#c8102e")
)

func (sortingFrame) name() string { return "Sorting Frame" }

func sfSlip(it item, w int, selected bool) []string {
	t := pad(fmt.Sprintf("#%d %s", it.ref, it.title), w)
	if selected {
		t = lipgloss.NewStyle().Reverse(true).Render(t)
	} else {
		t = bold.Render(t)
	}
	status := stages[it.stage]
	if it.triage {
		status = "unsorted"
	}
	return []string{t, pad(status+" · "+it.why, w), sfInk.Render(pad("⌁ "+it.moved+"  "+strings.ToUpper(appsLabel(it)), w))}
}

func (sortingFrame) home(w, h, sel int) string {
	head := lipgloss.NewStyle().Background(sfRed).Foreground(sfCream).Bold(true).Width(w).
		Render(fmt.Sprintf(" ADE · demo-gl    Sprint 42 · last collection Fri 13 Nov · 2 workdays left    ✉ 2 in the box"))
	colW := []int{w * 40 / 100, w * 18 / 100, w * 14 / 100}
	colW = append(colW, w-colW[0]-colW[1]-colW[2]-5)
	holes := []string{"YOU", "REVIEWERS", "CI", "WAITING"}
	var cols [4][]string
	for i, it := range myWork {
		if it.triage {
			continue
		}
		dst := 0
		if it.blocked {
			dst = 3
		}
		cols[dst] = append(cols[dst], sfSlip(it, colW[dst]-2, i == sel)...)
		cols[dst] = append(cols[dst], "")
	}
	for i := 1; i < 3; i++ {
		cols[i] = []string{faint.Render("(empty)")}
	}
	top := "┌"
	for i, n := range holes {
		label := "─ " + bold.Render(n) + " "
		top += label + strings.Repeat("─", colW[i]-lipgloss.Width(label))
		if i < 3 {
			top += "┬"
		}
	}
	top += "┐"
	rows := h - 9
	var body []string
	for y := range rows {
		line := "│"
		for i := range holes {
			cell := ""
			if y < len(cols[i]) {
				cell = cols[i][y]
			}
			line += " " + pad(cell, colW[i]-1) + "│"
		}
		body = append(body, line)
	}
	bot := "└" + strings.Repeat("─", colW[0]) + "┴" + strings.Repeat("─", colW[1]) + "┴" + strings.Repeat("─", colW[2]) + "┴" + strings.Repeat("─", colW[3]) + "┘"
	unsorted := " " + sfRedFg.Bold(true).Render("UNSORTED") + faint.Render("  #11 Exports page sometimes shows stale totals · needs type and app")
	next := " " + bold.Render("NEXT COLLECTION · Sprint 43") + faint.Render("  #8 Scheduled exports (api+web) · #7 Dark mode for the export dashboard")
	foot := " " + keys("enter", "open slip", "s", "dispatch", "w", "workspace", "i", "letterbox", "?", "keys")
	return head + "\n" + top + "\n" + strings.Join(body, "\n") + "\n" + bot + "\n" + unsorted + "\n" + next + "\n\n" + foot
}

func (sortingFrame) dialog(w int) string {
	var l []string
	l = append(l, sfRedFg.Bold(true).Render("DISPATCH DOCKET")+faint.Render("   tracker#3 · Retry failed webhook deliveries"))
	l = append(l, "To: "+bold.Render("api")+faint.Render(" (addressed by app::api)")+"    "+sfInk.Render("⌁ 11 NOV"), "")
	for i, p := range plan {
		l = append(l, fmt.Sprintf(" %d. ", i+1)+bold.Render(pad(p.step, 12))+pad(p.detail, w-21))
	}
	l = append(l, "", keys("enter", "dispatch", "e", "amend", "esc", "return to frame"))
	return lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).BorderForeground(sfRed).Padding(0, 1).Width(w).Render(strings.Join(l, "\n"))
}

func (sortingFrame) inbox(h int) string {
	w := 58
	var l []string
	l = append(l, sfRedFg.Bold(true).Render("LETTERBOX")+faint.Render(" · 2 unopened"), "")
	for i, n := range inbox {
		t := pad(n.title, w-14)
		if i < 2 {
			t = bold.Render(t)
		}
		l = append(l, sfInk.Render("⌁ "+pad(n.ago, 9))+" "+t, "  "+faint.Render(n.kind), "")
	}
	l = append(l, keys("enter", "open", "r", "mark opened", "esc", "close"))
	return lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(sfRed).Padding(0, 1).Width(w).Height(h).Render(strings.Join(l, "\n"))
}
