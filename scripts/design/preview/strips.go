package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Strip Board: each item is a flight-progress strip with fixed fields, racked
// in bays by who holds control.
type stripBoard struct{}

var (
	sbStory = c("#3a6ea5")
	sbBug   = c("#c0392b")
	sbBuff  = c("#e8dcc0")
)

func (stripBoard) name() string { return "Strip Board" }

func sbStrip(it item, w int, sel bool) string {
	edge := sbStory
	if it.typ == "bug" {
		edge = sbBug
	}
	if it.triage {
		edge = faint
	}
	abbr := map[string]string{"assigned": "ASG", "started": "STR", "branch": "BRN", "draft": "DRF", "ci": "CI", "review": "RVW", "merged": "MRG"}[stages[it.stage]]
	if it.blocked {
		abbr = "BLK"
	}
	sep := faint.Render(" │ ")
	titleW := w - 6 - 5 - 3*6 - 5 - 7 - 8 - 7
	title := pad(strings.ToUpper(it.title), titleW)
	if sel {
		title = bold.Render(title)
	}
	fields := []string{sbBuff.Render(pad(fmt.Sprintf("#%d", it.ref), 4)), title, bold.Render(pad(abbr, 3)), pad(fmt.Sprintf("%d THR", it.threads), 5), pad(strings.ToUpper(appsLabel(it)), 7), faint.Render(pad(it.moved, 6))}
	mark := " "
	if sel {
		mark = "▸"
	}
	return mark + edge.Render("▐") + " " + strings.Join(fields, sep) + " " + edge.Render("▌")
}

func (stripBoard) home(w, h, sel int) string {
	head := " " + bold.Render("ADE") + "  demo-gl" + faint.Render(" · demo-gh") + "    Sprint 42 " + faint.Render("· ends Fri 13 Nov · 2 workdays left") + strings.Repeat(" ", 24) + faint.Render("⟳ 40s  ") + bold.Render("● 2")
	bay := func(name, note string) string {
		t := " " + bold.Render("BAY · "+name) + " " + faint.Render(note) + " "
		return t + faint.Render(strings.Repeat("═", max(1, w-lipgloss.Width(t)-2)))
	}
	l := []string{head, "", bay("YOU", "you hold control")}
	for i, it := range myWork {
		if it.court == "you" {
			l = append(l, sbStrip(it, w-4, i == sel))
		}
	}
	l = append(l, "", bay("WAITING", "blocked outside the team"))
	for i, it := range myWork {
		if it.blocked {
			l = append(l, sbStrip(it, w-4, i == sel))
		}
	}
	l = append(l, "", bay("REVIEWERS · CI", "empty"), faint.Render("   No strips handed off."), "", bay("PENDING", "Sprint 42–43, unassigned"))
	for _, it := range sprintOpen {
		l = append(l, faint.Render(sbStrip(it, w-4, false)))
	}
	return fit(strings.Join(l, "\n"), w, h-1) + "\n " + keys("enter", "open strip", "s", "new strip", "w", "workspace", "i", "message log", "?", "keys")
}

func (stripBoard) dialog(w int) string {
	var l []string
	l = append(l, bold.Render("New strip")+faint.Render("  handing tracker#3 to api"), "")
	l = append(l, sbStrip(myWork[2], w-6, true), "")
	for _, p := range plan {
		l = append(l, " "+sbBuff.Render(pad(strings.ToUpper(p.step), 11))+pad(p.detail, w-18))
	}
	l = append(l, "", keys("enter", "rack strip", "e", "amend field", "esc", "cancel"))
	return lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("#3a6ea5")).Padding(0, 1).Width(w).Render(strings.Join(l, "\n"))
}

func (stripBoard) inbox(h int) string {
	w := 58
	var l []string
	l = append(l, bold.Render("Message log")+faint.Render(" · 2 unread"), "")
	for _, n := range inbox {
		edge := map[string]lipgloss.Style{"warn": c("#e8a33d"), "error": sbBug, "success": c("#3fa66b"), "info": sbStory}[n.sev]
		l = append(l, edge.Render("▐ ")+pad(n.title, w-14)+faint.Render(rpad(n.ago, 9)), edge.Render("▐ ")+faint.Render(n.kind), "")
	}
	l = append(l, keys("enter", "open", "r", "read", "esc", "close"))
	return lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1).Width(w).Height(h).Render(strings.Join(l, "\n"))
}
