package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/budget"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

// Two read-only AI texts about a Home item, from the palette (`:`): a digest of
// the comments that are new since you last looked (`ade ai digest`), and a
// stand-in description for its issues that have none (`ade ai standin`). Both
// go through ai.Service like the CLI, so the result cache, pre-flight, caps
// and audit apply, and neither writes anything to the forge: the digest only
// moves the local last-seen marker, the stand-in lives only in the item's
// record and is marked as an AI stand-in wherever it is shown.

type aiTextKind int

const (
	aiTextDigest  aiTextKind = iota // the comments new since you last looked
	aiTextStandIn                   // a stand-in description for an empty one
)

func (k aiTextKind) title() string {
	if k == aiTextStandIn {
		return "AI stand-in description"
	}
	return "AI digest"
}

// aiTextSection is the answer for one issue or MR of the item.
type aiTextSection struct {
	ref   forge.Ref
	title string // the issue's or MR's title, when it was read
	guide string // guide words of the section rule
	text  string // the answer
	note  string // a faint closing line: model and cost, or what was left alone
	warn  string // why there is no answer
}

type aiTextDialog struct {
	kind     aiTextKind
	item     *domain.WorkItem
	forgeKey string
	refs     []forge.Ref // what is asked about, in order
	next     int         // the first ref not answered yet
	secs     []aiTextSection
	pf       *ai.Preflight // the next ref's call waits for a yes
	busy     bool
	scroll   int
	// notice is shown instead of sections when nothing is asked.
	notice string
}

// aiTextMsg is the answer of one run of the dialog's command: the sections it
// finished, and where it stopped.
type aiTextMsg struct {
	d    *aiTextDialog
	secs []aiTextSection
	next int
	pf   *ai.Preflight
}

// openAIText is the palette entry of both texts, on the selected Home item.
func (m model) openAIText(kind aiTextKind) (model, tea.Cmd) {
	if m.page != pageHome || m.mr != nil || m.isAgilePage() || m.homeSel >= len(m.homeRows) || m.homeRows[m.homeSel].item == nil || m.ait != nil {
		return m, nil
	}
	it := m.homeRows[m.homeSel].item
	d := &aiTextDialog{kind: kind, item: it}
	for _, r := range m.results {
		if r.Project == it.Project && r.Forge != "" {
			d.forgeKey = r.Forge
		}
	}
	m.ait = d
	switch kind {
	case aiTextDigest:
		d.refs = digestRefs(it)
	case aiTextStandIn:
		switch {
		case !m.aiOn():
			d.notice = "AI is off or has no profile (ai.mode, ai.profile): a stand-in needs a model. Nothing was sent."
			return m, nil
		default:
			d.refs = standInRefs(it)
		}
		if len(d.refs) == 0 {
			d.notice = "Every issue of this item has a description: no stand-in needed. Nothing was sent."
			return m, nil
		}
	}
	d.busy = true
	return m, m.aiTextCmd(d, false)
}

// digestRefs are the places an item's comments live: its MRs (the focus one
// first), its tracker issue and its child issues.
func digestRefs(it *domain.WorkItem) []forge.Ref {
	var refs []forge.Ref
	add := func(r forge.Ref) {
		for _, have := range refs {
			if have == r {
				return
			}
		}
		refs = append(refs, r)
	}
	for _, mr := range it.MRs {
		add(mr.Ref)
	}
	add(it.Ref)
	for _, c := range it.Children {
		add(c.Ref)
	}
	return refs
}

// standInRefs are the item's issues that have no description. The cache
// carries every description, so an issue with one is not even read.
func standInRefs(it *domain.WorkItem) []forge.Ref {
	var refs []forge.Ref
	if strings.TrimSpace(it.Issue.Description) == "" {
		refs = append(refs, it.Ref)
	}
	for _, c := range it.Children {
		if strings.TrimSpace(c.Description) == "" {
			refs = append(refs, c.Ref)
		}
	}
	return refs
}

// aiTextCmd answers the refs from d.next on, off the UI loop. It stops at the
// first call whose pre-flight needs a yes; assume answers that.
func (m model) aiTextCmd(d *aiTextDialog, assume bool) tea.Cmd {
	ctx, a, mm := m.ctx, m.app, m
	if ctx == nil {
		ctx = context.Background()
	}
	kind, key, refs, from := d.kind, d.forgeKey, d.refs, d.next
	return func() tea.Msg {
		svc, err := ai.NewService(a)
		if err != nil {
			return aiTextMsg{d: d, secs: []aiTextSection{{warn: err.Error()}}, next: len(refs)}
		}
		svc.SprintStart = budget.SprintStart(a)
		var out []aiTextSection
		for i := from; i < len(refs); i++ {
			ref := refs[i]
			be, err := mm.backend(ctx, ref, key)
			if err != nil {
				out = append(out, aiTextSection{ref: ref, warn: err.Error()})
				continue
			}
			var sec aiTextSection
			var pf *ai.Preflight
			if kind == aiTextStandIn {
				sec, pf = standInSection(ctx, svc, be, key, ref, assume && i == from)
			} else {
				sec, pf = digestSection(ctx, svc, be, key, ref, assume && i == from)
			}
			if pf != nil {
				return aiTextMsg{d: d, secs: out, next: i, pf: pf}
			}
			out = append(out, sec)
		}
		return aiTextMsg{d: d, secs: out, next: len(refs)}
	}
}

func digestSection(ctx context.Context, svc *ai.Service, be forge.Forge, key string, ref forge.Ref, assume bool) (aiTextSection, *ai.Preflight) {
	sec := aiTextSection{ref: ref}
	in, err := ai.LoadDigestInput(ctx, be, ref)
	sec.title = in.Title
	if err != nil {
		sec.warn = aiCause(err)
		return sec, nil
	}
	res, err := svc.Digest(ctx, key, in, ai.DigestOpts{Assume: assume})
	var nc *ai.ErrNeedsConfirm
	if errors.As(err, &nc) {
		return sec, &nc.Preflight
	}
	since := "(first look)"
	if !res.Since.IsZero() {
		since = "since " + res.Since.Local().Format("2 Jan 15:04")
	}
	switch {
	case err != nil && res.Text == "":
		sec.warn = aiCause(err)
	case res.Count == 0:
		sec.guide = "no new comments " + since
	default:
		sec.guide = fmt.Sprintf("%s %s", countWord(res.Count, "new comment"), since)
		sec.text = res.Text
		switch {
		case res.Source == ai.DigestSourceList:
			sec.note = "listed as they are: no model was asked"
		case res.Run != nil && res.Run.CacheHit:
			sec.note = "AI · " + res.Run.Model + " · cached, $0"
		case res.Run != nil:
			sec.note = fmt.Sprintf("AI · %s · $%.4f", res.Run.Model, res.Run.CostUSD)
		}
		if res.Run != nil && res.Run.Truncated {
			sec.note += " · " + res.Run.CutShort() + ": these comments stay new"
		}
	}
	if err != nil && res.Text != "" {
		sec.warn = aiCause(err)
	}
	return sec, nil
}

func standInSection(ctx context.Context, svc *ai.Service, be forge.Forge, key string, ref forge.Ref, assume bool) (aiTextSection, *ai.Preflight) {
	sec := aiTextSection{ref: ref}
	in, err := ai.LoadStandInInput(ctx, be, ref)
	sec.title = in.Issue.Title
	if err != nil {
		sec.warn = aiCause(err)
		return sec, nil
	}
	job, err := ai.StandInJob(key, in)
	if errors.Is(err, ai.ErrHasDescription) {
		sec.guide, sec.note = "has a description", "no stand-in needed"
		return sec, nil
	}
	if err != nil {
		sec.warn = aiCause(err)
		return sec, nil
	}
	job.Assume = assume
	res, err := svc.Run(ctx, job)
	var nc *ai.ErrNeedsConfirm
	if errors.As(err, &nc) {
		return sec, &nc.Preflight
	}
	if err != nil {
		sec.warn = aiCause(err)
		return sec, nil
	}
	sec.guide, sec.text = ai.StandInLabel, res.Text
	if res.CacheHit {
		sec.note = "AI · " + res.Model + " · cached, $0"
	} else {
		sec.note = fmt.Sprintf("AI · %s · $%.4f", res.Model, res.CostUSD)
	}
	if res.Truncated {
		sec.note += " · " + res.CutShort()
	}
	return sec, nil
}

// aiTextUpdate routes the dialog's messages and, while it is open, every key
// but ctrl+c.
func (m model) aiTextUpdate(msg tea.Msg) (model, tea.Cmd, bool) {
	d := m.ait
	switch msg := msg.(type) {
	case aiTextMsg:
		if d == nil || msg.d != d {
			return m, nil, true
		}
		d.busy, d.pf = false, msg.pf
		d.secs = append(d.secs, msg.secs...)
		d.next = msg.next
		return m, nil, true
	case tea.KeyPressMsg:
		if d == nil || msg.String() == "ctrl+c" {
			return m, nil, false
		}
		next, cmd := m.aiTextKey(msg.String())
		return next, cmd, true
	}
	return m, nil, false
}

func (m model) aiTextKey(ks string) (model, tea.Cmd) {
	d := m.ait
	if d.pf != nil {
		switch ks {
		case "y", "enter":
			d.pf, d.busy = nil, true
			return m, m.aiTextCmd(d, true)
		case "n", "esc", "q":
			m.ait = nil
		}
		return m, nil
	}
	switch ks {
	case "esc", "q", "enter":
		m.ait = nil
	case "j", "down":
		d.scroll++
	case "k", "up":
		d.scroll = max(d.scroll-1, 0)
	case "pgdown", "ctrl+d", "space":
		d.scroll += 10
	case "pgup", "ctrl+u":
		d.scroll = max(d.scroll-10, 0)
	case "g", "home":
		d.scroll = 0
	}
	return m, nil
}

// --- view ---------------------------------------------------------------------

func (m model) aiTextOverlay(w, h int) string {
	d := m.ait
	if d == nil {
		return ""
	}
	dw := min(w, 100)
	inner := dw - 4
	g := m.g
	title := d.kind.title() + g.T(" · ") + d.item.Ref.String()
	var l []string
	l = append(l, stFaint.Render(ansi.Truncate(d.item.Issue.Title, inner, g.Ell)), "")
	var body []string
	if d.notice != "" {
		body = append(body, stFaint.Render(ansi.Wrap(d.notice, inner, "")))
	}
	for _, s := range d.secs {
		body = append(body, m.aiTextSectionLines(d.kind, s, inner)...)
	}
	if d.kind == aiTextStandIn && len(d.secs) > 0 {
		body = append(body, "", stFaint.Render(ansi.Wrap(ai.StandInNote, inner, "")))
	}
	room := max(6, h-14)
	d.scroll = clamp(d.scroll, 0, max(len(body)-room, 0))
	l = append(l, body[d.scroll:min(len(body), d.scroll+room)]...)
	if more := len(body) - d.scroll - room; more > 0 {
		l = append(l, stFaint.Render(fmt.Sprintf(g.T("… %d more lines (j scrolls)"), more)))
	}
	if d.busy {
		l = append(l, "", stFaint.Render(g.Sync+" "+g.T("asking")+g.Ell))
	}
	hint := keys("j/k", "scroll", "esc", "close")
	if d.pf != nil {
		why := strings.Join(d.pf.Confirm, "; ")
		l = append(l, "", stWarn.Render(ansi.Wrap(g.Warn+" "+why, inner, "")), stFaint.Render(ansi.Truncate(g.T(d.pf.Describe()), inner, g.Ell)))
		hint = keys("y", "ask", "n", "close")
	}
	l = append(l, "", hint)
	return m.dialog(title, l, dw)
}

func (m model) aiTextSectionLines(kind aiTextKind, s aiTextSection, inner int) []string {
	g := m.g
	head := s.ref.String()
	if s.title != "" {
		head += " " + s.title
	}
	l := []string{section(ansi.Truncate(head, max(10, inner-30), g.Ell), g.T(s.guide), inner, g)}
	if s.warn != "" {
		l = append(l, stWarn.Render(ansi.Wrap(g.Warn+" "+s.warn, inner, "")))
	}
	if s.text != "" {
		if kind == aiTextStandIn {
			l = append(l, stFaint.Render(g.Vertical+" "+ai.StandInLabel))
		}
		for _, ln := range m.mdLines(s.text, inner-2) {
			l = append(l, stFaint.Render(g.Vertical)+" "+ln)
		}
	}
	if s.note != "" {
		l = append(l, stFaint.Render(ansi.Truncate(g.T(s.note), inner, g.Ell)))
	}
	return append(l, "")
}

func (m model) aiTextOverlayLayer(w, h int) *lipgloss.Layer {
	return blankFieldLayer(m.g, m.aiTextOverlay(w, h), w, h)
}
