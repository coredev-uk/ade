package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Interchange: every work item is a line; stages are stations; the current
// stage is the train. One line ink per app; all other text is terminal ink.
type interchange struct{}

var (
	icLine = map[string]lipgloss.Style{"api": c("#e0453a"), "web": c("#3b7dd8")}
	icWarn = c("#e8a33d")
	icOK   = c("#3fa66b")
	icBad  = c("#e0453a")
)

func (interchange) name() string { return "Interchange" }

func icInk(it item) lipgloss.Style {
	if len(it.apps) == 0 {
		return faint
	}
	return icLine[it.apps[0]]
}

// track draws the stage line: done stations filled, the current one as the
// train (an interchange ring when the item spans repos), the rest hollow.
func icTrack(it item) string {
	if it.triage {
		return faint.Render(pad("? untriaged", 19))
	}
	ink := icInk(it)
	var b strings.Builder
	for i := range stages {
		if i > 0 {
			if i <= it.stage {
				b.WriteString(ink.Render("━━"))
			} else {
				b.WriteString(faint.Render("──"))
			}
		}
		switch {
		case i < it.stage:
			b.WriteString(ink.Render("●"))
		case i == it.stage && it.blocked:
			b.WriteString(icWarn.Bold(true).Render("◉"))
		case i == it.stage && len(it.apps) > 1:
			b.WriteString(bold.Render("◎"))
		case i == it.stage:
			b.WriteString(ink.Bold(true).Render("◉"))
		default:
			b.WriteString(faint.Render("○"))
		}
	}
	return b.String()
}

func icCourt(it item) string {
	switch it.court {
	case "you":
		return bold.Render("you") + faint.Render(" · ") + it.why
	case "blocked":
		return icWarn.Render("blocked") + faint.Render(" · "+it.why)
	default:
		return faint.Render(it.court)
	}
}

func icRow(it item, selected bool, titleW int) string {
	mark := "  "
	if selected {
		mark = bold.Render("▸ ")
	}
	ref := pad(fmt.Sprintf("#%d", it.ref), 5)
	title := pad(it.title, titleW)
	if selected {
		title = bold.Render(title)
	}
	return mark + faint.Render(ref) + title + "  " + icTrack(it) + "  " + icCourt(it)
}

func icHeader(w int) string {
	left := bold.Render("ADE") + "   " + bold.Underline(true).Render("demo-gl") + "  " + faint.Render("demo-gh")
	mid := "Sprint 42 " + faint.Render("· ends Fri 13 Nov · ") + icWarn.Render("2 workdays left") + faint.Render(" · next Sprint 43 starts Mon 16")
	right := faint.Render("⟳ updated 40s ago   ") + bold.Render("● 2")
	gap := w - lipgloss.Width(left) - lipgloss.Width(mid) - lipgloss.Width(right) - 2
	return " " + left + strings.Repeat(" ", max(2, gap/2)) + mid + strings.Repeat(" ", max(2, gap-gap/2)) + right
}

func icSection(title, guide string, w int) string {
	t := bold.Render(title) + " "
	g := " " + faint.Render(guide)
	return " " + t + faint.Render(strings.Repeat("─", max(1, w-lipgloss.Width(t)-lipgloss.Width(g)-2))) + g
}

func (interchange) home(w, h, sel int) string {
	listW := w * 64 / 100
	titleW := listW - 2 - 5 - 2 - 19 - 2 - 26
	var l []string
	l = append(l, icSection("My work", "#1 – #11 · 7 items · sorted by court", listW))
	legend := strings.Repeat(" ", 2+5+titleW+2)
	for _, s := range []string{"as", "st", "br", "dr", "ci", "rv", "mg"} {
		legend += pad(s, 3)
	}
	l = append(l, faint.Render(legend))
	for i, it := range myWork {
		l = append(l, icRow(it, i == sel, titleW))
	}
	l = append(l, "", icSection("Reviews requested", "none", listW), faint.Render("   Nothing is waiting on your review."))
	l = append(l, "", icSection("Sprint 42 · open", "#6 · 1 unassigned", listW))
	l = append(l, icRow(sprintOpen[0], false, titleW))
	l = append(l, "", icSection("Sprint 43 · next", "#7 · starts Mon 16", listW))
	l = append(l, icRow(sprintOpen[1], false, titleW))
	left := fit(strings.Join(l, "\n"), listW, h-3)

	right := fit(icPreview(myWork[sel], w-listW-3), w-listW-3, h-3)
	body := columns(faint.Render(" │ "), h-3, left, right)
	foot := " " + keys("enter", "open", "s", "start work", "w", "workspace", "a", "agent", "i", "inbox", "/", "filter", "?", "keys")
	return icHeader(w) + "\n\n" + body + "\n" + foot
}

func keys(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, bold.Render(kv[i])+" "+faint.Render(kv[i+1]))
	}
	return strings.Join(parts, "   ")
}

func icPreview(it item, w int) string {
	ink := icInk(it)
	var l []string
	l = append(l, bold.Render(fmt.Sprintf("tracker#%d  %s", it.ref, it.title)))
	meta := []string{it.typ, appsLabel(it), "Sprint " + it.sprint, "label: in review " + icOK.Render("✓")}
	l = append(l, faint.Render(strings.Join(meta, " · ")), "")
	when := []string{"9 Nov", "9 Nov", "9 Nov", "10 Nov", "", "11 Nov", ""}
	what := []string{"", "api#1 (child)", "1-rate-limit-the-export-endpoint", "api!1", "no pipeline configured", "ready · 0 approvals · 2 unresolved", ""}
	for i, s := range stages {
		g := faint.Render("○")
		switch {
		case i < it.stage:
			g = ink.Render("●")
		case i == it.stage:
			g = ink.Bold(true).Render("◉")
		}
		name := pad(s, 10)
		if i == it.stage {
			name = bold.Render(name)
		}
		l = append(l, " "+g+"  "+name+faint.Render(rpad(when[i], 7))+"   "+what[i])
		if i < len(stages)-1 {
			conn := faint.Render("│")
			if i < it.stage {
				conn = ink.Render("┃")
			}
			l = append(l, " "+conn)
		}
	}
	l = append(l, "", bold.Render("Threads")+faint.Render(" · 2 unresolved on api!1"))
	for _, t := range threads {
		l = append(l, " "+icWarn.Render("●")+" "+pad(t.body, w-4))
		l = append(l, "   "+faint.Render(t.who+" · "+t.ago))
	}
	l = append(l, "", bold.Render("Tasks")+faint.Render(" · 1 of 4"))
	l = append(l, " "+icOK.Render("✓")+" Agree limits with support", " "+faint.Render("○")+" Token-bucket limiter in api", " "+faint.Render("○")+" 429 response with Retry-After")
	return strings.Join(l, "\n")
}

func (interchange) dialog(w int) string {
	ink := icLine["api"]
	inner := w - 4
	var l []string
	l = append(l, bold.Render("Start work")+faint.Render("  tracker#3 · Retry failed webhook deliveries"))
	l = append(l, faint.Render("Routed to ")+ink.Render("api")+faint.Render(" by app::api · 8 steps, nothing has been written yet"), "")
	for i, p := range plan {
		g := ink.Render("○")
		if i == 0 {
			g = ink.Bold(true).Render("◉")
		}
		l = append(l, " "+g+"  "+bold.Render(pad(p.step, 12))+pad(p.detail, inner-17))
		if i < len(plan)-1 {
			l = append(l, " "+ink.Render("┃"))
		}
	}
	l = append(l, "", " "+faint.Render("[ ]")+" Open the workspace with omp ready instead of the editor")
	l = append(l, "", keys("enter", "start", "e", "edit a step", "tab", "toggle", "esc", "cancel"))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#e0453a")).Padding(0, 1).Width(w).Render(strings.Join(l, "\n"))
}

func (interchange) inbox(h int) string {
	w := 58
	var l []string
	l = append(l, bold.Render("Inbox")+faint.Render(" · 2 unread"), "")
	for i, n := range inbox {
		g := map[string]string{"warn": icWarn.Render("▲"), "error": icBad.Render("✕"), "success": icOK.Render("✓"), "info": faint.Render("●")}[n.sev]
		title := pad(n.title, w-12)
		if i < 2 {
			title = bold.Render(title)
		}
		l = append(l, g+" "+title+faint.Render(rpad(n.ago, 8)))
		l = append(l, "  "+faint.Render(n.kind), "")
	}
	l = append(l, keys("enter", "open", "r", "mark read", "R", "all read", "esc", "close"))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#555555")).Padding(0, 1).Width(w).Height(h).Render(strings.Join(l, "\n"))
}
