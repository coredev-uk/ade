package tui

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/viewport"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/commitmsg"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/joblog"
	"github.com/coredev-uk/ade/internal/review"
)

type modalKind int

const (
	modalNone modalKind = iota
	modalReply
	modalComment
	modalEditDraft
	modalDeny
	modalSubmit
	modalMerge
	modalLog
	modalAI
)

// severities are the choices of a comment, in cycling order. The first, none,
// posts the body as written.
var severities = []review.Severity{
	review.SeverityNone, review.SeverityBlocking, review.SeveritySuggestion, review.SeverityQuestion, review.SeverityNit,
}

func sevWord(s review.Severity) string {
	if s == review.SeverityNone {
		return "comment"
	}
	return string(s)
}

func sevIndex(s review.Severity) int {
	for i, v := range severities {
		if v == s {
			return i
		}
	}
	return 0
}

// lineTarget is the diff line a new comment lands on.
type lineTarget struct {
	path string // the file's path in the latest version
	line int    // number on side
	side review.Side
	text string // the line, for the dialog
}

func (t lineTarget) String() string {
	s := fmt.Sprintf("%s:%d", t.path, t.line)
	if t.side == review.SideOld {
		s += " (removed line)"
	}
	return s
}

// mrModal is the open dialog: an inline editor (reply, comment, edit, deny
// reason, review summary), the merge confirmation, or the job log viewer.
type mrModal struct {
	kind modalKind
	ed   editor
	warn string // a refusal shown inside the dialog

	thread string // reply: the discussion
	aiNote string // reply: set when the text is an AI draft
	aiCut  string // reply: set when the AI draft is cut short, so the user finishes it before posting

	ai aiKey // modalAI: the call waiting for a yes

	target  lineTarget // comment: where
	sev     int        // comment, edit: index into severities
	draftID string     // edit, deny: which draft

	approve bool // submit: also approve

	squash, remove, auto bool   // merge options
	prefix               string // merge: the commit prefix that applies to the item, "" for none

	job        forge.Job // log viewer
	logView    viewport.Model
	logLines   int  // lines in the log
	logTail    bool // open on the last lines: set until the first draw knows the height
	logCut     bool // the log's start was cut off: only the last logTail bytes were read
	logLoading bool
	logErr     error
	logRaw     []string // the lines without search highlights
	logTyping  bool     // the "/" input has focus
	logQuery   string
	logHits    []int // lines holding the query
	logHit     int   // index into logHits of the line last jumped to
}

// logOpen reports whether the page shows a job log.
func (p *mrPage) logOpen() bool { return p != nil && p.modal != nil && p.modal.kind == modalLog }

func (md *mrModal) setLog(msg mrResultMsg) {
	md.logLoading = false
	if msg.err != nil {
		md.logErr = msg.err
		return
	}
	jl, _ := msg.out.(jobLog)
	lines := joblog.Clean(jl.text)
	md.logView = viewport.New()
	md.logView.SetContentLines(lines)
	md.logRaw = lines
	md.logLines, md.logTail = len(lines), true
	md.logCut = len(jl.text) > logTail-utf8.UTFMax // the read stops at logTail bytes, on a character boundary
}

// logSearch applies the query: matches are marked in the text and the view
// jumps to the first one at or below the top line (wrapping to the first).
func (md *mrModal) logSearch(query string) {
	md.logQuery, md.logHits, md.logHit = query, nil, 0
	if query == "" {
		md.logView.SetContentLines(md.logRaw)
		return
	}
	needle := strings.ToLower(query)
	lines := make([]string, len(md.logRaw))
	for i, ln := range md.logRaw {
		lines[i] = ln
		if strings.Contains(strings.ToLower(ln), needle) {
			md.logHits = append(md.logHits, i)
			lines[i] = markMatches(ln, needle)
		}
	}
	md.logView.SetContentLines(lines)
	if i := slices.IndexFunc(md.logHits, func(l int) bool { return l >= md.logView.YOffset() }); i >= 0 {
		md.logHit = i
	}
	md.logJump()
}

// logStep moves to the next (d=1) or previous (d=-1) match, wrapping.
func (md *mrModal) logStep(d int) {
	if n := len(md.logHits); n > 0 {
		md.logHit = (md.logHit + d + n) % n
		md.logJump()
	}
}

// logJump puts the current match a quarter of the way down the view.
func (md *mrModal) logJump() {
	if len(md.logHits) == 0 {
		return
	}
	md.logView.SetYOffset(max(md.logHits[md.logHit]-md.logView.Height()/4, 0))
}

// markMatches reverses every case-insensitive occurrence of needle (lower
// case) in the plain line.
func markMatches(line, needle string) string {
	lower := strings.ToLower(line)
	if len(lower) != len(line) { // case folding changed the byte length: keep the line as it is
		return line
	}
	var b strings.Builder
	for {
		i := strings.Index(lower, needle)
		if i < 0 {
			break
		}
		b.WriteString(line[:i])
		b.WriteString(stFound.Render(line[i : i+len(needle)]))
		line, lower = line[i+len(needle):], lower[i+len(needle):]
	}
	b.WriteString(line)
	return b.String()
}

// logKey handles the open log's keys; typing is the "/" input.
func (md *mrModal) logKey(k tea.KeyPressMsg) {
	ks := k.String()
	if md.logTyping {
		switch ks {
		case "esc":
			md.logTyping = false
			md.logSearch("")
		case "enter":
			md.logTyping = false
		case "ctrl+u":
			md.logSearch("")
		case "backspace", "ctrl+h":
			if r := []rune(md.logQuery); len(r) > 0 {
				md.logSearch(string(r[:len(r)-1]))
			}
		default:
			if k.Text != "" && !strings.ContainsAny(k.Text, "\r\n\t") {
				md.logSearch(md.logQuery + k.Text)
			}
		}
		return
	}
	switch ks {
	case "j", "down":
		md.logView.ScrollDown(1)
	case "k", "up":
		md.logView.ScrollUp(1)
	case "g", "home":
		md.logView.GotoTop()
	case "G", "end":
		md.logView.GotoBottom()
	case "pgdown", "ctrl+d", "space":
		md.logView.ScrollDown(max(md.logView.Height()-1, 1))
	case "pgup", "ctrl+u":
		md.logView.ScrollUp(max(md.logView.Height()-1, 1))
	case "right", "l":
		md.logView.ScrollRight(8)
	case "left", "h":
		md.logView.ScrollLeft(8)
	case "/":
		md.logTyping = true
		md.logSearch("")
	case "n":
		md.logStep(1)
	case "N":
		md.logStep(-1)
	}
}

// --- keys ----------------------------------------------------------------------

func (m *model) modalKey(k tea.KeyPressMsg) tea.Cmd {
	p := m.mr
	md := p.modal
	ks := k.String()
	if md.kind == modalLog && md.logTyping {
		md.logKey(k) // esc ends the search input, not the viewer
		return nil
	}
	if ks == "esc" {
		p.modal = nil
		return nil
	}
	md.warn = ""
	switch md.kind {
	case modalReply:
		if ks == "enter" {
			body := strings.TrimSpace(md.ed.String())
			if body == "" {
				md.warn = "write a reply first"
				return nil
			}
			p.modal = nil
			return m.actReply(md.thread, body)
		}
		md.ed.handle(k)
	case modalComment, modalEditDraft:
		switch ks {
		case "enter":
			return m.commitComment(md)
		case "tab":
			md.sev = (md.sev + 1) % len(severities)
		case "shift+tab":
			md.sev = (md.sev + len(severities) - 1) % len(severities)
		default:
			md.ed.handle(k)
		}
	case modalDeny:
		if ks == "enter" {
			reason := strings.Join(strings.Fields(md.ed.String()), "-")
			p.modal = nil
			return m.denyDraft(md.draftID, reason)
		}
		md.ed.handle(k)
	case modalSubmit:
		switch ks {
		case "enter":
			return m.commitSubmit(md)
		case "tab":
			md.approve = !md.approve
		default:
			md.ed.handle(k)
		}
	case modalMerge:
		return m.mergeKey(md, ks)
	case modalAI:
		return m.aiConfirmKey(md, ks)
	case modalLog:
		md.logKey(k)
	}
	return nil
}

func (m *model) commitSubmit(md *mrModal) tea.Cmd {
	p := m.mr
	d := p.data
	sum := strings.TrimSpace(md.ed.String())
	n := len(sendable(d.drafts))
	if n == 0 && sum == "" && !md.approve {
		md.warn = "nothing to submit: add a comment, write a summary, or approve"
		return nil
	}
	p.modal = nil
	return m.actSubmit(sum, md.approve)
}

func (m *model) mergeKey(md *mrModal, ks string) tea.Cmd {
	v := mergeVerdict(m.mr.data.mr)
	switch ks {
	case "s":
		md.squash = !md.squash
	case "d":
		md.remove = !md.remove
	case "w":
		md.auto = !md.auto
	case "enter", "y":
		switch {
		case !v.ok && !v.autoOK:
			md.warn = "cannot merge: " + v.why
		case !v.ok && !md.auto:
			md.warn = v.why + ": turn on merge when the pipeline succeeds (w) or wait"
		default:
			m.mr.modal = nil
			return m.actMerge(forge.MergeOpts{Squash: md.squash, RemoveSourceBranch: md.remove, AutoMerge: md.auto}, md.prefix)
		}
	}
	return nil
}

// --- drawing -------------------------------------------------------------------

// dialog frames lines in the rounded overlay border. Below 100 columns it is
// the full width, so no cut fragments of the page show around it.
func (m model) dialog(title string, lines []string, w int) string {
	body := append([]string{stBold.Render(title), ""}, lines...)
	return lipgloss.NewStyle().Border(m.g.Border).BorderForeground(lipgloss.Color(colRule)).
		Padding(0, 1).Width(w).Render(strings.Join(body, "\n"))
}

// overlay draws the open dialog, or "" when none is open.
func (m model) mrOverlay(w, h int) string {
	p := m.mr
	if p == nil || p.modal == nil {
		return ""
	}
	md := p.modal
	if md.kind == modalLog {
		return m.logScreen(md, w, h)
	}
	dw := min(w, 100) // the Agent dialog's measure; blankFieldLayer centres it
	inner := dw - 4
	var title string
	var l []string
	editorRows := 5
	switch md.kind {
	case modalReply:
		title = m.g.T("Reply · ") + p.data.ref.String()
		for _, t := range p.data.threadList() {
			if t.d.ID == md.thread {
				l = append(l, stFaint.Render(m.g.Vertical)+" "+stFaint.Render(ansi.Truncate(t.where()+m.g.T(" · ")+t.first().Author.Display()+`: "`+oneLine(t.first().Body)+`"`, inner-3, m.g.Ell)), "")
				break
			}
		}
		if md.aiNote != "" {
			l = append(l, stFaint.Render(ansi.Wrap(md.aiNote, inner, "")))
		}
		if md.aiCut != "" {
			l = append(l, stWarn.Render(ansi.Wrap(md.aiCut, inner, "")))
		}
		l = append(l, md.ed.view(inner, editorRows, "your reply")...)
	case modalComment, modalEditDraft:
		title = m.g.T("Comment · ") + md.target.String()
		if md.kind == modalEditDraft {
			title = "Edit comment"
		}
		if md.target.text != "" {
			l = append(l, stFaint.Render(m.g.Vertical)+" "+stFaint.Render(ansi.Truncate(md.target.text, inner-3, m.g.Ell)), "")
		}
		l = append(l, m.severityChips(md.sev, inner), "")
		l = append(l, md.ed.view(inner, editorRows, "one point, problem first, at most two sentences")...)
	case modalDeny:
		title = "Deny draft"
		l = append(l, stFaint.Render("Optional one-word reason, kept in the audit log"), "")
		l = append(l, md.ed.view(inner, 1, "reason")...)
	case modalSubmit:
		title, l = m.submitDialog(md, inner)
	case modalMerge:
		title, l = m.mergeDialog(md, inner)
	case modalAI:
		title, l = m.aiModal(md, inner)
	}
	if md.warn != "" {
		l = append(l, "", stWarn.Render(m.g.Warn+" "+md.warn))
	}
	l = append(l, "", m.modalHint(md))
	d := m.dialog(title, l, dw)
	return d
}

func (m model) severityChips(sel, w int) string {
	var parts []string
	for i, s := range severities {
		word := sevWord(s)
		if i == sel {
			parts = append(parts, stBold.Render("["+word+"]"))
		} else {
			parts = append(parts, stFaint.Render(" "+word+" "))
		}
	}
	return ansi.Truncate(strings.Join(parts, " "), w, m.g.Ell)
}

func (m model) modalHint(md *mrModal) string {
	switch md.kind {
	case modalReply:
		return keys("enter", "post reply", "alt+enter", "newline", "esc", "cancel")
	case modalAI:
		return keys("enter", "send", "esc", "cancel")
	case modalComment:
		return keys("enter", "add draft", "tab", "severity", "alt+enter", "newline", "esc", "cancel")
	case modalEditDraft:
		return keys("enter", "save", "tab", "severity", "alt+enter", "newline", "esc", "cancel")
	case modalDeny:
		return keys("enter", "deny", "esc", "cancel")
	case modalSubmit:
		return keys("enter", "submit", "tab", "approve", "alt+enter", "newline", "esc", "cancel")
	case modalMerge:
		if v := mergeVerdict(m.mr.data.mr); !v.ok && !v.autoOK {
			return keys("esc", "close")
		}
		return keys("enter", "merge", "s", "squash", "d", "delete branch", "w", "when CI passes", "esc", "cancel")
	}
	return keys("j/k", "scroll", "h/l", "sideways", "g/G", "top/bottom", "esc", "close")
}

func checkbox(g glyphs, on bool, label string) string {
	box := "[ ]"
	if on {
		box = "[x]"
	}
	if on {
		return stBold.Render(box + " " + label)
	}
	return box + " " + label
}

// sendable is what Submit would send now: your drafts and the agent's accepted
// ones, not yet published.
func sendable(ds []review.Draft) []review.Draft {
	var out []review.Draft
	for _, d := range ds {
		if d.Selected() && !d.Published {
			out = append(out, d)
		}
	}
	return out
}

func (m model) submitDialog(md *mrModal, inner int) (string, []string) {
	d := m.mr.data
	n := len(sendable(d.drafts))
	pending, denied := 0, 0
	for _, dr := range d.drafts {
		switch {
		case dr.Source == review.SourceAgent && dr.State == review.StatePending:
			pending++
		case dr.State == review.StateDenied:
			denied++
		}
	}
	v := stFaint.Render(m.g.Vertical) + " "
	goes := "go"
	if n == 1 {
		goes = "goes"
	}
	l := []string{v + stBold.Render(fmt.Sprintf("%d %s", n, plural(n, "comment"))) + " " + goes + " out as one batch: one notification for the author"}
	if pending > 0 {
		l = append(l, v+stWarn.Render(fmt.Sprintf("%s %d agent %s still pending", m.g.Warn, pending, plural(pending, "draft")))+stFaint.Render(m.g.T(" · they stay drafts until triaged")))
	}
	if denied > 0 {
		l = append(l, v+stFaint.Render(fmt.Sprintf(m.g.T("%d denied · never posted"), denied)))
	}
	l = append(l, "", stFaint.Render("Summary (optional)"))
	l = append(l, md.ed.view(inner, 4, "an overall note, posted with the batch")...)
	l = append(l, "", checkbox(m.g, md.approve, "also approve "+shortHead(d.mr.HeadSHA)))
	return m.g.T("Submit review · ") + d.ref.String(), l
}

func shortHead(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func (m model) mergeDialog(md *mrModal, inner int) (string, []string) {
	d := m.mr.data
	mr := d.mr
	v := mergeVerdict(mr)
	vl := stFaint.Render(m.g.Vertical) + " "
	l := []string{vl + stBold.Render(mr.SourceBranch) + stFaint.Render(m.g.T(" → ")) + stBold.Render(mr.TargetBranch)}
	switch {
	case v.ok:
		l = append(l, vl+stOK.Render(m.g.OK+" mergeable")+stFaint.Render(m.g.T(" · head ")+shortHead(mr.HeadSHA)+" is what you reviewed"))
	case v.autoOK:
		l = append(l, vl+stWarn.Render(m.g.Warn+" not mergeable yet: "+v.why))
	default:
		l = append(l, vl+stBad.Render(m.g.Bad+" cannot merge: "+v.why))
	}
	if mr.Unresolved > 0 {
		l = append(l, vl+stFaint.Render(fmt.Sprintf("%d unresolved %s", mr.Unresolved, plural(mr.Unresolved, "thread"))))
	}
	l = append(l, vl+stFaint.Render(approvalsText(mr)+m.g.T(" · ")+ciText(m.g, mr.Pipeline)))
	if v.ok || v.autoOK {
		l = append(l, "",
			checkbox(m.g, md.squash, "s  squash commits"),
			checkbox(m.g, md.remove, "d  delete the source branch"),
			checkbox(m.g, md.auto, "w  merge when the pipeline succeeds"))
		if md.prefix != "" && md.squash {
			l = append(l, "", vl+stFaint.Render(ansi.Truncate("squash commit: "+commitmsg.Apply(md.prefix, mr.Title), max(10, inner-2), m.g.Ell)))
		}
	}
	return m.g.T("Merge · ") + d.ref.String() + m.g.T(" · ") + ansi.Truncate(mr.Title, max(10, inner-20), m.g.Ell), l
}

// logScreen is the open job log: the whole screen, a title line, the log and
// a hint line. The log is plain text in ADE's own ink (joblog.Clean).
func (m model) logScreen(md *mrModal, w, h int) string {
	g := m.g
	title := stBold.Render(g.T("Job log · ") + md.job.Name)
	rows := max(h-2, 1)
	var body []string
	switch {
	case md.logLoading:
		body = []string{stFaint.Render(spinnerFrame(g, m.mr.spin) + g.T(" reading the log…"))}
	case md.logErr != nil:
		body = []string{stBad.Render(g.Bad + " " + shortCause(md.logErr))}
	case md.logLines == 0:
		body = []string{stFaint.Render("The job has no log.")}
	default:
		md.logView.SetWidth(w)
		md.logView.SetHeight(rows)
		if md.logTail {
			md.logView.GotoBottom()
			md.logTail = false
		}
		top := md.logView.YOffset()
		rng := fmt.Sprintf(g.T(" · %d-%d of %d lines"), top+1, min(md.logLines, top+rows), md.logLines)
		cut := ""
		if md.logCut {
			cut = fmt.Sprintf(g.T(" · start cut off: only the last %d KiB"), logTail>>10)
		}
		search := ""
		if md.logQuery != "" {
			search = g.T(" · /") + md.logQuery + " "
			if n := len(md.logHits); n > 0 {
				search += fmt.Sprintf("%d of %d", md.logHit+1, n)
			} else {
				search += "not found"
			}
		}
		// What does not fit goes in this order: the cut-off note, the range,
		// then the job name; the match count stays.
		fits := func(parts ...string) bool { return ansi.StringWidth(strings.Join(parts, "")) <= w }
		switch {
		case fits(g.T("Job log · "), md.job.Name, rng, cut, search):
			title += stFaint.Render(rng + cut)
		case fits(g.T("Job log · "), md.job.Name, rng, search):
			title += stFaint.Render(rng)
		case search != "":
			title = ansi.Truncate(title, max(w-ansi.StringWidth(search), 0), g.Ell)
		default:
			title += stFaint.Render(rng + cut)
		}
		if search != "" {
			title += stWarn.Render(search)
		}
		body = strings.Split(md.logView.View(), "\n")
	}
	hint := keys("esc", "close", "j/k", "scroll", "pgup/pgdn", "page", "g/G", "ends", "h/l", "side", "/", "search", "n/N", "match")
	if md.logTyping {
		hint = stBold.Render("/") + md.logQuery + g.Cursor + "  " + keys("enter", "keep", "esc", "clear")
	}
	out := make([]string, 0, h)
	out = append(out, ansi.Truncate(title, w, g.Ell))
	for i := range rows {
		ln := ""
		if i < len(body) {
			ln = ansi.Truncate(body[i], w, "")
		}
		out = append(out, ln)
	}
	out = append(out, ansi.Truncate(hint, w, g.Ell))
	return strings.Join(out, "\n")
}

// spinnerFrame is the progress mark: braille dots, or | / - \ in the ascii
// glyph set.
func spinnerFrame(g glyphs, n int) string {
	frames := []string{g.T("⠋"), g.T("⠙"), g.T("⠹"), g.T("⠸"), g.T("⠼"), g.T("⠴"), g.T("⠦"), g.T("⠧"), g.T("⠇"), g.T("⠏")}
	if g.Selected == ">" {
		frames = []string{"|", "/", "-", "\\"}
	}
	return frames[n%len(frames)]
}
