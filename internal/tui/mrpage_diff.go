package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// The Diff tab: the file list, and the selected file's unified diff with line
// numbers on both sides, your drafts and the existing threads drawn inline
// under the line they are about. A removed line is numbered on the old side
// only, an added line on the new side only, an unchanged line on both.

const numW = 4 // cells of a line number

// fileStat counts added and removed lines.
func fileStat(f forge.DiffFile) (add, del int) {
	for _, l := range review.Lines(f) {
		switch l.Kind {
		case '+':
			add++
		case '-':
			del++
		}
	}
	return add, del
}

// fileKind is the one-letter status in the file list; the diff pane spells it
// out, and the section rule carries the legend.
func fileKind(f forge.DiffFile) string {
	switch {
	case f.NewFile:
		return "A"
	case f.Deleted:
		return "D"
	case f.Renamed || f.OldPath != f.NewPath:
		return "R"
	}
	return "M"
}

func fileWord(f forge.DiffFile) string {
	return map[string]string{"A": "added", "D": "deleted", "R": "renamed", "M": "modified"}[fileKind(f)]
}

// renameLabel is the path of a file, old → new for a rename: the shared
// directory stays out of the braces (limit/{bucket.go → token_bucket.go}).
func renameLabel(f forge.DiffFile) string {
	if f.OldPath == f.NewPath || f.OldPath == "" {
		return f.NewPath
	}
	od, of := splitDir(f.OldPath)
	nd, nf := splitDir(f.NewPath)
	if od == nd {
		return od + "{" + of + " → " + nf + "}"
	}
	return f.OldPath + " → " + f.NewPath
}

func splitDir(p string) (dir, file string) {
	i := strings.LastIndex(p, "/")
	return p[:i+1], p[i+1:]
}

// fileLabel fits a path into n cells, giving up the directory and the old
// name before the new basename: a rename reads limit/{a.go → b.go}, then
// {a.go → b.go}, a.go → b.go, → b.go; a plain path loses its left end first.
func fileLabel(f forge.DiffFile, n int) string {
	if n <= 1 {
		return ""
	}
	_, nf := splitDir(f.NewPath)
	var tries []string
	if f.OldPath != f.NewPath && f.OldPath != "" {
		od, of := splitDir(f.OldPath)
		nd, _ := splitDir(f.NewPath)
		tries = append(tries, renameLabel(f))
		if od == nd {
			tries = append(tries, "{"+of+" → "+nf+"}")
		}
		tries = append(tries, of+" → "+nf, "→ "+nf)
	} else {
		tries = append(tries, f.NewPath)
	}
	for _, t := range tries {
		if lipgloss.Width(t) <= n {
			return t
		}
	}
	tail := nf
	if len(tries) == 1 {
		tail = f.NewPath // a plain path: the directory goes first, then the name's head
	}
	if lipgloss.Width(tail) <= n {
		return tail
	}
	return ansi.TruncateLeft(tail, lipgloss.Width(tail)-n+1, "…")
}

func (m model) diffGuide() string {
	d := m.mr.data
	if d.diffL.st != loadOK {
		return ""
	}
	add, del := 0, 0
	for _, f := range d.diff.Files {
		a, r := fileStat(f)
		add, del = add+a, del+r
	}
	return fmt.Sprintf("%d %s +%d -%d %s A added M modified R renamed D deleted", len(d.diff.Files), plural(len(d.diff.Files), "file"), add, del, m.g.Sep)
}

func (m model) mrDiff(w, h int) string {
	p := m.mr
	d := p.data
	if ph, ok := m.wait(d.diffL, "the diff", w, h); !ok {
		return ph
	}
	if len(d.diff.Files) == 0 {
		return fit("   "+stFaint.Render("This merge request changes no files."), w, h)
	}
	sel := clamp(p.fileSel, 0, len(d.diff.Files)-1)
	if m.mrNarrow(w) {
		if p.focusDiff {
			return indentBlock(m.codePane(d.diff.Files[sel], w-1, h))
		}
		return m.fileLines(sel, w, h)
	}
	fw := clamp(w*3/10, 30, 44)
	return columns(stRule.Render(" │ "), h, m.fileLines(sel, fw, h), m.codePane(d.diff.Files[sel], w-fw-3, h))
}

func (m model) fileLines(sel, w, h int) string {
	p := m.mr
	d := p.data
	g := m.g
	var out []string
	for i, f := range d.diff.Files {
		mark := "   "
		if i == sel {
			mark = " " + stBold.Render(g.Selected) + " "
		}
		add, del := fileStat(f)
		right := stOK.Render(fmt.Sprintf("+%d", add)) + " " + stBad.Render(fmt.Sprintf("-%d", del))
		if n := len(m.draftsOn(f)); n > 0 {
			right += " " + stBold.Render(fmt.Sprintf("%s%d", g.Dirty, n))
		}
		label := fileLabel(f, w-3-2-lipgloss.Width(right)-1)
		if i == sel && !p.focusDiff {
			label = stBold.Render(label)
		}
		left := mark + stFaint.Render(fileKind(f)) + " " + label
		gap := max(1, w-lipgloss.Width(left)-lipgloss.Width(right)-1)
		out = append(out, left+strings.Repeat(" ", gap)+right)
	}
	top := 0
	if sel >= h-1 {
		top = sel - h + 2
	}
	return fit(strings.Join(out[min(top, len(out)):], "\n"), w, h)
}

// draftsOn lists the drafts anchored in f that will still matter (not denied).
func (m model) draftsOn(f forge.DiffFile) []review.Draft {
	var out []review.Draft
	for _, dr := range m.mr.data.drafts {
		if draftInFile(dr, f) && dr.State != review.StateDenied {
			out = append(out, dr)
		}
	}
	return out
}

func draftInFile(dr review.Draft, f forge.DiffFile) bool {
	return !dr.Overview() && (dr.Path == f.NewPath || dr.Path == f.OldPath)
}

// onLine reports whether a draft anchors to line l of its file.
func draftOnLine(dr review.Draft, l review.Line) bool {
	if dr.Side == review.SideOld {
		return l.Old > 0 && l.Old == dr.Line
	}
	return l.New > 0 && l.New == dr.Line
}

func threadOnLine(pos *forge.Position, l review.Line) bool {
	switch {
	case pos == nil:
		return false
	case pos.NewLine > 0:
		return l.New == pos.NewLine
	case pos.OldLine > 0:
		return l.New == 0 && l.Old == pos.OldLine
	}
	return false
}

func threadInFile(pos *forge.Position, f forge.DiffFile) bool {
	return pos != nil && (pos.Path == f.NewPath || pos.Path == f.OldPath || pos.OldPath == f.OldPath && pos.OldPath != "")
}

// codeLine draws one numbered line of a diff in w cells.
func (m model) codeLine(l review.Line, w int, cursor bool) string {
	num := func(n int) string {
		if n == 0 {
			return strings.Repeat(" ", numW)
		}
		return fmt.Sprintf("%*d", numW, n)
	}
	mark := " "
	if cursor {
		mark = stBold.Render(m.g.Selected)
	}
	sign, style := " ", lipgloss.NewStyle()
	switch l.Kind {
	case '+':
		sign, style = "+", stOK
	case '-':
		sign, style = "-", stBad
	}
	txt := ansi.Truncate(strings.ReplaceAll(l.Text, "\t", "    "), max(1, w-(numW*2+5)), "…")
	if cursor {
		style = style.Bold(true)
	}
	return mark + stFaint.Render(num(l.Old)+" "+num(l.New)) + " " + style.Render(sign+txt)
}

// diffContext draws the few lines around a thread's position, for the Threads
// tab, or nothing when the diff is not loaded or the line is not in it.
func (m model) diffContext(d *mrData, pos *forge.Position, w int) []string {
	if d.diffL.st != loadOK || pos == nil {
		return nil
	}
	f, ok := review.File(d.diff, pos.Path)
	if !ok {
		return nil
	}
	lines := review.Lines(f)
	at := -1
	for i, l := range lines {
		if threadOnLine(pos, l) {
			at = i
			break
		}
	}
	if at < 0 {
		return nil
	}
	var out []string
	for i := max(0, at-2); i <= min(len(lines)-1, at+1); i++ {
		out = append(out, m.codeLine(lines[i], w-1, i == at))
	}
	return out
}

// diffRows draws a file's lines with their inline drafts and threads. cur is
// the row of the cursor line.
func (m model) diffRows(f forge.DiffFile, w int) (rows []string, starts []bool, cur int) {
	p := m.mr
	d := p.data
	g := m.g
	lines := review.Lines(f)
	textW := max(10, w-(numW*2+5))
	indent := strings.Repeat(" ", numW*2+3)
	var prev *review.Line
	add := func(start bool, r ...string) { // starts marks the rows a view may open on
		for _, s := range r {
			rows = append(rows, s)
			starts = append(starts, start)
			start = false
		}
	}
	for i := range lines {
		l := lines[i]
		if prev != nil && !contiguous(*prev, l) {
			add(true, stFaint.Render(indent+g.Vertical+" "+g.ToCome+g.ToCome+g.ToCome))
		}
		prev = &lines[i]
		if i == p.lineCur {
			cur = len(rows)
		}
		add(true, m.codeLine(l, w, p.focusDiff && i == p.lineCur))
		for _, th := range d.threads {
			if threadInFile(th.Position, f) && threadOnLine(th.Position, l) {
				add(false, m.inlineThread(th, indent, textW)...)
			}
		}
		for _, dr := range d.drafts {
			if draftInFile(dr, f) && draftOnLine(dr, l) {
				add(false, m.draftBlock(dr, indent, textW)...)
			}
		}
	}
	return rows, starts, cur
}

// contiguous reports whether b follows a in the same hunk.
func contiguous(a, b review.Line) bool {
	return (a.New == 0 || b.New == 0 || b.New == a.New+1) && (a.Old == 0 || b.Old == 0 || b.Old == a.Old+1)
}

func (m model) inlineThread(th forge.Discussion, indent string, w int) []string {
	g := m.g
	n := th.Notes[0]
	state := stWarn.Render(g.Info + " open")
	if th.Resolved {
		state = stFaint.Render(g.OK + " resolved")
	}
	line := state + " " + stFaint.Render(n.Author.Username+": ") + stFaint.Render(oneLine(n.Body))
	return []string{indent + stRule.Render(g.Vertical) + " " + ansi.Truncate(line, w-2, "…")}
}

// draftBlock draws a draft under its line: who wrote it, its state (and the
// keys that triage it), then the body, wrapped.
func (m model) draftBlock(dr review.Draft, indent string, w int) []string {
	g := m.g
	var who, keysHint, sev string
	switch {
	case dr.State == review.StateDenied:
		who = stFaint.Render(fmt.Sprintf("%s %s draft %s denied · never posted", g.Dirty, dr.Source, g.Sep))
	case dr.Source == review.SourceAgent && dr.State == review.StatePending:
		who = stWarn.Render(fmt.Sprintf("%s agent draft %s pending triage", g.Dirty, g.Sep))
		keysHint = stFaint.Render("y accept · n deny · e edit")
	case dr.Source == review.SourceAgent:
		who = stBold.Render(fmt.Sprintf("%s agent draft %s accepted", g.Dirty, g.Sep))
	default:
		who = stBold.Render(g.Dirty + " your draft")
	}
	if dr.Severity == review.SeverityBlocking {
		sev = stWarn.Render(g.Warn + " blocking") // ▲ and amber, as in the Review list; the glyph survives NO_COLOR
	} else {
		sev = stFaint.Render(sevWord(dr.Severity))
	}
	if long(dr) {
		sev += stFaint.Render(" · ") + stWarn.Render("long")
	}
	bar := indent + stRule.Render(g.Vertical) + " "
	// The head reads who and state, the keys that triage it, then the severity
	// and `long`. When that does not fit it is packed again with the severity
	// ahead of the keys and wrapped by whole segments: triage reads the
	// severity first, so it is never cut.
	dot := stFaint.Render(" · ")
	segs := []string{who, keysHint, sev}
	if keysHint != "" && lipgloss.Width(who+dot+keysHint+dot+sev) > w-2 {
		segs = []string{who, sev, keysHint}
	}
	var heads []string
	line := ""
	for _, s := range segs {
		switch {
		case s == "":
		case line == "":
			line = s
		case lipgloss.Width(line+dot+s) <= w-2:
			line += dot + s
		default:
			heads = append(heads, line)
			line = s
		}
	}
	out := make([]string, 0, 8)
	for _, h := range append(heads, line) {
		out = append(out, bar+ansi.Truncate(h, w-2, "…"))
	}
	body := strings.Split(lipgloss.Wrap(dr.Body, min(100, max(10, w-2)), " "), "\n") // a reading measure, not the pane's width
	for i, ln := range body {
		if i == 4 {
			out = append(out, bar+stFaint.Render("…"))
			break
		}
		if dr.State == review.StateDenied {
			ln = stFaint.Render(ln)
		}
		out = append(out, bar+ln)
	}
	return out
}

// long flags a draft over the house limit: it should be trimmed before it is
// posted (docs/PLAN.md 6.9).
func long(dr review.Draft) bool { return len([]rune(dr.Body)) > 280 }

func (m model) codePane(f forge.DiffFile, w, h int) string {
	p := m.mr
	g := m.g
	add, del := fileStat(f)
	head := stBold.Render(ansi.Truncate(renameLabel(f), w-20, "…")) + stFaint.Render(fmt.Sprintf(" · %s · +%d -%d", fileWord(f), add, del))
	rows, starts, cur := m.diffRows(f, w)
	if len(rows) == 0 {
		msg := "The forge withheld this file's diff (collapsed or too large): open it on the forge."
		if f.Renamed && f.Diff == "" {
			msg = "Renamed without changes."
		}
		return fit(head+"\n"+stFaint.Render(msg), w, h)
	}
	body := max(1, h-1)
	top := max(0, min(cur-body/3, len(rows)-body))
	if !p.focusDiff {
		top = 0
	}
	for top > 0 && !starts[top] {
		top-- // never open on the tail of a draft or thread: keep its line with it
	}
	out := []string{head}
	out = append(out, rows[top:min(len(rows), top+body)]...)
	if top+body < len(rows) {
		out[len(out)-1] = stFaint.Render(fmt.Sprintf("%s %d more rows %s j/k scrolls", g.Down, len(rows)-top-body+1, g.Sep))
	}
	return fit(strings.Join(out, "\n"), w, h)
}
