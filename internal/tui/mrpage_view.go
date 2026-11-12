package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/forge"
)

// --- frame -----------------------------------------------------------------------

// mrBody is the page: four head lines (title, status strip, tabs, the tab's
// section rule), then the tab's content.
func (m model) mrBody(w, h int) string {
	head := m.mrHead(w)
	content := m.mrContent(w, h-len(head))
	return fit(m.g, strings.Join(head, "\n")+"\n"+content, w, h)
}

func (m model) mrNarrow(w int) bool { return w < twoPaneMinW }

func (m model) mrContent(w, h int) string {
	p := m.mr
	if p.data == nil && p.tab != tabOverview && p.tab != tabHistory {
		return fit(m.g, "   "+stFaint.Render("This item has no merge request yet. Threads, Diff, Review and CI appear once one exists."), w, h)
	}
	switch p.tab {
	case tabThreads:
		return m.mrThreads(w, h)
	case tabDiff:
		return m.mrDiff(w, h)
	case tabReview:
		return m.mrReview(w, h)
	case tabCI:
		return m.mrCI(w, h)
	case tabHistory:
		if ph, ok := m.wait(p.histL, "the history", w, h); !ok {
			return ph
		}
		return historyView(m.g, p.histEntries, w, h)
	}
	return m.mrOverview(w, h)
}

// mrCrumb names the page in the header: the MR, else the item.
func (m model) mrCrumb() string {
	p := m.mr
	tracker := m.trackerOf(p.project)
	switch {
	case p.data != nil:
		return shortRef(p.data.ref, tracker)
	case p.item != nil:
		return shortRef(p.item.Ref, tracker)
	}
	return ""
}

// trackerOf is the tracker project of an ADE project, to shorten refs.
func (m model) trackerOf(project string) string {
	for i := range m.results {
		if m.results[i].Project == project {
			return m.results[i].Tracker
		}
	}
	return ""
}

func (m model) mrHead(w int) []string {
	p := m.mr
	tracker := m.trackerOf(p.project)
	var title string
	switch {
	case p.data != nil:
		ref := shortRef(p.data.ref, tracker)
		crumb := ""
		if p.item != nil {
			crumb = stFaint.Render(shortRef(p.item.Ref, tracker) + m.g.T(" › "))
		}
		count := ""
		if len(p.mrs) > 1 {
			count = stFaint.Render(fmt.Sprintf(m.g.T("  (%d of %d · [ ] switch)"), p.cur+1, len(p.mrs)))
		}
		title = " " + crumb + stBold.Render(ref+"  "+p.data.mr.Title) + count
	case p.item != nil:
		title = " " + stBold.Render(shortRef(p.item.Ref, tracker)+"  "+p.item.Issue.Title)
	}
	return []string{ansi.Truncate(title, w-1, m.g.Ell), m.mrStrip(w), m.mrTabs(w), m.mrRule(w)}
}

// mrStrip is the status line: what is in flight, then the MR's state.
func (m model) mrStrip(w int) string {
	p := m.mr
	d := p.data
	if d == nil {
		return " " + stFaint.Render("no merge request yet")
	}
	mr := d.mr
	g := m.g
	var seg []string
	switch {
	case d.busy != "":
		seg = append(seg, stBold.Render(spinnerFrame(g, p.spin)+" "+d.busy+m.g.Ell))
	case d.ai[aiKey{kind: aiReview}] != nil && d.ai[aiKey{kind: aiReview}].l.busy():
		seg = append(seg, stBold.Render(spinnerFrame(g, p.spin)+m.g.T(" agent review…")))
	case d.mrL.busy() && d.loadedAt.IsZero(): // nothing read yet: the sync summary stands in
		seg = append(seg, stFaint.Render(spinnerFrame(g, p.spin)+" reading from the forge"))
	case d.mrL.busy():
		seg = append(seg, stFaint.Render(spinnerFrame(g, p.spin)+" refreshing"))
	case d.mrL.st == loadFailed:
		seg = append(seg, stBad.Render(g.Bad+" could not refresh"))
	}
	switch {
	case mr.State == "merged":
		seg = append(seg, stOK.Render(g.OK+" merged"))
	case mr.State == "closed":
		seg = append(seg, stFaint.Render("closed"))
	case mr.Draft:
		seg = append(seg, stWarn.Render("Draft"))
	default:
		seg = append(seg, "Ready")
	}
	seg = append(seg, ciText(g, mr.Pipeline), approvalsText(mr))
	if mr.Unresolved > 0 {
		seg = append(seg, stWarn.Render(fmt.Sprintf("%d unresolved", mr.Unresolved)))
	} else {
		seg = append(seg, stFaint.Render("no unresolved threads"))
	}
	if mr.State == "opened" {
		v := mergeVerdict(mr)
		switch {
		case v.ok:
			seg = append(seg, stOK.Render(g.OK+" mergeable"))
		default:
			seg = append(seg, stWarn.Render(g.Warn+" "+v.why))
		}
	}
	sep := stFaint.Render(" " + g.Sep + " ")
	for len(seg) > 2 && lipgloss.Width(" "+strings.Join(seg, sep)) > w {
		seg = seg[:len(seg)-1] // the verdict goes first: it is also in the merge dialog
	}
	return " " + ansi.Truncate(strings.Join(seg, sep), w-2, m.g.Ell)
}

func (m model) mrTabs(w int) string {
	p := m.mr
	d := p.data
	var parts []string
	for t := range tabCount {
		name := t.title()
		badge := ""
		if d != nil {
			switch t {
			case tabThreads:
				if n := len(d.byGroup(groupOpen)); n > 0 {
					badge = stWarn.Render(fmt.Sprintf(" %d", n))
				}
			case tabDiff:
				if d.diffL.st == loadOK {
					badge = stFaint.Render(fmt.Sprintf(" %d", len(d.diff.Files)))
				}
			case tabReview:
				if n := len(d.drafts); n > 0 {
					badge = stBold.Render(fmt.Sprintf(" %d", n))
				}
			case tabCI:
				if d.mr.Pipeline != nil {
					gl, _ := ciGlyph(m.g, d.mr.Pipeline.Status)
					badge = " " + gl
				}
			}
		}
		if t == p.tab {
			parts = append(parts, stBold.Render("["+name)+badge+stBold.Render("]"))
		} else {
			parts = append(parts, " "+stFaint.Render(name)+badge+" ")
		}
	}
	return " " + ansi.Truncate(strings.Join(parts, ""), w-2, m.g.Ell)
}

func (m model) mrRule(w int) string {
	p := m.mr
	d := p.data
	guide := ""
	if d != nil {
		switch p.tab {
		case tabOverview:
			guide = d.mr.Author.Display() + " " + m.g.Sep + " " + d.mr.SourceBranch + m.g.T(" → ") + d.mr.TargetBranch
		case tabThreads:
			if d.threadsL.st == loadOK || len(d.threads) > 0 { // not "0 unresolved" while loading or failed
				guide = fmt.Sprintf("%d unresolved %s %d resolved", len(d.byGroup(groupOpen)), m.g.Sep, len(d.byGroup(groupDone)))
			}
		case tabDiff:
			guide = m.diffGuide()
		case tabReview:
			guide = m.reviewGuide()
		case tabCI:
			if pl := d.mr.Pipeline; pl != nil {
				guide = fmt.Sprintf("pipeline #%d", pl.ID)
				if d.jobsL.st == loadOK { // no job count until the jobs are read: "0 jobs" would be a claim
					guide += fmt.Sprintf(" %s %d jobs", m.g.Sep, len(d.jobs))
				}
			} else {
				guide = "no pipeline"
			}
		}
	}
	if p.tab == tabHistory && p.histL.st == loadOK {
		guide = fmt.Sprintf("%d recorded %s", len(p.histEntries), plural(len(p.histEntries), "action"))
	}
	return section(p.tab.title(), ansi.Truncate(guide, max(10, w-lipgloss.Width(p.tab.title())-6), m.g.Ell), w, m.g)
}

// wait is the placeholder while a tab's data loads or failed to load; ok is
// true when the tab can draw.
func (m model) wait(l load, what string, w, h int) (string, bool) {
	switch l.st {
	case loadOK:
		return "", true
	case loadFailed:
		return fit(m.g, "   "+stBad.Render(m.g.Bad+" could not read "+what+": "+shortCause(l.err))+"\n   "+stFaint.Render("U tries again"), w, h), false
	}
	return fit(m.g, "   "+stFaint.Render(spinnerFrame(m.g, m.mr.spin)+" reading "+what+m.g.Ell), w, h), false
}

// --- status words --------------------------------------------------------------

// ciGlyph maps a pipeline or job status to its glyph (coloured) and word.
func ciGlyph(g glyphs, status string) (glyph, word string) {
	switch status {
	case "success":
		return stOK.Render(g.OK), "passed"
	case "failed":
		return stBad.Render(g.Bad), "failed"
	case "running":
		return stWarn.Render(g.Sync), "running"
	case "pending", "created", "waiting_for_resource", "preparing", "scheduled":
		return stFaint.Render(g.Ahead), "pending"
	case "manual":
		return g.Selected, "manual"
	case "canceled", "canceling":
		return stFaint.Render(g.Bad), "canceled"
	case "skipped":
		return stFaint.Render(g.ToCome), "skipped"
	}
	return stFaint.Render(g.Ahead), status
}

func ciText(g glyphs, p *forge.Pipeline) string {
	if p == nil {
		return stFaint.Render("no CI")
	}
	gl, word := ciGlyph(g, p.Status)
	return gl + " CI " + word
}

// --- overview ------------------------------------------------------------------

func (m model) mrOverview(w, h int) string {
	p := m.mr
	d := p.data
	g := m.g
	var l []string
	if it := p.item; it != nil {
		l = append(l, subRule("Work item", it.Ref.String(), w, g))
		for _, ln := range strings.Split(m.itemDetail(it, w-4, false), "\n") {
			l = append(l, "   "+ln)
		}
		l = append(l, "")
	}
	if d == nil {
		l = append(l, "   "+stFaint.Render("No merge request yet. Start work creates the branch and a draft MR (phase 3)."))
		return m.scrolled(l, w, h)
	}
	mr := d.mr
	l = append(l, subRule("Merge request", mr.Ref.String(), w, g))
	kv := func(k, v string) {
		if v != "" {
			l = append(l, "   "+stFaint.Render(pad(m.g, k, 10))+ansi.Truncate(v, w-15, m.g.Ell))
		}
	}
	kv("state", mr.State+map[bool]string{true: " " + g.Sep + " draft", false: ""}[mr.Draft])
	kv("author", mr.Author.Display())
	kv("branch", mr.SourceBranch+m.g.T(" → ")+mr.TargetBranch)
	var rev []string
	for _, u := range mr.Reviewers {
		rev = append(rev, u.Display())
	}
	kv("reviewers", strings.Join(rev, ", "))
	kv("approvals", approvalsText(mr))
	kv("pipeline", ciText(g, mr.Pipeline))
	kv("threads", fmt.Sprintf("%d unresolved %s %d resolved", mr.Unresolved, g.Sep, mr.Resolved))
	if mr.State == "opened" {
		if v := mergeVerdict(mr); v.ok {
			kv("merge", stOK.Render(g.OK+" mergeable"))
		} else {
			kv("merge", stWarn.Render(g.Warn+" "+v.why))
		}
	}
	desc := strings.TrimSpace(mr.Description)
	sum := d.ai[aiKey{kind: aiSummary}]
	summarised := m.aiOn() && sum != nil && sum.l.st == loadOK
	if m.aiOn() && !summarised && !p.rawDesc { // running, waiting for a yes, or failed: say so above the description
		if blk := m.aiBlock("Summary", sum, w); len(blk) > 0 {
			l = append(l, "")
			l = append(l, blk...)
		}
	}
	switch {
	case summarised && !p.rawDesc:
		l = append(l, "")
		l = append(l, m.aiBlock("Summary", sum, w)...)
		l = append(l, "   "+stFaint.Render(m.g.T("d shows the raw description · s asks again (a new call, the cache is skipped)")))
	case desc != "":
		guide := ""
		if summarised {
			guide = "d shows the AI summary"
		}
		l = append(l, "", subRule("Description", guide, w, g))
		for _, ln := range m.mdLines(desc, w-6) {
			l = append(l, "   "+ln)
		}
	case d.mrL.busy() && d.loadedAt.IsZero():
		l = append(l, "", subRule("Description", "", w, g), "   "+stFaint.Render(spinnerFrame(g, p.spin)+m.g.T(" reading the description…")))
	}
	return m.scrolled(l, w, h)
}

// scrolled shows lines from the page's overview scroll position.
func (m model) scrolled(l []string, w, h int) string {
	p := m.mr
	p.ovTop = clamp(p.ovTop, 0, len(l)-h)
	return fit(m.g, strings.Join(l[p.ovTop:], "\n"), w, h)
}

// --- threads -------------------------------------------------------------------

type threadGroup int

const (
	groupOpen    threadGroup = iota // resolvable and unresolved
	groupComment                    // not resolvable: plain comments
	groupDone                       // resolved
)

func (g threadGroup) title() string {
	return [...]string{"Unresolved", "Comments", "Resolved"}[g]
}

// thread is a discussion with its group, for the Threads tab.
type thread struct {
	d     *forge.Discussion
	group threadGroup
}

func (t thread) first() forge.Note {
	for _, n := range t.d.Notes {
		if !n.System {
			return n
		}
	}
	if len(t.d.Notes) > 0 {
		return t.d.Notes[0]
	}
	return forge.Note{}
}

func (t thread) path() string {
	if pos := t.d.Position; pos != nil {
		return pos.Path
	}
	return ""
}

func (t thread) line() int {
	if pos := t.d.Position; pos != nil {
		if pos.NewLine > 0 {
			return pos.NewLine
		}
		return pos.OldLine
	}
	return 0
}

func (t thread) where() string {
	if t.path() == "" {
		return "general"
	}
	return fmt.Sprintf("%s:%d", t.path(), t.line())
}

// threadList orders the discussions: unresolved first, then plain comments,
// then resolved; within a group by file and line, general comments last.
// System notes ("added 1 commit") are not threads.
func (d *mrData) threadList() []thread {
	var out []thread
	for i := range d.threads {
		disc := &d.threads[i]
		user := false
		for _, n := range disc.Notes {
			user = user || !n.System
		}
		if !user {
			continue
		}
		grp := groupComment
		switch {
		case disc.Resolvable && disc.Resolved:
			grp = groupDone
		case disc.Resolvable:
			grp = groupOpen
		}
		out = append(out, thread{disc, grp})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.group != b.group {
			return a.group < b.group
		}
		if (a.path() == "") != (b.path() == "") {
			return a.path() != ""
		}
		if a.path() != b.path() {
			return a.path() < b.path()
		}
		return a.line() < b.line()
	})
	return out
}

func (d *mrData) byGroup(g threadGroup) []thread {
	var out []thread
	for _, t := range d.threadList() {
		if t.group == g {
			out = append(out, t)
		}
	}
	return out
}

func (m model) mrThreads(w, h int) string {
	p := m.mr
	d := p.data
	if ph, ok := m.wait(d.threadsL, "threads", w, h); !ok && len(d.threads) == 0 {
		return ph
	}
	list := d.threadList()
	if len(list) == 0 {
		return fit(m.g, "   "+stFaint.Render("No threads on this merge request. Line comments from your review appear here once submitted."), w, h)
	}
	sel := clamp(p.thSel, 0, len(list)-1)
	if m.mrNarrow(w) {
		if p.thDetail {
			return fit(m.g, indentBlock(m.threadDetail(list[sel], w-2, h)), w, h)
		}
		return m.threadLines(list, sel, w, h)
	}
	listW := w * 5 / 12
	return columns(stRule.Render(m.g.Pane), h, m.threadLines(list, sel, listW, h), fit(m.g, m.threadDetail(list[sel], w-listW-5, h), w-listW-4, h))
}

func (m model) threadLines(list []thread, sel, w, h int) string {
	var out []string
	selLine := 0
	last := threadGroup(-1)
	counts := map[threadGroup]int{}
	for _, t := range list {
		counts[t.group]++
	}
	for i, t := range list {
		if t.group != last {
			last = t.group
			out = append(out, subRule(fmt.Sprintf("%s (%d)", t.group.title(), counts[t.group]), "", w, m.g))
		}
		if i == sel {
			selLine = len(out)
		}
		out = append(out, m.threadRow(t, i == sel, w))
	}
	top := 0
	if selLine >= h-1 {
		top = selLine - h + 2
	}
	return fit(m.g, strings.Join(out[min(top, len(out)):], "\n"), w, h)
}

func (m model) threadRow(t thread, selected bool, w int) string {
	g := m.g
	mark := "   "
	if selected {
		mark = " " + stBold.Render(g.Selected) + " "
	}
	var state string
	switch t.group {
	case groupOpen:
		state = stWarn.Render(pad(m.g, g.Info+" open", 9))
	case groupDone:
		state = stFaint.Render(pad(m.g, g.OK+" done", 9))
	default:
		state = stFaint.Render(pad(m.g, "note", 9))
	}
	n := t.first()
	who := stFaint.Render(n.Author.Display())
	if len(t.d.Notes) > 1 {
		who += stFaint.Render(fmt.Sprintf(" +%d", len(t.d.Notes)-1))
	}
	where := t.where()
	if selected {
		where = stBold.Render(where)
	}
	left := mark + state + " " + where
	quote := stFaint.Render(`"` + oneLine(n.Body) + `"`)
	room := w - lipgloss.Width(left) - lipgloss.Width(who) - 4
	if room >= 12 {
		left += "  " + ansi.Truncate(quote, room, m.g.Ell)
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(who) - 1
	if gap < 1 {
		return ansi.Truncate(left, w-lipgloss.Width(who)-2, m.g.Ell) + " " + who
	}
	return left + strings.Repeat(" ", gap) + who
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// threadDetail is the conversation: where it is, the diff around it, then
// every note.
func (m model) threadDetail(t thread, w, h int) string {
	g := m.g
	d := m.mr.data
	state := stWarn.Render(g.Info + " open")
	switch t.group {
	case groupDone:
		state = stFaint.Render(g.OK + " resolved")
	case groupComment:
		state = stFaint.Render("comment")
	}
	head := []string{stBold.Render(ansi.Truncate(t.where(), w, m.g.Ell)), state}
	var body []string
	if m.aiOn() {
		body = append(body, m.threadAI(d, t.d.ID, w)...)
	}
	if t.d.Position != nil {
		if ctx := m.diffContext(d, t.d.Position, w); len(ctx) > 0 {
			body = append(body, ctx...)
			body = append(body, "")
		}
	}
	for _, n := range t.d.Notes {
		if n.System {
			body = append(body, stFaint.Render(oneLine(n.Body)))
			continue
		}
		body = append(body, stBold.Render(n.Author.Display())+stFaint.Render(" "+g.Sep+" "+ago(n.CreatedAt)))
		body = append(body, strings.Split(lipgloss.Wrap(n.Body, w-1, " "), "\n")...)
		body = append(body, "")
	}
	// A conversation longer than the pane shows its latest notes.
	room := max(1, h-len(head)-1)
	if len(body) > room {
		body = append([]string{stFaint.Render(g.ToCome + " earlier notes cut")}, body[len(body)-room+1:]...)
	}
	return strings.Join(append(append(head, ""), body...), "\n")
}

// --- CI --------------------------------------------------------------------------

func (m model) mrCI(w, h int) string {
	p := m.mr
	d := p.data
	g := m.g
	if d.mr.Pipeline == nil {
		return fit(m.g, "   "+stFaint.Render("No pipeline ran for this merge request."), w, h)
	}
	if ph, ok := m.wait(d.jobsL, "CI jobs", w, h); !ok {
		return ph
	}
	l := []string{" " + m.stageLine(d), ""}
	sel := clamp(p.ciSel, 0, max(0, len(d.jobs)-1))
	nameW := 12
	for _, j := range d.jobs {
		nameW = max(nameW, min(30, lipgloss.Width(j.Name)))
	}
	for i, j := range d.jobs {
		mark := "   "
		name := pad(m.g, j.Name, nameW)
		if i == sel {
			mark, name = " "+stBold.Render(g.Selected)+" ", stBold.Render(name)
		}
		gl, word := ciGlyph(g, j.Status)
		dur := ""
		if j.Duration > 0 {
			dur = fmt.Sprintf("%.0fs", j.Duration)
		}
		l = append(l, mark+gl+" "+pad(m.g, word, 8)+" "+name+" "+stFaint.Render(pad(m.g, j.Stage, 12))+" "+stFaint.Render(dur))
	}
	if len(d.jobs) == 0 {
		l = append(l, "   "+stFaint.Render("The pipeline has no jobs yet."))
	}
	failed := 0
	for _, j := range d.jobs {
		if j.Status == "failed" {
			failed++
		}
	}
	l = append(l, "")
	triage := ""
	if m.aiOn() && failed > 0 {
		triage = m.g.T(" · t asks the AI for cause and fix")
	}
	switch {
	case failed > 0:
		l = append(l, "   "+stBad.Render(fmt.Sprintf("%s %d failed %s", g.Bad, failed, plural(failed, "job")))+stFaint.Render(m.g.T(" · l shows the log tail · r retries the failed jobs")+triage))
	default:
		l = append(l, "   "+stFaint.Render(m.g.T("l shows a job's log tail · r retries failed jobs")))
	}
	if blk := m.ciTriageLines(d, w); len(blk) > 0 {
		l = append(append(l, ""), blk...)
		return m.scrolled(l, w, h)
	}
	return fit(m.g, strings.Join(l, "\n"), w, h)
}

// stageLine draws the pipeline as a station line in the repo's ink: one
// station per stage, travelled up to the first stage that has not passed.
func (m model) stageLine(d *mrData) string {
	g := m.g
	repo := d.ref.Project
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repo = repo[i+1:]
	}
	line := ink(repo)
	var order []string
	status := map[string]string{}
	rank := map[string]int{"success": 0, "skipped": 0, "manual": 1, "pending": 2, "created": 2, "running": 3, "canceled": 4, "failed": 5}
	for _, j := range d.jobs {
		if _, ok := status[j.Stage]; !ok {
			order = append(order, j.Stage)
			status[j.Stage] = j.Status
			continue
		}
		if rank[j.Status] > rank[status[j.Stage]] {
			status[j.Stage] = j.Status
		}
	}
	var b strings.Builder
	reached := true
	for i, st := range order {
		if i > 0 {
			if reached {
				b.WriteString(line.Render(strings.Repeat(g.Travelled, 2)))
			} else {
				b.WriteString(stFaint.Render(strings.Repeat(g.ToCome, 2)))
			}
		}
		var station string
		switch status[st] {
		case "success":
			station = line.Render(g.Done)
		case "skipped": // not run: hollow, but it does not stop the travelled line
			station = stFaint.Render(g.Ahead)
		case "failed":
			station = stBad.Bold(true).Render(g.Bad)
			reached = false
		case "running":
			station = line.Bold(true).Render(g.Train)
			reached = false
		default:
			station = stFaint.Render(g.Ahead)
			reached = false
		}
		b.WriteString(" " + station + " " + st + " ")
	}
	return b.String()
}

// indentBlock gives a full-width pane the page's one-cell margin.
func indentBlock(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = " " + lines[i]
	}
	return strings.Join(lines, "\n")
}
