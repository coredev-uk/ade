package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/forge"
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

	target  lineTarget // comment: where
	sev     int        // comment, edit: index into severities
	draftID string     // edit, deny: which draft

	approve bool // submit: also approve

	squash, remove, auto bool // merge options

	job        forge.Job // log viewer
	log        []string
	logTop     int
	logLoading bool
	logErr     error
}

func (md *mrModal) setLog(msg mrResultMsg) {
	md.logLoading = false
	if msg.err != nil {
		md.logErr = msg.err
		return
	}
	jl, _ := msg.out.(jobLog)
	md.log = strings.Split(strings.TrimRight(strings.ReplaceAll(jl.text, "\r", ""), "\n"), "\n")
	md.logTop = 1 << 30 // clamped when drawn: opens on the tail
}

// --- keys ----------------------------------------------------------------------

func (m *model) modalKey(k tea.KeyPressMsg) tea.Cmd {
	p := m.mr
	md := p.modal
	ks := k.String()
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
	case modalLog:
		switch ks {
		case "j", "down":
			md.logTop++
		case "k", "up":
			md.logTop = max(0, min(md.logTop, len(md.log))-1)
		case "g", "home":
			md.logTop = 0
		case "G", "end":
			md.logTop = 1 << 30
		case "pgdown", "ctrl+d", "space":
			md.logTop += 10
		case "pgup", "ctrl+u":
			md.logTop = max(0, min(md.logTop, len(md.log))-10)
		}
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
			return m.actMerge(forge.MergeOpts{Squash: md.squash, RemoveSourceBranch: md.remove, AutoMerge: md.auto})
		}
	}
	return nil
}

// --- drawing -------------------------------------------------------------------

// dialog frames lines in the rounded overlay border. Below 100 columns it is
// the full width, so no cut fragments of the page show around it.
func (m model) dialog(title string, lines []string, w int) string {
	body := append([]string{stBold.Render(title), ""}, lines...)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(colRule)).
		Padding(0, 1).Width(w).Render(strings.Join(body, "\n"))
}

// overlay draws the open dialog, or "" when none is open.
func (m model) mrOverlay(w, h int) string {
	p := m.mr
	if p == nil || p.modal == nil {
		return ""
	}
	dw := min(w, 100) // the Agent dialog's measure; blankFieldLayer centres it
	inner := dw - 4
	md := p.modal
	var title string
	var l []string
	editorRows := 5
	switch md.kind {
	case modalReply:
		title = "Reply · " + p.data.ref.String()
		for _, t := range p.data.threadList() {
			if t.d.ID == md.thread {
				l = append(l, stFaint.Render(m.g.Vertical)+" "+stFaint.Render(ansi.Truncate(t.where()+" · "+t.first().Author.Username+`: "`+oneLine(t.first().Body)+`"`, inner-3, "…")), "")
				break
			}
		}
		l = append(l, md.ed.view(inner, editorRows, "your reply")...)
	case modalComment, modalEditDraft:
		title = "Comment · " + md.target.String()
		if md.kind == modalEditDraft {
			title = "Edit comment"
		}
		if md.target.text != "" {
			l = append(l, stFaint.Render(m.g.Vertical)+" "+stFaint.Render(ansi.Truncate(md.target.text, inner-3, "…")), "")
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
	case modalLog:
		title, l = m.logDialog(md, inner, h)
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
	return ansi.Truncate(strings.Join(parts, " "), w, "…")
}

func (m model) modalHint(md *mrModal) string {
	switch md.kind {
	case modalReply:
		return keys("enter", "post reply", "alt+enter", "newline", "esc", "cancel")
	case modalComment:
		return keys("enter", "add draft", "tab", "severity", "alt+enter", "newline", "esc", "cancel")
	case modalEditDraft:
		return keys("enter", "save", "tab", "severity", "esc", "cancel")
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
	return keys("j/k", "scroll", "g/G", "top/bottom", "esc", "close")
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
		l = append(l, v+stWarn.Render(fmt.Sprintf("%s %d agent %s still pending", m.g.Warn, pending, plural(pending, "draft")))+stFaint.Render(" · they stay drafts until triaged"))
	}
	if denied > 0 {
		l = append(l, v+stFaint.Render(fmt.Sprintf("%d denied · never posted", denied)))
	}
	l = append(l, "", stFaint.Render("Summary (optional)"))
	l = append(l, md.ed.view(inner, 4, "an overall note, posted with the batch")...)
	l = append(l, "", checkbox(m.g, md.approve, "also approve "+shortHead(d.mr.HeadSHA)))
	return "Submit review · " + d.ref.String(), l
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
	l := []string{vl + stBold.Render(mr.SourceBranch) + stFaint.Render(" → ") + stBold.Render(mr.TargetBranch)}
	switch {
	case v.ok:
		l = append(l, vl+stOK.Render(m.g.OK+" mergeable")+stFaint.Render(" · head "+shortHead(mr.HeadSHA)+" is what you reviewed"))
	case v.autoOK:
		l = append(l, vl+stWarn.Render(m.g.Warn+" not mergeable yet: "+v.why))
	default:
		l = append(l, vl+stBad.Render(m.g.Bad+" cannot merge: "+v.why))
	}
	if mr.Unresolved > 0 {
		l = append(l, vl+stFaint.Render(fmt.Sprintf("%d unresolved %s", mr.Unresolved, plural(mr.Unresolved, "thread"))))
	}
	l = append(l, vl+stFaint.Render(approvalsText(mr)+" · "+ciText(m.g, mr.Pipeline)))
	if v.ok || v.autoOK {
		l = append(l, "",
			checkbox(m.g, md.squash, "s  squash commits"),
			checkbox(m.g, md.remove, "d  delete the source branch"),
			checkbox(m.g, md.auto, "w  merge when the pipeline succeeds"))
	}
	return "Merge · " + d.ref.String() + " · " + ansi.Truncate(mr.Title, max(10, inner-20), "…"), l
}

func (m model) logDialog(md *mrModal, inner, h int) (string, []string) {
	title := "Job log · " + md.job.Name
	rows := max(5, h-12)
	switch {
	case md.logLoading:
		return title, []string{stFaint.Render(spinnerFrame(m.g, m.mr.spin) + " reading the log tail…")}
	case md.logErr != nil:
		return title, []string{stBad.Render(m.g.Bad + " " + shortCause(md.logErr))}
	case len(md.log) == 0:
		return title, []string{stFaint.Render("The job has no log.")}
	}
	top := clamp(md.logTop, 0, max(0, len(md.log)-rows))
	md.logTop = top
	var l []string
	for _, ln := range md.log[top:min(len(md.log), top+rows)] {
		l = append(l, ansi.Truncate(stripCtl(ln), inner, "…"))
	}
	guide := fmt.Sprintf("%d-%d of %d lines · last %d KiB of the log", top+1, min(len(md.log), top+rows), len(md.log), logTail>>10)
	return title + stFaint.Render(" · "+guide), l
}

// stripCtl drops ANSI colour from CI output: the log is shown in ADE's own ink.
func stripCtl(s string) string { return ansi.Strip(s) }

// spinnerFrame is the progress mark: braille dots, or | / - \ in the ascii
// glyph set.
func spinnerFrame(g glyphs, n int) string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	if g.Selected == ">" {
		frames = []string{"|", "/", "-", "\\"}
	}
	return frames[n%len(frames)]
}
