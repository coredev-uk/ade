package tui

import (
	"fmt"
	"slices"
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
	// badgeW is the thread-count and pipeline column after the track, 0 where
	// the list is too narrow to spare it.
	badgeW int
}

// badgeCells is the width of "✉ 12 ✓": threads, a space, the pipeline glyph.
const badgeCells = 6

// minTitleForBadge is the narrowest title cell that still gives up room to the
// badge column.
const minTitleForBadge = 20

// layoutFor sizes columns for a list w cells wide; courtW is the widest court
// text present, so reasons are never cut when there is room.
func layoutFor(w, refW, courtW int) homeLayout {
	badge := 0
	if w >= 76 {
		badge = badgeCells
	}
	if w < 110 {
		return homeLayout{refW: refW, trackGap: 1, weightW: 3, courtW: min(courtW, max(18, w*2/5)), badgeW: badge}
	}
	return homeLayout{refW: refW, trackGap: 2, weightW: 4, courtW: min(courtW, 40), badgeW: badge}
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
		return stFaint.Render(g.T("–"))
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
	reason := fitWords(g, it.Reason, w-lipgloss.Width(c)-lipgloss.Width(sep)-mark)
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
func fitWords(g glyphs, s string, w int) string {
	s = g.T(s)
	if lipgloss.Width(s) <= w {
		return s
	}
	if w <= 1 {
		return ""
	}
	cut := ansi.Truncate(s, w-lipgloss.Width(g.Ell), "")
	if i := strings.LastIndexAny(cut, " ·:"); i > 0 {
		cut = strings.TrimRight(cut[:i], " ·:")
	}
	return cut + g.Ell
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
	if m.hs.search.typing || m.searching() {
		return m.searchBar(w) + "\n" + m.homeList(w, h-1)
	}
	return m.homeList(w, h)
}

// narrowed says what is limiting the list, for the empty message.
func (m model) narrowed() string {
	switch {
	case m.searching():
		return `matching "` + strings.TrimSpace(m.hs.search.input) + `"`
	case m.hs.facet != "":
		return m.hs.facet + " " + m.hs.facetVal
	case m.homeFilter != hfAll:
		return m.homeFilter.String()
	}
	return ""
}

func (m model) homeList(w, h int) string {
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
			courtW = max(courtW, lipgloss.Width(courtText(m.g, withAuthor(r))))
		}
	}
	lay := layoutFor(w, refW, courtW)
	// Gaps: ref|title|track|badge|weight|court; the badge and its gap go when
	// they would leave the title under minTitleForBadge cells.
	titleW := max(12, w-4-lay.refW-lay.trackW()-lay.weightW-lay.courtW-4*2-lay.badgeGap())
	if lay.badgeW > 0 && titleW < minTitleForBadge {
		lay.badgeW = 0
		titleW = max(12, w-4-lay.refW-lay.trackW()-lay.weightW-lay.courtW-4*2)
	}
	idx := 0
	legend := false // the station names appear once, above the first project that has tracks
	narrowed := m.narrowed()
	for ri := range m.results {
		res := &m.results[ri]
		if m.hs.project != "" && res.Project != m.hs.project {
			continue
		}
		out = append(out, m.projectRule(res, w))
		if !legend && res.Err == "" {
			out = append(out, m.legend(lay, titleW))
			legend = true
		}
		if res.Err != "" {
			out = append(out, "   "+stBad.Render(m.g.Bad+" sync failed: ")+ansi.Truncate(res.Err, w-20, m.g.Ell))
		}
		shown := false
		for sec := secMine; sec <= secSearch; sec++ {
			start := idx
			for idx < len(m.homeRows) && m.homeRows[idx].project == res.Project && m.homeRows[idx].section == sec {
				idx++
			}
			if start == idx {
				if sec == secMine && narrowed == "" && res.Err == "" {
					out = append(out, subRule(sec.title(m.g), "nothing assigned", w, m.g),
						"   "+stFaint.Render("Nothing assigned to you in this project."))
				}
				continue
			}
			shown = true
			out = append(out, subRule(sec.title(m.g), m.sectionGuide(res, sec, m.homeRows[start:idx]), w, m.g))
			for i := start; i < idx; i++ {
				if i == m.homeSel {
					selLine = len(out)
				}
				out = append(out, m.homeRowLine(m.homeRows[i], lay, i == m.homeSel, titleW, marks))
			}
		}
		if !shown && narrowed != "" {
			hint := " f changes the filter."
			if m.searching() {
				hint = " esc clears the search."
			}
			out = append(out, "   "+stFaint.Render("Nothing "+narrowed+" here."+hint))
		}
		out = append(out, "")
	}
	if !m.threeRegion() {
		out = append(out, m.activityLines(w)...)
	}
	top := 0
	if selLine >= h-1 {
		top = selLine - h + 2
	}
	return fit(m.g, strings.Join(out[min(top, len(out)):], "\n"), w, h)
}

// badgeGap is the two-cell gap the badge column adds, 0 without the column.
func (l homeLayout) badgeGap() int {
	if l.badgeW == 0 {
		return 0
	}
	return l.badgeW + 2
}

// projectRule heads a project: its name, the current sprint and days left on
// the rule, load in the guide words.
func (m model) projectRule(res *forgesync.Result, w int) string {
	title := res.Project
	sep := " " + m.g.Sep + " "
	var head []string    // the sprint's end and days left: always kept
	var loads []string   // the load, most detailed first; a shorter one is tried before dropping anything
	var next string      // the next sprint's start: dropped first
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
		head = append(head, ends+left)
		var loadShort string
		if l := loadOf(res); l.items > 0 && l.unweighted == l.items {
			loads, loadShort = []string{"no weights"}, "no weights"
		} else if l.items > 0 {
			loadShort = fmt.Sprintf("%d/%dw", l.done, l.committed)
			lean := fmt.Sprintf("%dw committed %s %dw done %s %dw left", l.committed, m.g.Sep, l.done, m.g.Sep, max(l.committed-l.done, 0))
			mid := lean
			if v, ok := m.hs.vel[res.Project]; ok {
				mid += " " + m.g.Sep + " " + v.String()
			}
			full := mid
			if l.unweighted > 0 {
				full = lean + fmt.Sprintf(" %s %d unweighted", m.g.Sep, l.unweighted)
				if v, ok := m.hs.vel[res.Project]; ok {
					full += " " + m.g.Sep + " " + v.String()
				}
			}
			loads = []string{full, mid, lean}
		}
		c := ends + short
		if loadShort != "" {
			c += sep + loadShort
		}
		compact = append(compact, c)
	} else if s := res.Window.Next; s != nil {
		head = append(head, "no sprint running "+m.g.Sep+" "+s.Title+" starts "+s.Start.Format("Mon 2 Jan"))
	}
	if n := res.Window.Next; n != nil && res.Window.Current != nil {
		next = "next " + n.Title + " starts " + n.Start.Format("Mon 2 Jan")
		compact = append(compact, next)
	}
	room := w - lipgloss.Width(title) - 6
	fits := func(g []string) bool { return lipgloss.Width(strings.Join(g, sep)) <= room }
	// Candidates from richest to leanest; the first that fits wins. The compact
	// form keeps the load in short form before any group is dropped.
	var cands [][]string
	for _, load := range loads {
		if next != "" {
			cands = append(cands, append(append(slices.Clone(head), load), next))
		}
		cands = append(cands, append(slices.Clone(head), load))
	}
	if len(loads) == 0 {
		cands = append(cands, append(slices.Clone(head), next))
		cands[len(cands)-1] = slices.DeleteFunc(cands[len(cands)-1], func(s string) bool { return s == "" })
	}
	if len(compact) > 0 {
		cands = append(cands, compact)
	}
	guide := cands[len(cands)-1]
	for _, c := range cands {
		if fits(c) {
			guide = c
			break
		}
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
		b.WriteString(pad(m.g, n, col))
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

// sectionGuide words a section's header. A search lists assigned sprint
// items too, so the sprint-open count is of those with nobody on them.
func (m model) sectionGuide(res *forgesync.Result, sec homeSection, rows []homeRow) string {
	n := len(rows)
	switch sec {
	case secReviews:
		return fmt.Sprintf("%d waiting on you", n)
	case secSprintOpen:
		unassigned := 0
		for _, r := range rows {
			if r.item != nil && len(r.item.Issue.Assignees) == 0 {
				unassigned++
			}
		}
		if unassigned < n {
			return fmt.Sprintf("%d unassigned in the tracker · %d assigned to others", unassigned, n-unassigned)
		}
		return fmt.Sprintf("%d unassigned in the tracker", unassigned)
	case secNext:
		return fmt.Sprintf("%d not assigned to you", n)
	case secPrevious:
		title := "the last sprint"
		if p := res.Window.Previous; p != nil {
			title = p.Title
		}
		return fmt.Sprintf("%d unfinished %s carried over from %s", n, m.g.Sep, title)
	case secSearch:
		note := ""
		if m.hs.search.loading {
			note = " " + m.g.Sep + " " + m.g.Sync + " searching"
		}
		return fmt.Sprintf("%d open %s on the forge%s", n, plural(n, "MR"), note)
	default:
		you := 0
		for i := range m.homeRows {
			r := m.homeRows[i]
			if r.project == res.Project && r.section == secMine && r.item.Court == domain.CourtYou {
				you++
			}
		}
		return fmt.Sprintf(m.g.T("%d %s · %d waiting on you"), n, plural(n, "item"), you)
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
		ref := pad(m.g, m.shortRefOf(r), lay.refW)
		title := mr.Title
		if selected {
			title = stBold.Render(title)
		}
		left := mark + stFaint.Render(ref) + "  " + pad(m.g, ansi.Truncate(title, titleW-lipgloss.Width(unread), m.g.Ell)+unread, titleW)
		right := stFaint.Render(mr.Author.Display()) + stFaint.Render(" "+m.g.Sep+" ") + mrStatus(m.g, mr)
		return left + "  " + strings.Repeat(" ", lay.trackW()+lay.badgeGap()+2+lay.weightW) + "  " + ansi.Truncate(right, lay.courtW, m.g.Ell)
	}
	it := r.item
	ref := pad(m.g, m.shortRefOf(r), lay.refW)
	title := it.Issue.Title
	if selected {
		title = stBold.Render(title)
	}
	titleCell := m.titleCell(it, title, titleW, unread)
	tr := track(m.g, it, lay.trackGap)
	if len(it.Triage) > 0 && contains(it.Triage, "type") && contains(it.Triage, "app") {
		label := "? untriaged"
		if lay.trackW() < len(label) {
			label = "? triage"
		}
		tr = stFaint.Render(pad(m.g, label, lay.trackW()))
	}
	badge := ""
	if lay.badgeW > 0 {
		badge = m.rowBadge(it) + "  "
	}
	return mark + stFaint.Render(ref) + "  " + titleCell + "  " + tr + "  " + badge +
		pad(m.g, weightText(m.g, it.Weight), lay.weightW) + "  " + courtFit(m.g, withAuthor(r), lay.courtW)
}

// withAuthor is the row's item, with the reason "unassigned" swapped for the
// tracker issue's author in the sprint sections: nobody holds those tickets,
// so who raised one is what tells them apart.
func withAuthor(r homeRow) *domain.WorkItem {
	it := r.item
	if r.section < secSprintOpen || r.section > secPrevious || it.Reason != "unassigned" || it.Issue.Author.Display() == "" {
		return it
	}
	c := *it
	c.Reason = "by " + it.Issue.Author.Display()
	return &c
}

// titleCell is the title, the unread mark, then the epic faint after them when
// the cell has room: both follow the title directly, never the cell's edge.
func (m model) titleCell(it *domain.WorkItem, title string, titleW int, unread string) string {
	room := titleW - lipgloss.Width(unread)
	epic := ""
	if e := it.Issue.Epic; e != "" && titleW >= 36 {
		epicW := min(lipgloss.Width(e)+3, max(room/3, 8))
		epic = stFaint.Render(ansi.Truncate(" "+m.g.Sep+" "+e, epicW, m.g.Ell))
		room -= lipgloss.Width(epic)
	}
	return pad(m.g, ansi.Truncate(title, room, m.g.Ell)+unread+epic, titleW)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// rowBadge is "✉ 2 ✓": unresolved review threads over the item's open MRs and
// the state of the newest pipeline, each a glyph plus a number; blank when
// there is neither. It is exactly badgeCells wide.
func (m model) rowBadge(it *domain.WorkItem) string {
	threads := 0
	var pipe *forge.Pipeline
	for i := range it.MRs {
		mr := &it.MRs[i]
		if mr.State != "opened" {
			continue
		}
		threads += mr.Unresolved
		if mr.Pipeline != nil && (pipe == nil || mr.Pipeline.UpdatedAt.After(pipe.UpdatedAt)) {
			pipe = mr.Pipeline
		}
	}
	t := ""
	if threads > 0 {
		t = stBold.Render(fmt.Sprintf("%s %d", m.g.Thread, threads))
	}
	p := ""
	if pipe != nil {
		switch pipe.Status {
		case "failed":
			p = stBad.Render(m.g.Bad)
		case "success":
			p = stOK.Render(m.g.OK)
		case "canceled", "skipped", "manual":
			p = stFaint.Render(m.g.Ahead)
		default:
			p = stWarn.Render(m.g.Sync)
		}
	}
	return pad(m.g, t, badgeCells-2) + " " + pad(m.g, p, 1)
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
	l = append(l, stBold.Render(ansi.Truncate(it.Ref.String()+"  "+it.Issue.Title, w, m.g.Ell)))
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
	if a := it.Issue.Author.Display(); a != "" {
		meta = append(meta, "by "+a)
	}
	l = append(l, stFaint.Render(ansi.Truncate(strings.Join(meta, " "+g.Sep+" "), w, m.g.Ell)))
	if it.Workflow != "" || it.WantWorkflow != "" {
		lbl := "label " + orDash(m.g, it.Workflow)
		if it.Drift {
			l = append(l, stWarn.Render(g.Warn+" "+lbl+" "+g.Sep+" stage says "+it.WantWorkflow))
		} else {
			l = append(l, stFaint.Render(lbl+" "+g.OK))
		}
	}
	l = append(l, "")
	l = append(l, m.stationLines(it, w)...)
	l = append(l, "", courtText(g, it))
	if svc := m.serviceLines(it.Ref.String(), w); len(svc) > 0 && !m.threeRegion() { // the rail carries them at wide widths
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
		l = append(l, m.mdPreview(d, w, 8)...)
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
		label := pad(m.g, name, 9)
		if st == it.Stage {
			label = stBold.Render(label)
		}
		l = append(l, " "+glyph+"  "+label+" "+ansi.Truncate(evidence[i], w-14, m.g.Ell))
		if i < len(domain.Stations)-1 {
			conn := stFaint.Render(m.g.Thin)
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
		who = append(who, a.Display())
	}
	ev[domain.StageAssigned] = stFaint.Render(orDash(m.g, strings.Join(who, ", ")))
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
		stBold.Render(ansi.Truncate(mr.Ref.String()+"  "+mr.Title, w, m.g.Ell)),
		stFaint.Render(ansi.Truncate(mr.Author.Display()+" "+gl.Sep+" "+mr.SourceBranch+m.g.T(" → ")+mr.TargetBranch, w, m.g.Ell)),
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
		l = append(l, m.mdPreview(d, w, 8)...)
	}
	return strings.Join(l, "\n")
}

func orDash(g glyphs, s string) string {
	if s == "" {
		return g.T("–")
	}
	return s
}
