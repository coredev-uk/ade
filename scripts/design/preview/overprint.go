package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Course Overprint: everything sits in quiet terrain colours; the items waiting
// on you are overprinted in course purple as numbered controls, in order.
type overprint struct{}

var (
	opPurple  = c("#c04fd0")
	opTerrain = c("#a0703c")
	opMarsh   = c("#3a8fd0")
	controls  = []string{"①", "②", "③", "④", "⑤", "⑥", "⑦", "⑧", "⑨"}
)

func (overprint) name() string { return "Course Overprint" }

func (overprint) home(w, h, sel int) string {
	head := " " + bold.Render("ADE") + "  demo-gl" + faint.Render(" · demo-gh") + "    " + opTerrain.Render("Sprint 42 · ends Fri 13 Nov · 2 workdays left") + strings.Repeat(" ", 20) + faint.Render("⟳ 40s  ") + opPurple.Bold(true).Render("● 2")
	listW := w * 62 / 100
	var l []string
	course := []item{}
	terrain := []item{}
	for _, it := range myWork {
		if it.court == "you" && it.sprint == "42" {
			course = append(course, it)
		} else {
			terrain = append(terrain, it)
		}
	}
	l = append(l, " "+opPurple.Bold(true).Render(fmt.Sprintf("Your course · %d controls", len(course)))+faint.Render("  in the order to tackle them"), "")
	for i, it := range course {
		t := pad(fmt.Sprintf("#%d %s", it.ref, it.title), listW-36)
		if i == sel {
			t = bold.Underline(true).Render(t)
		}
		l = append(l, " "+opPurple.Bold(true).Render(controls[i])+"  "+t+"  "+opPurple.Render(it.why))
		if i < len(course)-1 {
			l = append(l, " "+opPurple.Render("│"))
		}
	}
	l = append(l, "", " "+opTerrain.Render("Terrain")+faint.Render("  everything else in reach"), "")
	for _, it := range append(terrain, sprintOpen...) {
		l = append(l, "    "+opMarsh.Render(pad(fmt.Sprintf("#%d", it.ref), 5))+opTerrain.Render(pad(it.title, listW-40))+"  "+faint.Render(it.court+" · Sprint "+it.sprint))
	}
	left := fit(strings.Join(l, "\n"), listW, h-3)

	it := myWork[sel]
	rw := w - listW - 3
	d := []string{opPurple.Bold(true).Render(controls[min(sel, len(controls)-1)]) + " " + bold.Render(fmt.Sprintf("Control description · tracker#%d", it.ref)), ""}
	rows := [][2]string{{"stage", stages[it.stage]}, {"merge request", it.mr}, {"threads", fmt.Sprintf("%d unresolved", it.threads)}, {"pipeline", "none configured"}, {"label", "workflow::in review ✓"}, {"route", "api (app::api)"}, {"moved", it.moved}}
	for _, r := range rows {
		d = append(d, opTerrain.Render(pad(r[0], 15))+faint.Render("│ ")+r[1])
	}
	d = append(d, "", bold.Render("Threads"))
	for _, t := range threads {
		d = append(d, " "+opPurple.Render("◇")+" "+pad(t.body, rw-4))
	}
	right := fit(strings.Join(d, "\n"), rw, h-3)
	body := columns(faint.Render(" │ "), h-3, left, right)
	return head + "\n\n" + body + "\n " + keys("enter", "open", "s", "start leg", "w", "workspace", "i", "notices", "?", "keys")
}

func (overprint) dialog(w int) string {
	var l []string
	l = append(l, opPurple.Bold(true).Render("New leg")+faint.Render("  tracker#3 · Retry failed webhook deliveries → api"), "")
	for i, p := range plan {
		l = append(l, " "+opPurple.Render(controls[i])+"  "+bold.Render(pad(p.step, 12))+opTerrain.Render(pad(p.detail, w-21)))
		if i < len(plan)-1 {
			l = append(l, " "+opPurple.Render("│"))
		}
	}
	l = append(l, "", keys("enter", "run the leg", "e", "edit control", "esc", "cancel"))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#c04fd0")).Padding(0, 1).Width(w).Render(strings.Join(l, "\n"))
}

func (overprint) inbox(h int) string {
	w := 58
	var l []string
	l = append(l, opPurple.Bold(true).Render("Notices")+faint.Render(" · 2 unread"), "")
	for _, n := range inbox {
		l = append(l, opTerrain.Render(pad(n.kind, 18))+faint.Render(rpad(n.ago, 10)), "  "+n.title, "")
	}
	l = append(l, keys("enter", "open", "r", "read", "esc", "close"))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#a0703c")).Padding(0, 1).Width(w).Height(h).Render(strings.Join(l, "\n"))
}
