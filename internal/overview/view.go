package overview

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/itemdoc"
	"github.com/coredev-uk/ade/internal/mdrender"
)

const (
	keyW     = 10 // faint key column of the facts block
	minWidth = 30
)

// Render draws the overview once, in full, for a non-interactive reader:
// every line of the body and the "synced" footer, no keys.
func Render(s Snapshot, glyphName string, width int, now time.Time) string {
	g := glyphSet(glyphName)
	w := max(width, minWidth)
	lines := body(s, g, w, false)
	lines = append(lines, footer(s, w, now, nil))
	return strings.Join(lines, "\n") + "\n"
}

// body is the scrolling part of the view, one string per line.
func body(s Snapshot, g glyphs, w int, gone bool) []string {
	cw := w - 2
	if !s.Found {
		return []string{
			" " + stBold.Render(s.Ref.String()),
			"",
			" " + stFaint.Render(g.Ahead+" not in the cache yet"),
			" " + stFaint.Render("ade sync reads it; this pane then fills in"),
		}
	}
	it := &s.Item
	var out []string
	out = append(out, headerLines(it, g, cw)...)
	if gone {
		out = append(out, " "+stFaint.Render(g.Ahead+" not in cache any more"))
	}
	out = append(out, "")
	out = append(out, factLines(s, g, cw)...)
	out = append(out, "", section("Code", codeGuide(it, g), w, g, false))
	out = append(out, codeLines(it, g, cw)...)

	doc := itemdoc.Parse(it.Issue.Description)
	if doc.Body != "" || doc.Acceptance == "" {
		done, total := itemdoc.Parse(doc.Body).Progress()
		out = append(out, "", section("Description", progressGuide(done, total, "tasks"), w, g, false))
		if doc.Body == "" {
			out = append(out, " "+stFaint.Render("no description"))
		} else {
			out = append(out, docLines(doc.Body, cw, g)...)
		}
	}
	if doc.Acceptance != "" {
		done, total := itemdoc.Parse(doc.Acceptance).Progress()
		out = append(out, "", section("Acceptance", progressGuide(done, total, "done"), w, g, false))
		out = append(out, docLines(doc.Acceptance, cw, g)...)
	}
	out = append(out, "", section("notes", "", w, g, true))
	if strings.TrimSpace(s.Notes) == "" {
		out = append(out, " "+stFaint.Render("no notes yet"))
	} else {
		out = append(out, docLines(s.Notes, cw, g)...)
	}
	return out
}

// --- header ---------------------------------------------------------------------

func headerLines(it *domain.WorkItem, g glyphs, cw int) []string {
	ref := it.Ref.String()
	line := ink(lineOf(it))
	var out []string
	for i, l := range wrap(ref+"  "+it.Issue.Title, cw) {
		if i == 0 && strings.HasPrefix(l, ref) {
			out = append(out, " "+line.Bold(true).Render(ref)+stBold.Render(strings.TrimPrefix(l, ref)))
			continue
		}
		out = append(out, " "+stBold.Render(l))
	}

	gap := 1
	if cw >= minWidth {
		gap = 2
	}
	word := stBold.Render(it.Stage.String())
	if it.KeptOpen {
		word += stFaint.Render(" " + g.Sep + " kept open")
	}
	out = append(out, "", " "+track(g, it, gap)+"  "+word, " "+stFaint.Render(legend(gap)))
	if c := courtLines(g, it, cw); len(c) > 0 {
		out = append(out, c...)
	}
	return out
}

// lineOf picks the ink an item draws in: its first routed repo, else its app,
// else its own project path (the dashboard's rule, so the colour matches).
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

// track draws the stage line: done stations filled, the current one the
// train (an interchange ring across repos, amber when blocked), the rest
// hollow; gap is the connector length between stations.
func track(g glyphs, it *domain.WorkItem, gap int) string {
	line := ink(lineOf(it))
	var b strings.Builder
	for i := range domain.Stations {
		cur := domain.Stage(i)
		done := it.Stage == domain.StageDone || cur < it.Stage
		if i > 0 {
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

// legend names the stations under the track, each initial under its station:
// two letters at the long gap (pitch three), one at the short.
func legend(gap int) string {
	initials := [...]string{"as", "st", "br", "dr", "ci", "rv", "mg"}
	var b strings.Builder
	for i, s := range initials {
		if gap == 1 {
			s = s[:1]
		}
		if i < len(initials)-1 {
			s += strings.Repeat(" ", gap+1-len(s))
		}
		b.WriteString(s)
	}
	return strings.TrimRight(b.String(), " ")
}

// courtLines is "court · reason", wrapped at word boundaries. Waiting courts
// carry the warn glyph and bold, a failed pipeline is red with the failure
// glyph, so none of it needs colour to read.
func courtLines(g glyphs, it *domain.WorkItem, cw int) []string {
	var lead string
	switch it.Court {
	case "":
		return nil
	case domain.CourtYou:
		lead = stBold.Render("you")
	case domain.CourtBlocked:
		lead = stWarn.Bold(true).Render(g.Warn + " blocked")
	case domain.CourtUpstream:
		lead = stWarn.Bold(true).Render(g.Warn + " waits")
	default:
		lead = stFaint.Render(string(it.Court))
	}
	if it.Reason == "" {
		return []string{" " + lead}
	}
	sep := " " + g.Sep + " "
	w0 := lipgloss.Width(lead) + lipgloss.Width(sep)
	failed := strings.HasPrefix(it.Reason, "pipeline failed")
	text := it.Reason
	if failed {
		text = g.Bad + " " + text
	}
	var out []string
	for i, l := range wrap(text, max(cw-w0, 10)) {
		switch {
		case failed:
			l = stBad.Bold(true).Render(l)
		case it.Court == domain.CourtYou:
		default:
			l = stFaint.Render(l)
		}
		if i == 0 {
			out = append(out, " "+lead+stFaint.Render(sep)+l)
		} else {
			out = append(out, " "+strings.Repeat(" ", w0)+l)
		}
	}
	return out
}

// --- facts ----------------------------------------------------------------------

func factLines(s Snapshot, g glyphs, cw int) []string {
	it := &s.Item
	var out []string
	row := func(key, val string) {
		for i, l := range wrap(val, cw-keyW) {
			k := strings.Repeat(" ", keyW)
			if i == 0 {
				k = stFaint.Render(fmt.Sprintf("%-*s", keyW, key))
			}
			out = append(out, " "+k+l)
		}
	}
	rowStyled := func(key string, parts ...string) {
		out = append(out, " "+stFaint.Render(fmt.Sprintf("%-*s", keyW, key))+strings.Join(parts, ""))
	}

	if it.Weight != nil {
		row("weight", fmt.Sprintf("%dw", *it.Weight))
	} else {
		rowStyled("weight", stFaint.Render("unweighted"))
	}
	if it.Sprint != nil {
		row("sprint", sprintText(it, s.Window, g))
	} else {
		rowStyled("sprint", stFaint.Render("no sprint"))
	}
	switch {
	case it.Type == "" && len(it.Apps) == 0:
		rowStyled("type", stWarn.Render(g.Warn+" untriaged"))
	default:
		typ := stWarn.Render(g.Warn + " no type")
		if it.Type != "" {
			typ = it.Type
		}
		apps := stWarn.Render(g.Warn + " no app")
		if len(it.Apps) > 0 {
			apps = strings.Join(it.Apps, ", ")
		}
		rowStyled("type", typ, stFaint.Render(" "+g.Sep+" "), apps)
	}
	switch {
	case it.Workflow == "":
		rowStyled("workflow", stFaint.Render(g.T(g.T(g.T("–")))))
	case it.Drift && it.WantWorkflow != "":
		row("workflow", it.Workflow+" "+stWarn.Render(g.Warn+" stage says "+it.WantWorkflow))
	default:
		row("workflow", it.Workflow)
	}
	row("state", it.Issue.State)
	if len(it.Issue.Assignees) == 0 {
		rowStyled("assignees", stFaint.Render("unassigned"))
	} else {
		names := make([]string, len(it.Issue.Assignees))
		for i, a := range it.Issue.Assignees {
			names[i] = a.Username
		}
		row("assignees", strings.Join(names, ", "))
	}
	return out
}

// sprintText is the sprint title, its last day and, for the current sprint,
// the working days left (amber at one or none).
func sprintText(it *domain.WorkItem, win domain.Window, g glyphs) string {
	sp := it.Sprint
	parts := []string{sp.Title}
	if !sp.Due.IsZero() {
		parts = append(parts, stFaint.Render("ends "+sp.Due.Format("Mon 2 Jan")))
	}
	if win.Current != nil && win.Current.Key == sp.Key {
		left := fmt.Sprintf("%dd left", win.DaysLeft)
		if win.DaysLeft <= 1 {
			left = stWarn.Render(left)
		}
		parts = append(parts, left)
	}
	return strings.Join(parts, " "+g.Sep+" ")
}

// --- code: child issues and their merge requests -------------------------------

func codeGuide(it *domain.WorkItem, g glyphs) string {
	if len(it.Children) == 0 && len(it.MRs) == 0 {
		return "not started"
	}
	var parts []string
	if n := len(it.Children); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "issue")))
	}
	if n := len(it.MRs); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "MR")))
	}
	return strings.Join(parts, " "+g.Sep+" ")
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// group is a child issue with the merge requests of its repo that belong to
// it; child is nil for merge requests no child issue claims.
type group struct {
	child *forge.Issue
	mrs   []forge.MR
}

// groupMRs files each merge request under the child issue of the same repo.
// With several children in one repo an MR goes to the one its branch, title
// or description names (#N); one that names none stays with the repo's first
// child.
func groupMRs(it *domain.WorkItem) []group {
	used := make([]bool, len(it.MRs))
	var groups []group
	for ci := range it.Children {
		c := &it.Children[ci]
		siblings := 0
		for _, o := range it.Children {
			if o.Ref.Project == c.Ref.Project {
				siblings++
			}
		}
		grp := group{child: c}
		for i, mr := range it.MRs {
			if used[i] || mr.Ref.Project != c.Ref.Project {
				continue
			}
			if siblings > 1 && !mentions(mr, c.Ref.IID) && (!mentionsNone(mr, it.Children) || !firstInProject(it.Children, ci)) {
				continue
			}
			used[i] = true
			grp.mrs = append(grp.mrs, mr)
		}
		groups = append(groups, grp)
	}
	var rest group
	for i, mr := range it.MRs {
		if !used[i] {
			rest.mrs = append(rest.mrs, mr)
		}
	}
	if len(rest.mrs) > 0 {
		groups = append(groups, rest)
	}
	return groups
}

func firstInProject(kids []forge.Issue, i int) bool {
	for j := range i {
		if kids[j].Ref.Project == kids[i].Ref.Project {
			return false
		}
	}
	return true
}

func mentions(mr forge.MR, iid int) bool {
	re := regexp.MustCompile(`(?:^|\D)` + fmt.Sprint(iid) + `(?:\D|$)`)
	return re.MatchString(mr.SourceBranch) || re.MatchString(mr.Title) || re.MatchString(mr.Description)
}

func mentionsNone(mr forge.MR, kids []forge.Issue) bool {
	for _, k := range kids {
		if k.Ref.Project == mr.Ref.Project && mentions(mr, k.Ref.IID) {
			return false
		}
	}
	return true
}

func codeLines(it *domain.WorkItem, g glyphs, cw int) []string {
	groups := groupMRs(it)
	if len(groups) == 0 {
		return []string{" " + stFaint.Render("no child issue yet")}
	}
	var out []string
	for gi, grp := range groups {
		if gi > 0 {
			out = append(out, "")
		}
		repo := ""
		switch {
		case grp.child != nil:
			repo = repoOf(grp.child.Ref.Project)
		case len(grp.mrs) > 0:
			repo = repoOf(grp.mrs[0].Ref.Project)
		}
		bar := " " + ink(repo).Render(g.Vertical) + " "
		inner := cw - 2
		emit := func(lines ...string) {
			for _, l := range lines {
				out = append(out, bar+l)
			}
		}
		if grp.child != nil {
			c := grp.child
			ref := shortRef(c.Ref)
			title := c.Title
			if c.State == "closed" {
				title += " (closed)"
			}
			for i, l := range wrap(ref+"  "+title, inner) {
				if i == 0 && strings.HasPrefix(l, ref) {
					emit(ink(repo).Bold(true).Render(ref) + stBold.Render(strings.TrimPrefix(l, ref)))
				} else {
					emit(stBold.Render(l))
				}
			}
		} else {
			emit(stFaint.Render(repo + " " + g.Sep + " no child issue"))
		}
		if len(grp.mrs) == 0 {
			emit(stFaint.Render("no MR yet"))
		}
		for mi, mr := range grp.mrs {
			if mi > 0 {
				emit("")
			}
			emit(mrLines(mr, g, inner)...)
		}
	}
	return out
}

func repoOf(project string) string {
	if i := strings.LastIndex(project, "/"); i >= 0 {
		return project[i+1:]
	}
	return project
}

func shortRef(ref forge.Ref) string {
	r := repoOf(ref.Project) + "#"
	if ref.Kind == forge.KindMR {
		r = repoOf(ref.Project) + "!"
	}
	return fmt.Sprintf("%s%d", r, ref.IID)
}

// mrLines is one merge request: number, state and branch; then pipeline,
// threads, approvals and conflicts as glyph-and-word chips; then the URL on a
// faint line.
func mrLines(mr forge.MR, g glyphs, w int) []string {
	chips := []string{stBold.Render(fmt.Sprintf("!%d", mr.Ref.IID))}
	open := mr.State == "opened"
	switch {
	case mr.State == "merged":
		m := g.OK + " merged"
		if mr.MergedAt != nil {
			m += " " + mr.MergedAt.Format("2 Jan")
		}
		chips = append(chips, stOK.Render(m))
	case mr.State == "closed":
		chips = append(chips, stFaint.Render(g.Bad+" closed"))
	case mr.Draft:
		chips = append(chips, stFaint.Render("draft"))
	default:
		chips = append(chips, "open")
	}
	if mr.SourceBranch != "" {
		chips = append(chips, stFaint.Render(ansi.Truncate(mr.SourceBranch, max(w-12, 8), g.Ell)))
	}
	out := packChips(chips, g, w)

	var status []string
	if open {
		status = append(status, pipelineChip(mr, g))
		if mr.HasConflicts {
			status = append(status, stBad.Bold(true).Render(g.Bad+" conflicts"))
		}
		if mr.Unresolved > 0 {
			status = append(status, stWarn.Bold(true).Render(fmt.Sprintf("%s %d unresolved %s", g.Thread, mr.Unresolved, plural(mr.Unresolved, "thread"))))
		} else {
			status = append(status, stFaint.Render("no open threads"))
		}
		if a := approvalChip(mr, g); a != "" {
			status = append(status, a)
		}
	}
	if len(status) > 0 {
		out = append(out, packChips(status, g, w)...)
	}
	if mr.WebURL != "" {
		for _, l := range strings.Split(ansi.Wrap(mr.WebURL, w, "/"), "\n") {
			out = append(out, stFaint.Render(l))
		}
	}
	return out
}

func pipelineChip(mr forge.MR, g glyphs) string {
	if mr.Pipeline == nil {
		return stFaint.Render("no pipeline")
	}
	switch st := mr.Pipeline.Status; st {
	case "success":
		return stOK.Render(g.OK + " pipeline passed")
	case "failed":
		return stBad.Bold(true).Render(g.Bad + " pipeline failed")
	case "running":
		return stWarn.Render(g.Sync + " pipeline running")
	case "canceled", "canceling":
		return stFaint.Render(g.Bad + " pipeline canceled")
	case "skipped":
		return stFaint.Render(g.ToCome + " pipeline skipped")
	case "manual":
		return stFaint.Render(g.Ahead + " pipeline manual")
	case "pending", "created", "waiting_for_resource", "preparing", "scheduled":
		return stFaint.Render(g.Ahead + " pipeline pending")
	default:
		return stFaint.Render(g.Ahead + " pipeline " + st)
	}
}

func approvalChip(mr forge.MR, g glyphs) string {
	n := len(mr.Approvals.ApprovedBy)
	switch {
	case mr.Approvals.Required > 0:
		s := fmt.Sprintf("%d/%d approved", n, mr.Approvals.Required)
		if n >= mr.Approvals.Required {
			return stOK.Render(g.OK + " " + s)
		}
		return s
	case n > 0:
		return fmt.Sprintf("%d approved", n)
	}
	return ""
}

// packChips lays chips on lines joined by a faint separator, breaking between
// chips; a chip wider than the line is cut with an ellipsis.
func packChips(chips []string, g glyphs, w int) []string {
	sep := stFaint.Render(" " + g.Sep + " ")
	sw := lipgloss.Width(sep)
	var out []string
	cur, curW := "", 0
	for _, c := range chips {
		cwid := lipgloss.Width(c)
		if cwid > w {
			c, cwid = ansi.Truncate(c, w, g.Ell), w
		}
		switch {
		case cur == "":
			cur, curW = c, cwid
		case curW+sw+cwid <= w:
			cur, curW = cur+sep+c, curW+sw+cwid
		default:
			out = append(out, cur)
			cur, curW = c, cwid
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// --- description, acceptance, notes --------------------------------------------

func progressGuide(done, total int, word string) string {
	if total == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d %s", done, total, word)
}

// docLines renders markdown (description, acceptance list, notes) through
// glamour, in the Interchange style: weight for emphasis, faint quotes and
// addresses, no background; NO_COLOR and the ascii glyph set are honoured.
// Every line is indented one cell, like the rest of the pane.
func docLines(text string, cw int, g glyphs) []string {
	opts := mdrender.FromEnv(max(cw, 8), g == asciiGlyphs)
	var out []string
	for _, l := range mdrender.Render(text, opts) {
		if l == "" {
			out = append(out, "")
			continue
		}
		out = append(out, " "+l)
	}
	return out
}

// --- footer ---------------------------------------------------------------------

// footer is the faint last line: when the cache was last synced, then the
// keys (dropped from the right while they do not fit).
func footer(s Snapshot, w int, now time.Time, keys []string) string {
	synced := "never synced"
	if !s.SyncedAt.IsZero() {
		synced = "synced " + ago(now, s.SyncedAt)
	}
	groups := append([]string{synced}, keys...)
	for len(groups) > 1 && lipgloss.Width(" "+strings.Join(groups, "   ")) > w {
		groups = groups[:len(groups)-1]
	}
	return " " + stFaint.Render(strings.Join(groups, "   "))
}

// ago is a relative time in words.
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2 Jan")
	}
}
