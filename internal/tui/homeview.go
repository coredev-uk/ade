package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
)

// homeLayout sizes Home's columns, in cells. The track is the signature: one
// station per stage. Narrow lists shorten the connectors to one cell and give
// the court a third of the width so titles stay readable.
type homeLayout struct {
	refW, trackGap, weightW, courtW int
}

// layoutFor sizes columns for a list w cells wide; courtW is the widest court
// text present, so reasons are never cut when there is room.
func layoutFor(w, refW, courtW int) homeLayout {
	if w < 110 {
		return homeLayout{refW: refW, trackGap: 1, weightW: 3, courtW: min(courtW, max(18, w*2/5))}
	}
	return homeLayout{refW: refW, trackGap: 2, weightW: 4, courtW: min(courtW, 40)}
}

func (l homeLayout) trackW() int { return len(domain.Stations) + (len(domain.Stations)-1)*l.trackGap }

// track draws an item's stage line in its line ink: done stations filled,
// the current stage as the train (an interchange ring when the item spans
// repos, amber when blocked), stages to come hollow. gap is the connector
// length between stations (0 draws stations only).
func track(g glyphs, it *domain.WorkItem, gap int) string {
	line := ink(lineOf(it))
	var b strings.Builder
	for i := range domain.Stations {
		cur := domain.Stage(i)
		done := it.Stage == domain.StageDone || cur < it.Stage
		if i > 0 && gap > 0 {
			if done || cur == it.Stage {
				b.WriteString(line.Render(strings.Repeat(g.Travelled, gap)))
			} else {
				b.WriteString(stFaint.Render(strings.Repeat(g.ToCome, gap)))
			}
		}
		switch {
		case done:
			b.WriteString(line.Render(g.Done))
		case cur == it.Stage && (it.Blocked || it.Court == domain.CourtBlocked):
			b.WriteString(stWarn.Bold(true).Render(g.Train))
		case cur == it.Stage && len(it.Repos) > 1:
			b.WriteString(stBold.Render(g.Interchange))
		case cur == it.Stage:
			b.WriteString(line.Bold(true).Render(g.Train))
		default:
			b.WriteString(stFaint.Render(g.Ahead))
		}
	}
	return b.String()
}

// lineOf picks the ink an item draws in: its first routed repo, else its app,
// else its own project path, so the colour is stable across syncs.
func lineOf(it *domain.WorkItem) string {
	switch {
	case len(it.Repos) > 0:
		return it.Repos[0]
	case len(it.Apps) > 0:
		return it.Apps[0]
	default:
		return it.Ref.Project
	}
}

func weightText(g glyphs, w *int) string {
	if w == nil {
		return stFaint.Render("–")
	}
	return fmt.Sprintf("%dw", *w)
}

// courtWord names who holds the item. Waiting words carry the warn glyph and
// bold so they read without colour.
func courtWord(g glyphs, it *domain.WorkItem) string {
	switch it.Court {
	case domain.CourtYou:
		return stBold.Render("you")
	case domain.CourtBlocked:
		return stWarn.Bold(true).Render(g.Warn + " blocked")
	case domain.CourtUpstream:
		return stWarn.Bold(true).Render(g.Warn + " waits")
	default:
		return stFaint.Render(string(it.Court))
	}
}

func courtText(g glyphs, it *domain.WorkItem) string { return courtFit(g, it, 1<<20) }

// courtFit renders "court · reason" in at most w cells, cutting the reason at
// a word boundary (with an ellipsis) rather than mid-word. A failed pipeline
// is the one reason that carries a mark of its own: red, bold and led by the
// failure glyph, so it reads without colour too.
func courtFit(g glyphs, it *domain.WorkItem, w int) string {
	c := courtWord(g, it)
	if it.Reason == "" {
		return c
	}
	sep := " " + g.Sep + " "
	failed := strings.HasPrefix(it.Reason, "pipeline failed")
	mark := 0
	if failed {
		mark = lipgloss.Width(g.Bad) + 1
	}
	reason := fitWords(it.Reason, w-lipgloss.Width(c)-lipgloss.Width(sep)-mark)
	if reason == "" {
		return c
	}
	if failed {
		return c + stFaint.Render(sep) + stBad.Bold(true).Render(g.Bad+" "+reason)
	}
	if it.Court == domain.CourtYou {
		return c + stFaint.Render(sep) + reason
	}
	return c + stFaint.Render(sep+reason)
}

// fitWords shortens plain text to w cells at a word boundary, ending in "…".
// A single word longer than w is cut with an ellipsis.
func fitWords(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	if w <= 1 {
		return ""
	}
	cut := ansi.Truncate(s, w-1, "")
	if i := strings.LastIndexAny(cut, " ·:"); i > 0 {
		cut = strings.TrimRight(cut[:i], " ·:")
	}
	return cut + "…"
}

// newMark flags unread notifications on a row with a word, not a dot: dots
// are stations on this screen.
func newMark(n int) string {
	if n == 1 {
		return stWarn.Bold(true).Render("new")
	}
	return stWarn.Bold(true).Render(fmt.Sprintf("%d new", n))
}

func (m model) homeLines(w, h int) string {
	marks := m.nc.marks()
	var out []string
	selLine := 0
	refW := 3
	for _, r := range m.homeRows {
		refW = max(refW, lipgloss.Width(m.shortRefOf(r)))
	}
	courtW := 12
	for _, r := range m.homeRows {
		if r.item != nil {
			courtW = max(courtW, lipgloss.Width(courtText(m.g, r.item)))
		}
	}
	lay := layoutFor(w, refW, courtW)
	// Four two-cell gaps: ref|title|track|weight|court.
	titleW := max(12, w-3-lay.refW-lay.trackW()-lay.weightW-lay.courtW-4*2)
	idx := 0
	legend := false // the station names appear once, above the first project that has tracks
	for ri := range m.results {
		res := &m.results[ri]
		out = append(out, m.projectRule(res, w))
		if !legend && res.Err == "" {
			out = append(out, m.legend(lay, titleW))
			legend = true
		}
		if res.Err != "" {
			out = append(out, "   "+stBad.Render(m.g.Bad+" sync failed: ")+ansi.Truncate(res.Err, w-20, "…"))
		}
		shown := false
		for sec := secMine; sec <= secNext; sec++ {
			start := idx
			for idx < len(m.homeRows) && m.homeRows[idx].project == res.Project && m.homeRows[idx].section == sec {
				idx++
			}
			if start == idx {
				if sec == secMine && m.homeFilter == hfAll && res.Err == "" {
					out = append(out, subRule(sec.title(), "nothing assigned", w, m.g),
						"   "+stFaint.Render("Nothing assigned to you in this project."))
				}
				continue
			}
			shown = true
			out = append(out, subRule(sec.title(), m.sectionGuide(res, sec, idx-start), w, m.g))
			for i := start; i < idx; i++ {
				if i == m.homeSel {
					selLine = len(out)
				}
				out = append(out, m.homeRowLine(m.homeRows[i], lay, i == m.homeSel, titleW, marks))
			}
		}
		if !shown && m.homeFilter != hfAll {
			out = append(out, "   "+stFaint.Render("No "+m.homeFilter.String()+" items here. f cycles the filter."))
		}
		out = append(out, "")
	}
	top := 0
	if selLine >= h-1 {
		top = selLine - h + 2
	}
	return fit(strings.Join(out[min(top, len(out)):], "\n"), w, h)
}

// projectRule heads a project: its name, the current sprint and days left on
// the rule, load in the guide words.
func (m model) projectRule(res *forgesync.Result, w int) string {
	title := res.Project
	sep := " " + m.g.Sep + " "
	var guide []string   // highest priority first: dropped from the end to fit
	var compact []string // the same, with the sprint and its load squeezed into one group
	if s := res.Window.Current; s != nil {
		title += sep + s.Title
		days := res.Window.DaysLeft
		left := fmt.Sprintf("%d workday%s left", days, map[bool]string{true: "", false: "s"}[days == 1])
		short := fmt.Sprintf("%dd left", days)
		if days <= 1 {
			left, short = stWarn.Render(left), stWarn.Render(short)
		}
		ends := "ends " + s.Due.Format("Mon 2 Jan") + sep
		guide = append(guide, ends+left)
		var load, loadShort string
		if l := loadOf(res); l.items > 0 && l.unweighted == l.items {
			load, loadShort = "no weights", "no weights"
		} else if l.items > 0 {
			loadShort = fmt.Sprintf("%d/%dw", l.done, l.committed)
			load = fmt.Sprintf("%dw committed %s %dw done", l.committed, m.g.Sep, l.done)
			if l.unweighted > 0 {
				load += fmt.Sprintf(" %s %d unweighted", m.g.Sep, l.unweighted)
			}
		}
		if load != "" {
			guide = append(guide, load)
		}
		head := ends + short
		if loadShort != "" {
			head += sep + loadShort
		}
		compact = append(compact, head)
	} else if s := res.Window.Next; s != nil {
		guide = append(guide, "no sprint running "+m.g.Sep+" "+s.Title+" starts "+s.Start.Format("Mon 2 Jan"))
	}
	if n := res.Window.Next; n != nil && res.Window.Current != nil {
		next := "next " + n.Title + " starts " + n.Start.Format("Mon 2 Jan")
		guide = append(guide, next)
		compact = append(compact, next)
	}
	room := w - lipgloss.Width(title) - 6
	fits := func(g []string) bool { return lipgloss.Width(strings.Join(g, sep)) <= room }
	if !fits(guide) && len(compact) > 0 {
		// Keep load visible in short form before any group is dropped.
		guide = compact
	}
	for len(guide) > 1 && !fits(guide) {
		guide = guide[:len(guide)-1]
	}
	return section(title, strings.Join(guide, sep), w, m.g)
}

// legend names the track's stations once, aligned above the first tracks:
// two-letter names where a station has three cells, initials where it has two.
func (m model) legend(lay homeLayout, titleW int) string {
	names := []string{"as", "st", "br", "dr", "ci", "rv", "mg"}
	col := 1 + lay.trackGap
	var b strings.Builder
	for _, n := range names {
		if col < 3 {
			n = n[:1]
		}
		b.WriteString(pad(n, col))
	}
	lead := strings.Repeat(" ", 3+lay.refW+2+titleW+2)
	return stFaint.Render(lead + strings.TrimRight(b.String(), " "))
}

func subRule(title, guide string, w int, g glyphs) string {
	t := "   " + title + " "
	gd := " " + stFaint.Render(guide)
	fill := max(1, w-lipgloss.Width(t)-lipgloss.Width(gd)-1)
	return t + stRule.Render(strings.Repeat(g.Rule, fill)) + gd
}

func (m model) sectionGuide(res *forgesync.Result, sec homeSection, n int) string {
	switch sec {
	case secReviews:
		return fmt.Sprintf("%d waiting on you", n)
	case secSprintOpen, secNext:
		return fmt.Sprintf("%d not assigned to you", n)
	default:
		you := 0
		for i := range m.homeRows {
			r := m.homeRows[i]
			if r.project == res.Project && r.section == secMine && r.item.Court == domain.CourtYou {
				you++
			}
		}
		return fmt.Sprintf("%d %s · %d waiting on you", n, plural(n, "item"), you)
	}
}

// shortRefOf finds the row's project tracker to shorten its ref.
func (m model) shortRefOf(r homeRow) string {
	tracker := ""
	for i := range m.results {
		if m.results[i].Project == r.project {
			tracker = m.results[i].Tracker
		}
	}
	if r.mr != nil {
		return shortRef(r.mr.Ref, tracker)
	}
	return shortRef(r.item.Ref, tracker)
}

func (m model) homeRowLine(r homeRow, lay homeLayout, selected bool, titleW int, marks map[string]int) string {
	mark := "   "
	if selected {
		mark = " " + stBold.Render(m.g.Selected) + " "
	}
	unread := ""
	if n := marks[markKey(r.project, r.ref())]; n > 0 {
		unread = " " + newMark(n)
	}
	if r.mr != nil {
		mr := r.mr
		ref := pad(m.shortRefOf(r), lay.refW)
		title := mr.Title
		if selected {
			title = stBold.Render(title)
		}
		left := mark + stFaint.Render(ref) + "  " + pad(ansi.Truncate(title, titleW-lipgloss.Width(unread), "…")+unread, titleW)
		right := stFaint.Render(mr.Author.Username) + stFaint.Render(" "+m.g.Sep+" ") + mrStatus(m.g, mr)
		return left + "  " + strings.Repeat(" ", lay.trackW()+2+lay.weightW) + "  " + ansi.Truncate(right, lay.courtW, "…")
	}
	it := r.item
	ref := pad(m.shortRefOf(r), lay.refW)
	title := it.Issue.Title
	if selected {
		title = stBold.Render(title)
	}
	titleCell := pad(ansi.Truncate(title, titleW-lipgloss.Width(unread), "…")+unread, titleW)
	tr := track(m.g, it, lay.trackGap)
	if len(it.Triage) > 0 && contains(it.Triage, "type") && contains(it.Triage, "app") {
		label := "? untriaged"
		if lay.trackW() < len(label) {
			label = "? triage"
		}
		tr = stFaint.Render(pad(label, lay.trackW()))
	}
	return mark + stFaint.Render(ref) + "  " + titleCell + "  " + tr + "  " +
		pad(weightText(m.g, it.Weight), lay.weightW) + "  " + courtFit(m.g, it, lay.courtW)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func mrStatus(g glyphs, mr *forge.MR) string {
	var parts []string
	if mr.Draft {
		parts = append(parts, "draft")
	}
	if p := mr.Pipeline; p != nil {
		switch p.Status {
		case "failed":
			parts = append(parts, stBad.Render(g.Bad+" ci"))
		case "success":
			parts = append(parts, stOK.Render(g.OK+" ci"))
		default:
			parts = append(parts, g.Sync+" ci")
		}
	}
	if mr.Unresolved > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", mr.Unresolved, plural(mr.Unresolved, "thread")))
	}
	if len(parts) == 0 {
		return "ready"
	}
	return strings.Join(parts, " "+g.Sep+" ")
}

// --- preview ---------------------------------------------------------------------

func (m model) homeDetail(w int) string {
	if m.homeSel >= len(m.homeRows) {
		return lipgloss.Wrap(stFaint.Render("Nothing selected."), w, " ")
	}
	r := m.homeRows[m.homeSel]
	if r.mr != nil {
		return m.mrDetail(r.mr, w)
	}
	return m.itemDetail(r.item, w)
}

func (m model) itemDetail(it *domain.WorkItem, w int) string {
	g := m.g
	var l []string
	l = append(l, stBold.Render(ansi.Truncate(it.Ref.String()+"  "+it.Issue.Title, w, "…")))
	var meta []string
	for _, v := range []string{it.Type, strings.Join(it.Apps, "+")} {
		if v != "" {
			meta = append(meta, v)
		}
	}
	if it.Sprint != nil {
		meta = append(meta, it.Sprint.Title)
	}
	if it.Weight != nil {
		meta = append(meta, fmt.Sprintf("weight %d", *it.Weight))
	} else {
		meta = append(meta, "unweighted")
	}
	if it.Issue.Epic != "" {
		meta = append(meta, "epic "+it.Issue.Epic)
	}
	l = append(l, stFaint.Render(ansi.Truncate(strings.Join(meta, " "+g.Sep+" "), w, "…")))
	if it.Workflow != "" || it.WantWorkflow != "" {
		lbl := "label " + orDash(it.Workflow)
		if it.Drift {
			l = append(l, stWarn.Render(g.Warn+" "+lbl+" "+g.Sep+" stage says "+it.WantWorkflow))
		} else {
			l = append(l, stFaint.Render(lbl+" "+g.OK))
		}
	}
	l = append(l, "")
	l = append(l, m.stationLines(it, w)...)
	l = append(l, "", courtText(g, it))
	if svc := m.serviceLines(it.Ref.String(), w); len(svc) > 0 {
		l = append(l, "")
		l = append(l, svc...)
		l = append(l, "")
	}
	if len(it.Triage) > 0 {
		l = append(l, stWarn.Render(g.Warn+" needs "+strings.Join(it.Triage, ", ")))
	}
	if t := it.Issue.Tasks; t.Total > 0 {
		l = append(l, stFaint.Render(fmt.Sprintf("tasks %d of %d", t.Done, t.Total)))
	}
	if d := strings.TrimSpace(it.Issue.Description); d != "" {
		l = append(l, "")
		for _, line := range strings.Split(lipgloss.Wrap(d, w, " "), "\n")[:min(8, strings.Count(lipgloss.Wrap(d, w, " "), "\n")+1)] {
			l = append(l, stFaint.Render(line))
		}
	}
	return strings.Join(l, "\n")
}

// stationLines draws the item's line vertically, each station with its
// evidence: the child issue, the branch, the MR and its pipeline and review.
func (m model) stationLines(it *domain.WorkItem, w int) []string {
	g := m.g
	line := ink(lineOf(it))
	evidence := m.stationEvidence(it)
	var l []string
	for i, name := range domain.Stations {
		st := domain.Stage(i)
		glyph := stFaint.Render(g.Ahead)
		switch {
		case st < it.Stage || it.Stage == domain.StageDone:
			glyph = line.Render(g.Done)
		case st == it.Stage:
			glyph = line.Bold(true).Render(g.Train)
		}
		label := pad(name, 9)
		if st == it.Stage {
			label = stBold.Render(label)
		}
		l = append(l, " "+glyph+"  "+label+" "+ansi.Truncate(evidence[i], w-14, "…"))
		if i < len(domain.Stations)-1 {
			conn := stFaint.Render("│")
			if st < it.Stage || it.Stage == domain.StageDone {
				conn = line.Render(g.Vertical)
			}
			l = append(l, " "+conn)
		}
	}
	return l
}

func (m model) stationEvidence(it *domain.WorkItem) []string {
	ev := make([]string, len(domain.Stations))
	var who []string
	for _, a := range it.Issue.Assignees {
		who = append(who, a.Username)
	}
	ev[domain.StageAssigned] = stFaint.Render(orDash(strings.Join(who, ", ")))
	var kids []string
	for _, c := range it.Children {
		kids = append(kids, c.Ref.String())
	}
	ev[domain.StageStarted] = strings.Join(kids, ", ")
	var branches, mrs []string
	for i := range it.MRs {
		mr := &it.MRs[i]
		branches = append(branches, mr.SourceBranch)
		mrs = append(mrs, mr.Ref.String()+" "+stFaint.Render(mr.State))
	}
	ev[domain.StageBranch] = strings.Join(branches, ", ")
	ev[domain.StageDraft] = strings.Join(mrs, ", ")
	for i := range it.MRs {
		mr := &it.MRs[i]
		if mr.State != "opened" {
			continue
		}
		if p := mr.Pipeline; p != nil {
			ev[domain.StageCI] = "pipeline " + p.Status
		} else {
			ev[domain.StageCI] = stFaint.Render("no pipeline")
		}
		approvals := fmt.Sprintf("%d approval%s", len(mr.Approvals.ApprovedBy), map[bool]string{true: "", false: "s"}[len(mr.Approvals.ApprovedBy) == 1])
		if mr.Approvals.Required >= 0 {
			approvals = fmt.Sprintf("%d of %d approvals", len(mr.Approvals.ApprovedBy), mr.Approvals.Required)
		}
		ev[domain.StageReview] = approvals + " " + m.g.Sep + " " + fmt.Sprintf("%d unresolved", mr.Unresolved)
		break
	}
	for i := range it.MRs {
		if mr := &it.MRs[i]; mr.MergedAt != nil {
			ev[domain.StageMerged] = mr.Ref.String() + " " + stFaint.Render(mr.MergedAt.Format("2 Jan"))
		}
	}
	return ev
}

func (m model) mrDetail(mr *forge.MR, w int) string {
	gl := m.g
	l := []string{
		stBold.Render(ansi.Truncate(mr.Ref.String()+"  "+mr.Title, w, "…")),
		stFaint.Render(ansi.Truncate(mr.Author.Username+" "+gl.Sep+" "+mr.SourceBranch+" → "+mr.TargetBranch, w, "…")),
		"",
		mrStatus(gl, mr),
	}
	approvals := fmt.Sprintf("%d approvals", len(mr.Approvals.ApprovedBy))
	if mr.Approvals.Required >= 0 {
		approvals = fmt.Sprintf("%d of %d approvals", len(mr.Approvals.ApprovedBy), mr.Approvals.Required)
	}
	l = append(l, stFaint.Render(fmt.Sprintf("%s %s %d unresolved %s %d resolved", approvals, gl.Sep, mr.Unresolved, gl.Sep, mr.Resolved)))
	if d := strings.TrimSpace(mr.Description); d != "" {
		l = append(l, "")
		wrapped := strings.Split(lipgloss.Wrap(d, w, " "), "\n")
		for _, line := range wrapped[:min(8, len(wrapped))] {
			l = append(l, stFaint.Render(line))
		}
	}
	return strings.Join(l, "\n")
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}
