package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Departures: the sprint is a departure board. Fixed ruled columns, rows ranked
// by when they leave; changed cells flip once (static in this preview).
type departures struct{}

var (
	dpAmber = c("#f0a020")
	dpRed   = c("#e04a3a")
	dpGreen = c("#4cc38a")
)

func (departures) name() string { return "Departures" }

var dpCols = []struct {
	h string
	w int
}{{"DUE", 8}, {"REF", 5}, {"ITEM", 0}, {"STAGE", 10}, {"COURT", 14}, {"CI", 5}, {"PLAT", 9}, {"REMARKS", 24}}

func dpRow(it item, w int, sel bool) string {
	fixed := 0
	for _, c := range dpCols {
		fixed += c.w + 1
	}
	itemW := w - fixed - 3
	due := "FRI 13"
	if it.sprint == "43" {
		due = "FRI 27"
	}
	stage := strings.ToUpper(stages[it.stage])
	remark := strings.ToUpper(it.why)
	rs := plain
	switch {
	case it.blocked:
		stage, rs = "BLOCKED", dpRed
	case it.triage:
		stage, rs = "UNSORTED", dpAmber
	case it.stage == 3:
		rs = dpAmber
	}
	court := strings.ToUpper(it.court)
	if it.threads > 0 {
		court += fmt.Sprintf(" (%d)", it.threads)
	}
	cells := []string{due, fmt.Sprintf("#%d", it.ref), strings.ToUpper(it.title), stage, court, "—", strings.ToUpper(appsLabel(it)), remark}
	mark := "  "
	if sel {
		mark = dpAmber.Render("▸ ")
	}
	var b strings.Builder
	b.WriteString(mark)
	for i, col := range dpCols {
		cw := col.w
		if cw == 0 {
			cw = itemW
		}
		cell := pad(cells[i], cw)
		switch {
		case i == 3 || i == 7:
			cell = rs.Render(cell)
		case sel && i == 2:
			cell = bold.Render(cell)
		}
		b.WriteString(cell + " ")
	}
	return b.String()
}

func (departures) home(w, h, sel int) string {
	head := " " + dpAmber.Bold(true).Render("DEPARTURES") + "   " + bold.Render("DEMO-GL") + faint.Render("  DEMO-GH") +
		"      " + dpAmber.Render("SPRINT 42 · LAST DEPARTURE FRI 13 NOV · 2 WORKDAYS") + strings.Repeat(" ", 10) + faint.Render("UPDATED 40S AGO  ") + dpAmber.Bold(true).Render("● 2")
	var hdr strings.Builder
	hdr.WriteString("  ")
	fixed := 0
	for _, c := range dpCols {
		fixed += c.w + 1
	}
	for _, col := range dpCols {
		cw := col.w
		if cw == 0 {
			cw = w - fixed - 3
		}
		hdr.WriteString(faint.Render(pad(col.h, cw)) + " ")
	}
	rule := faint.Render(" " + strings.Repeat("─", w-2))
	l := []string{head, "", hdr.String(), rule}
	for i, it := range myWork {
		l = append(l, dpRow(it, w, i == sel), rule)
	}
	l = append(l, "", " "+bold.Render("LATER DEPARTURES · UNASSIGNED"), rule)
	for _, it := range sprintOpen {
		l = append(l, faint.Render(dpRow(it, w, false)), rule)
	}
	it := myWork[sel]
	l = append(l, "", " "+dpAmber.Render("PLATFORM INFORMATION")+"  "+bold.Render(fmt.Sprintf("#%d %s", it.ref, strings.ToUpper(it.title))))
	for _, t := range threads {
		l = append(l, "   "+dpAmber.Render("●")+" "+t.body+faint.Render("  — "+t.who+", "+t.ago))
	}
	body := fit(strings.Join(l, "\n"), w, h-1)
	return body + "\n " + keys("ENTER", "OPEN", "S", "START WORK", "W", "WORKSPACE", "I", "ANNOUNCEMENTS", "?", "KEYS")
}

func (departures) dialog(w int) string {
	var l []string
	l = append(l, dpAmber.Bold(true).Render("NOW BOARDING")+"  "+bold.Render("#3 RETRY FAILED WEBHOOK DELIVERIES")+faint.Render("  → PLATFORM API"), "")
	for _, p := range plan {
		l = append(l, " "+pad(strings.ToUpper(p.step), 11)+pad(p.detail, w-30)+dpAmber.Render(" WAITING"))
	}
	l = append(l, "", keys("ENTER", "DEPART", "E", "AMEND", "ESC", "CANCEL"))
	return lipgloss.NewStyle().Border(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color("#f0a020")).Padding(0, 1).Width(w).Render(strings.Join(l, "\n"))
}

func (departures) inbox(h int) string {
	w := 58
	var l []string
	l = append(l, dpAmber.Bold(true).Render("ANNOUNCEMENTS")+faint.Render(" · 2 NEW"), "")
	for _, n := range inbox {
		s := map[string]lipgloss.Style{"warn": dpAmber, "error": dpRed, "success": dpGreen, "info": plain}[n.sev]
		l = append(l, s.Render(pad(strings.ToUpper(n.kind), 18))+faint.Render(rpad(strings.ToUpper(n.ago), 10)), "  "+n.title, "")
	}
	l = append(l, keys("ENTER", "OPEN", "R", "READ", "ESC", "CLOSE"))
	return lipgloss.NewStyle().Border(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color("#f0a020")).Padding(0, 1).Width(w).Height(h).Render(strings.Join(l, "\n"))
}
