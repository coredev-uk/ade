package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/review"
)

// mrKey handles a key while the MR page is open. handled is false for the
// keys the dashboard owns everywhere (quit, help, notifications), which then
// fall through to its own handler. An open dialog takes every other key.
func (m *model) mrKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	p := m.mr
	ks := k.String()
	if ks == "ctrl+c" {
		return nil, false
	}
	if p.modal != nil {
		return m.modalKey(k), true
	}
	if m.help {
		switch ks {
		case "esc", "q", "?":
			return nil, false
		}
		return nil, true
	}
	switch ks {
	case "q", "?", "i", ":":
		return nil, false
	case "esc":
		if m.nc.dismissToasts() {
			return nil, true
		}
		if m.mrBack() {
			return nil, true
		}
		m.mr = nil
		return nil, true
	case "tab":
		return m.setTab((p.tab + 1) % tabCount), true
	case "shift+tab":
		return m.setTab((p.tab + tabCount - 1) % tabCount), true
	case "1", "2", "3", "4", "5", "6":
		return m.setTab(mrTab(ks[0] - '1')), true
	case "[", "]":
		if n := len(p.mrs); n > 1 {
			step := 1
			if ks == "[" {
				step = n - 1
			}
			return m.mrSelect((p.cur + step) % n), true
		}
		return nil, true
	case "U":
		return tea.Batch(m.mrReloadAll(), m.refresh()), true
	case "y":
		if p.tab != tabDiff && p.tab != tabReview || p.data == nil {
			return m.copyMR(), true
		}
	}
	if p.data == nil {
		return m.overviewKey(ks), true
	}
	d := p.data
	switch ks {
	case "a":
		return m.actApprove(), true
	case "R":
		return m.actReady(), true
	case "m":
		if d.busy != "" {
			m.notice(events.Warn, "Wait for "+d.busy+" to finish")
			return nil, true
		}
		p.modal = &mrModal{kind: modalMerge, prefix: review.PrefixForMR(m.app, m.cache, d.ref)}
		return nil, true
	case "S":
		m.beginSubmit()
		return nil, true
	case "A":
		return m.aiReviewKey(), true
	}
	switch p.tab {
	case tabThreads:
		return m.threadsKey(ks), true
	case tabDiff:
		return m.diffKey(ks), true
	case tabReview:
		return m.reviewKey(ks), true
	case tabCI:
		return m.ciKey(ks), true
	}
	return m.overviewKey(ks), true
}

// mrBack steps out of a sub-view (a narrow thread conversation, the diff pane)
// and reports whether there was one.
func (m *model) mrBack() bool {
	p := m.mr
	switch {
	case p.tab == tabThreads && p.thDetail:
		p.thDetail = false
	case p.tab == tabDiff && p.focusDiff:
		p.focusDiff = false
	default:
		return false
	}
	return true
}

func (m *model) copyMR() tea.Cmd {
	p := m.mr
	url := ""
	switch {
	case p.data != nil:
		url = p.data.mr.WebURL
	case p.item != nil:
		url = p.item.Issue.WebURL
	}
	if url == "" {
		return nil
	}
	e := events.New("ui.copied", events.Info, "copied "+url)
	e.Transient = true
	m.nc.push(e)
	return tea.SetClipboard(url)
}

// step moves a selection by d within n items.
func step(sel *int, d, n int) {
	*sel = clamp(*sel+d, 0, n-1)
}

func moveKey(ks string) (int, bool) {
	switch ks {
	case "j", "down":
		return 1, true
	case "k", "up":
		return -1, true
	case "g", "home":
		return -1 << 30, true
	case "G", "end":
		return 1 << 30, true
	case "pgdown", "ctrl+d":
		return 10, true
	case "pgup", "ctrl+u":
		return -10, true
	}
	return 0, false
}

func (m *model) overviewKey(ks string) tea.Cmd {
	if d, ok := moveKey(ks); ok {
		m.mr.ovTop = max(0, m.mr.ovTop+d)
	}
	if p := m.mr; p.tab == tabOverview && p.data != nil && m.aiOn() {
		switch ks {
		case "d":
			p.rawDesc = !p.rawDesc
		case "s":
			return m.summaryKey()
		}
	}
	return nil
}

func (m *model) threadsKey(ks string) tea.Cmd {
	p := m.mr
	list := p.data.threadList()
	if len(list) == 0 {
		return nil
	}
	if d, ok := moveKey(ks); ok {
		step(&p.thSel, d, len(list))
		return nil
	}
	sel := list[clamp(p.thSel, 0, len(list)-1)]
	switch ks {
	case "enter", "l", "right":
		p.thDetail = true
	case "h", "left":
		p.thDetail = false
	case "r":
		m.openReply(p.data, sel.d.ID, p.data.ai[aiKey{kind: aiReply, id: sel.d.ID}])
	case "d", "s":
		return m.threadAIKey(ks, sel)
	case "x":
		if !sel.d.Resolvable {
			m.notice(events.Info, "A plain comment has no resolved state")
			return nil
		}
		return m.actResolve(*sel.d)
	}
	return nil
}

func (m *model) diffKey(ks string) tea.Cmd {
	p := m.mr
	d := p.data
	if d.diffL.st != loadOK || len(d.diff.Files) == 0 {
		return nil
	}
	nf := len(d.diff.Files)
	if !p.focusDiff {
		if dl, ok := moveKey(ks); ok {
			step(&p.fileSel, dl, nf)
			p.lineCur = 0
			return nil
		}
		switch ks {
		case "enter", "l", "right":
			p.focusDiff = true
		case "D":
			return m.openPager()
		}
		return nil
	}
	f, _, _ := m.cursorLine()
	nl := len(review.Lines(f))
	if dl, ok := moveKey(ks); ok {
		step(&p.lineCur, dl, nl)
		return nil
	}
	switch ks {
	case "h", "left":
		p.focusDiff = false
	case "J":
		step(&p.fileSel, 1, nf)
		p.lineCur = 0
	case "K":
		step(&p.fileSel, -1, nf)
		p.lineCur = 0
	case "c":
		m.beginComment()
	case "D":
		return m.openPager()
	case "y", "n", "e":
		return m.inlineTriage(ks)
	}
	return nil
}

// inlineTriage applies y, n or e to the draft under the cursor: the first
// pending agent draft for y and n, the first draft for e.
func (m *model) inlineTriage(ks string) tea.Cmd {
	at := m.draftsAtCursor()
	pick := func(pending bool) (review.Draft, bool) {
		for _, dr := range at {
			if !pending || dr.Source == review.SourceAgent && dr.State == review.StatePending {
				return dr, true
			}
		}
		return review.Draft{}, false
	}
	switch ks {
	case "y":
		if dr, ok := pick(true); ok {
			m.accept(dr)
		}
	case "n":
		if dr, ok := pick(true); ok {
			m.mr.modal = &mrModal{kind: modalDeny, draftID: dr.ID, ed: newEditor("")}
		}
	case "e":
		if dr, ok := pick(false); ok {
			m.beginEdit(dr)
		}
	}
	return nil
}

func (m *model) reviewKey(ks string) tea.Cmd {
	p := m.mr
	d := p.data
	list := triageOrder(d.drafts)
	if len(list) == 0 {
		return nil
	}
	if dl, ok := moveKey(ks); ok {
		step(&p.revSel, dl, len(list))
		return nil
	}
	sel := list[clamp(p.revSel, 0, len(list)-1)]
	switch ks {
	case "y":
		m.accept(sel)
	case "Y":
		m.acceptAll()
	case "n":
		p.modal = &mrModal{kind: modalDeny, draftID: sel.ID, ed: newEditor("")}
	case "e":
		m.beginEdit(sel)
	case "s":
		m.cycleSeverity(sel)
	case "d":
		m.removeDraft(sel)
		step(&p.revSel, 0, len(d.drafts))
	case "enter":
		m.jumpToDraft(sel)
	}
	return nil
}

// jumpToDraft shows a draft on its diff line.
func (m *model) jumpToDraft(dr review.Draft) {
	p := m.mr
	d := p.data
	if dr.Overview() || d.diffL.st != loadOK {
		return
	}
	for fi, f := range d.diff.Files {
		if !draftInFile(dr, f) {
			continue
		}
		for li, l := range review.Lines(f) {
			if draftOnLine(dr, l) {
				p.tab, p.fileSel, p.lineCur, p.focusDiff = tabDiff, fi, li, true
				return
			}
		}
	}
}

func (m *model) ciKey(ks string) tea.Cmd {
	p := m.mr
	d := p.data
	if dl, ok := moveKey(ks); ok {
		step(&p.ciSel, dl, len(d.jobs))
		return nil
	}
	switch ks {
	case "l", "enter":
		if len(d.jobs) > 0 {
			return m.actLog(d.jobs[clamp(p.ciSel, 0, len(d.jobs)-1)])
		}
	case "r":
		return m.actRetry()
	}
	return nil
}

// --- footer and help -------------------------------------------------------------

func (m model) mrFooter(w int) string {
	p := m.mr
	narrow := w < twoPaneMinW
	if p.modal != nil {
		return " " + m.modalHint(p.modal)
	}
	if m.help {
		return " " + keys("esc", "close", "?", "close")
	}
	var tabKeys []string
	switch {
	case p.data == nil:
		tabKeys = []string{"j/k", "scroll"}
	case p.tab == tabThreads:
		tabKeys = []string{"j/k", "move"}
		if narrow {
			tabKeys = append(tabKeys, "enter", "open")
		}
		tabKeys = append(tabKeys, "r", "reply")
		if m.aiOn() {
			tabKeys = append(tabKeys, "s", "AI summary", "d", "AI reply")
		}
		tabKeys = append(tabKeys, "x", "resolve/reopen")
	case p.tab == tabDiff && p.focusDiff:
		tabKeys = []string{"j/k", "line", "c", "comment", "J/K", "file", "D", "delta", "h", "files"}
	case p.tab == tabDiff:
		tabKeys = []string{"j/k", "file", "enter", "open", "D", "delta"}
	case p.tab == tabReview:
		tabKeys = []string{"j/k", "move", "y", "accept", "n", "deny", "e", "edit", "s", "severity"}
		if narrow {
			tabKeys = tabKeys[:6]
		}
	case p.tab == tabCI:
		tabKeys = []string{"j/k", "job", "l", "log", "r", "retry"}
	case p.tab == tabHistory:
		tabKeys = []string{"U", "reload"}
	default:
		tabKeys = []string{"j/k", "scroll"}
		if m.aiOn() {
			tabKeys = append(tabKeys, "d", "summary/raw", "s", "new summary")
		}
	}
	act := []string{"a", "approve", "R", "ready/draft", "m", "merge", "S", "submit review"}
	if m.aiOn() {
		act = append(act, "A", "agent review")
	}
	if p.item != nil {
		act = append(act, "P", "agents")
	}
	if p.data == nil {
		act = nil
		if p.item != nil {
			act = []string{"P", "agents"}
		}
	}
	return fitKeys(w, []string{"esc", "back", "?", "keys"}, tabKeys, []string{"tab", "tabs"}, act)
}

// fitKeys joins key hints and drops whole hints, last group first, until the
// line fits: what the tab does outranks the page-wide actions. keep (the way
// out and the help) always stays, at the end.
func fitKeys(w int, keep []string, groups ...[]string) string {
	groups = append([][]string(nil), groups...)
	for {
		var all []string
		for _, g := range groups {
			all = append(all, g...)
		}
		line := " " + keys(append(all, keep...)...)
		if lipgloss.Width(line) < w {
			return line
		}
		trimmed := false
		for i := len(groups) - 1; i >= 0; i-- {
			if len(groups[i]) > 0 {
				groups[i] = groups[i][:len(groups[i])-2]
				trimmed = true
				break
			}
		}
		if !trimmed {
			return line
		}
	}
}

// mrHelpRows lists the MR page's keys. The AI rows exist only while AI is on.
func mrHelpRows(ai bool) [][2]string {
	rows := [][2]string{
		{"tab", "next tab; 1-6 jump to one"},
		{"[ ]", "another MR of the item"},
		{"esc", "back one step, then Home"},
		{"j k g G", "move; ctrl+d / ctrl+u page"},
		{"r", "reply (Threads), retry (CI)"},
		{"x", "resolve or reopen the thread"},
		{"enter", "open thread, file or job log"},
		{"c", "comment on the diff line"},
		{"D", "Diff: open delta (or your pager)"},
		{"y n e", "accept, deny, edit a draft"},
		{"Y s d", "accept all, severity, delete"},
		{"S", "submit the review as one batch"},
		{"a R m", "approve, ready/draft, merge"},
		{"P", "agent menu: plan, address comments"},
		{"l", "CI: job log tail"},
		{"U", "reload from the forge"},
	}
	if ai {
		rows = slices.Insert(rows, 8,
			[2]string{"A", "agent review pass, drafts in Review"},
			[2]string{"d s", "Overview: raw/summary, new summary"},
			[2]string{"d s", "Threads: AI draft reply, summary"})
	}
	return rows
}

// mrHelpView is terse on purpose: each row fits one line even at 60 columns,
// and rows that do not fit the height are left out.
func mrHelpView(w, h int, g glyphs, ai bool) string {
	rows := mrHelpRows(ai)
	l := []string{stBold.Render("Merge request page"), ""}
	for _, r := range rows[:min(len(rows), max(1, h-6))] {
		l = append(l, stRule.Render(g.Vertical)+" "+stBold.Render(pad(g, r[0], 8))+r[1])
	}
	return lipgloss.NewStyle().Border(g.Border).BorderForeground(lipgloss.Color(colRule)).
		Padding(0, 1).Width(w).Render(strings.Join(l, "\n"))
}
