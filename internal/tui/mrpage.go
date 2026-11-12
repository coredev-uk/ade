package tui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// The work item / MR page (docs/PLAN.md 6.2) and the review surface (6.9).
// Data is read lazily from the forge (a tab loads when first shown), cached per
// MR in memory so reopening is instant, and refreshed in the background when
// older than mrStale. Every write runs off the UI loop; its progress shows in
// the status strip, its result as a pop-up through the event bus, and the
// forge writes are audited by review.Audited.

const (
	mrStale   = 30 * time.Second
	logTail   = 16 << 10 // bytes of a job log kept for the viewer
	spinEvery = 120 * time.Millisecond
)

type mrTab int

const (
	tabOverview mrTab = iota
	tabThreads
	tabDiff
	tabReview
	tabCI
	tabHistory
	tabCount
)

func (t mrTab) title() string {
	return [...]string{"Overview", "Threads", "Diff", "Review", "CI", "History"}[t]
}

type loadState int

const (
	loadIdle loadState = iota
	loadBusy
	loadOK
	loadFailed
)

// load is the state of one lazily read piece of an MR.
type load struct {
	st  loadState
	err error
}

func (l load) busy() bool { return l.st == loadBusy }

func settled(err error) load {
	if err != nil {
		return load{loadFailed, err}
	}
	return load{loadOK, nil}
}

// mrData is everything read about one MR. It outlives the page showing it, so
// reopening an MR (or flipping back from Home) never waits on the network.
type mrData struct {
	ref      forge.Ref
	project  string // the ADE project that owns the MR
	forgeKey string

	mr  forge.MR
	mrL load // the preliminary MR (from Home's sync) shows while it loads

	threads  []forge.Discussion
	threadsL load

	diff  forge.Diff
	diffL load

	jobs  []forge.Job
	jobsL load

	// session validates and persists drafts against diff; nil until the diff
	// has loaded. drafts is read from the store on every change, so drafts
	// added by another ade process (ade review add, an agent) show up.
	session *review.Session
	drafts  []review.Draft

	loadedAt time.Time
	busy     string // the write in flight ("merging"), empty when idle
	outage   bool   // a failed read already raised its toast; later ones stay quiet

	ai map[aiKey]*aiText // answers asked of the model, per kind and thread
}

func (d *mrData) anyBusy() bool {
	return d.busy != "" || d.mrL.busy() || d.threadsL.busy() || d.diffL.busy() || d.jobsL.busy() || d.aiBusy()
}

func (d *mrData) refreshDrafts(st *review.Store) {
	if st == nil {
		return
	}
	if ds, err := st.List(d.ref); err == nil {
		d.drafts = ds
		if d.session != nil {
			d.session.Drafts = ds
		}
	}
}

// mrPage is the view state of the open page: which MR of the item, the tab,
// the selection and scroll position within each tab, and the open dialog.
type mrPage struct {
	project  string
	forgeKey string
	item     *domain.WorkItem // nil when opened from Reviews requested
	mrs      []forge.MR       // the item's MRs, opened first
	cur      int              // index into mrs
	data     *mrData          // nil while the item has no MR
	tab      mrTab

	ovTop    int  // overview scroll
	rawDesc  bool // Overview shows the raw description instead of the AI summary
	thSel    int  // selected thread (index into threadList)
	thDetail bool // narrow: the thread's conversation instead of the list

	fileSel   int
	focusDiff bool // the diff pane (not the file list) has the cursor
	lineCur   int  // index into the file's numbered lines

	revSel int
	ciSel  int

	hist        map[string][]audit.Entry // History: entries per audit item, as they arrive
	histWant    int                      // how many items to wait for
	histEntries []audit.Entry            // merged, newest first
	histL       load
	modal       *mrModal
	spin        int
	spinning    bool
}

// mrBackends caches the forge driver per forge key: opening one may run a
// token command, so it happens once.
type mrBackends struct {
	mu sync.Mutex
	m  map[string]review.Backend
}

func (c *mrBackends) get(key string, open func() (review.Backend, error)) (review.Backend, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if be, ok := c.m[key]; ok {
		return be, nil
	}
	be, err := open()
	if err != nil {
		return nil, err
	}
	if c.m == nil {
		c.m = map[string]review.Backend{}
	}
	c.m[key] = be
	return be, nil
}

// --- messages ------------------------------------------------------------------

// mrResultMsg is the answer to one background call. what names it (mr,
// threads, diff, jobs, log for reads; reply, resolve, approve, unapprove,
// ready, merge, retry, submit for writes); out carries the value.
type mrResultMsg struct {
	ref  forge.Ref
	what string
	verb string // writes: the action in progress ("merging"), for messages
	err  error
	out  any
	ai   aiKey // what == "ai": which answer this is
}

type mrSpinMsg struct{}

type mrReload int

const (
	relMR mrReload = 1 << iota
	relThreads
	relJobs
)

// mrWrote is what a successful write returns: the pop-up title and what to
// read again.
type mrWrote struct {
	done   string
	reload mrReload
	result *review.Result
}

// --- opening ---------------------------------------------------------------------

// openHomeRow opens the MR page for the selected Home row: the item's MRs, or
// the review-requested MR itself.
func (m *model) openHomeRow() tea.Cmd {
	if m.homeSel >= len(m.homeRows) {
		return nil
	}
	r := m.homeRows[m.homeSel]
	p := &mrPage{project: r.project, item: r.item}
	for i := range m.results {
		if m.results[i].Project == r.project {
			p.forgeKey = m.results[i].Forge
		}
	}
	if r.mr != nil {
		p.mrs = []forge.MR{*r.mr}
	} else if r.item != nil {
		p.mrs = append(p.mrs, r.item.MRs...)
		sort.SliceStable(p.mrs, func(i, j int) bool { // open MRs first
			return (p.mrs[i].State == "opened") && (p.mrs[j].State != "opened")
		})
	}
	m.mr, m.showDetail = p, false
	return m.mrSelect(0)
}

// mrSelect switches the page to its i-th MR, reading what is missing or stale.
func (m *model) mrSelect(i int) tea.Cmd {
	p := m.mr
	p.hist, p.histEntries, p.histL = nil, nil, load{}
	p.cur = i
	p.thSel, p.thDetail, p.fileSel, p.focusDiff, p.lineCur, p.revSel, p.ciSel, p.ovTop, p.rawDesc = 0, false, 0, false, 0, 0, 0, 0, false
	if len(p.mrs) == 0 {
		p.data = nil
		return nil
	}
	mr := p.mrs[i]
	key := mr.Ref.String()
	if m.mrCache == nil {
		m.mrCache = map[string]*mrData{}
	}
	d := m.mrCache[key]
	if d == nil {
		d = &mrData{ref: mr.Ref, project: p.project, forgeKey: p.forgeKey, mr: mr}
		m.mrCache[key] = d
	}
	p.data = d
	d.refreshDrafts(m.reviews)
	var cmds []tea.Cmd
	if !d.mrL.busy() && (d.mrL.st == loadIdle || time.Since(d.loadedAt) > mrStale) {
		cmds = append(cmds, m.loadMR(d), m.loadThreads(d))
	}
	cmds = append(cmds, m.ensureTab())
	return tea.Batch(cmds...)
}

// ensureTab reads what the current tab shows, once.
func (m *model) ensureTab() tea.Cmd {
	p := m.mr
	if p.tab == tabHistory && p.histL.st == loadIdle {
		return m.loadHistory()
	}
	d := p.data
	if d == nil {
		return nil
	}
	var cmds []tea.Cmd
	switch p.tab {
	case tabOverview:
		cmds = append(cmds, m.maybeSummary())
	case tabThreads, tabDiff, tabReview:
		if d.diffL.st == loadIdle {
			cmds = append(cmds, m.loadDiff(d))
		}
		if p.tab == tabThreads && d.threadsL.st == loadIdle {
			cmds = append(cmds, m.loadThreads(d))
		}
		if p.tab == tabReview {
			d.refreshDrafts(m.reviews)
		}
	case tabCI:
		if d.jobsL.st == loadIdle && d.mr.Pipeline != nil {
			cmds = append(cmds, m.loadJobs(d))
		}
	}
	return tea.Batch(cmds...)
}

func (m *model) setTab(t mrTab) tea.Cmd {
	m.mr.tab = t
	m.mr.focusDiff = false
	return m.ensureTab()
}

// mrReloadAll rereads the page's MR from the forge (U on the page).
func (m *model) mrReloadAll() tea.Cmd {
	cmds := []tea.Cmd{m.reloadHistory()}
	if d := m.mr.data; d != nil {
		d.outage = false // a retry that fails again is news
		cmds = append(cmds, m.mrRefetch(d, relMR|relThreads))
		if d.diffL.st != loadIdle {
			cmds = append(cmds, m.loadDiff(d))
		}
		if d.jobsL.st != loadIdle {
			cmds = append(cmds, m.loadJobs(d))
		}
		d.refreshDrafts(m.reviews)
	}
	return tea.Batch(cmds...)
}

// --- history ---------------------------------------------------------------------

// historyRefs are the audit items the History tab shows: the MR (forge writes
// and review decisions are recorded against it) and the work item (AI calls,
// plans and runs are).
func (p *mrPage) historyRefs() []string {
	var refs []string
	if p.data != nil {
		refs = append(refs, p.data.ref.String())
	}
	if p.item != nil {
		refs = append(refs, p.item.Ref.String())
	}
	return refs
}

// loadHistory reads the audit entries for the page's refs off the UI loop.
func (m *model) loadHistory() tea.Cmd {
	p := m.mr
	refs := p.historyRefs()
	p.hist, p.histWant, p.histL = map[string][]audit.Entry{}, len(refs), load{st: loadBusy}
	var cmds []tea.Cmd
	for _, r := range refs {
		cmds = append(cmds, loadHistory(m.mrAudit(), r, 200))
	}
	if len(cmds) == 0 {
		p.histL = load{st: loadOK}
	}
	return tea.Batch(cmds...)
}

// reloadHistory refreshes History when it has been shown, so a write just
// made appears in it.
func (m *model) reloadHistory() tea.Cmd {
	if m.mr == nil || m.mr.histL.st == loadIdle {
		return nil
	}
	return m.loadHistory()
}

func (m *model) onHistory(msg historyMsg) {
	p := m.mr
	if p == nil || p.hist == nil {
		return
	}
	if msg.err != nil {
		p.histL = load{st: loadFailed, err: msg.err}
		return
	}
	p.hist[msg.item] = msg.entries
	if len(p.hist) < p.histWant {
		return
	}
	p.histL = load{st: loadOK}
	var all []audit.Entry
	for _, es := range p.hist {
		all = append(all, es...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].TS.After(all[j].TS) })
	p.histEntries = all
}

// --- background calls ----------------------------------------------------------

func (m model) backend(ctx context.Context, ref forge.Ref, key string) (review.Backend, error) {
	open := func() (review.Backend, error) {
		if m.dial != nil {
			return m.dial(ctx, ref, key)
		}
		return review.OpenBackend(ctx, m.app, ref, key)
	}
	if m.be == nil { // a model built without Run's wiring
		return open()
	}
	return m.be.get(key, open)
}

// mrRun runs fn against the MR's forge off the UI loop.
func (m *model) mrRun(d *mrData, what, verb string, fn func(ctx context.Context, be review.Backend) (any, error)) tea.Cmd {
	return m.mrRunMsg(d, mrResultMsg{what: what, verb: verb}, fn)
}

// mrRunMsg is mrRun for a result that carries more than a name: msg is what
// comes back, with ref, err and out filled in.
func (m *model) mrRunMsg(d *mrData, msg mrResultMsg, fn func(ctx context.Context, be review.Backend) (any, error)) tea.Cmd {
	ctx, ref, key, mm := m.ctx, d.ref, d.forgeKey, *m
	if ctx == nil {
		ctx = context.Background()
	}
	run := func() tea.Msg {
		be, err := mm.backend(ctx, ref, key)
		var out any
		if err == nil {
			out, err = fn(ctx, be)
		}
		msg.ref, msg.err, msg.out = ref, err, out
		return msg
	}
	return tea.Batch(run, m.kickSpin())
}

// kickSpin starts the spinner clock if it is not already running.
func (m *model) kickSpin() tea.Cmd {
	p := m.mr
	if p == nil || p.spinning {
		return nil
	}
	p.spinning = true
	return tea.Tick(spinEvery, func(time.Time) tea.Msg { return mrSpinMsg{} })
}

func (m *model) onSpin() tea.Cmd {
	p := m.mr
	if p == nil {
		return nil
	}
	p.spin++
	if p.data != nil && p.data.anyBusy() || p.modal != nil && p.modal.logLoading {
		return tea.Tick(spinEvery, func(time.Time) tea.Msg { return mrSpinMsg{} })
	}
	p.spinning = false
	return nil
}

func (m *model) loadMR(d *mrData) tea.Cmd {
	d.mrL = load{st: loadBusy}
	return m.mrRun(d, "mr", "", func(ctx context.Context, be review.Backend) (any, error) { return be.MR(ctx, d.ref) })
}

func (m *model) loadThreads(d *mrData) tea.Cmd {
	d.threadsL = load{st: loadBusy}
	return m.mrRun(d, "threads", "", func(ctx context.Context, be review.Backend) (any, error) { return be.Discussions(ctx, d.ref) })
}

func (m *model) loadDiff(d *mrData) tea.Cmd {
	d.diffL = load{st: loadBusy}
	return m.mrRun(d, "diff", "", func(ctx context.Context, be review.Backend) (any, error) { return be.MRDiff(ctx, d.ref) })
}

func (m *model) loadJobs(d *mrData) tea.Cmd {
	pl := d.mr.Pipeline
	if pl == nil {
		return nil
	}
	d.jobsL = load{st: loadBusy}
	id := pl.ID
	return m.mrRun(d, "jobs", "", func(ctx context.Context, be review.Backend) (any, error) {
		return be.Jobs(ctx, d.ref.Project, id)
	})
}

func (m *model) mrRefetch(d *mrData, what mrReload) tea.Cmd {
	var cmds []tea.Cmd
	if what&relMR != 0 {
		cmds = append(cmds, m.loadMR(d))
	}
	if what&relThreads != 0 {
		cmds = append(cmds, m.loadThreads(d))
	}
	if what&relJobs != 0 && d.jobsL.st != loadIdle {
		cmds = append(cmds, m.loadJobs(d))
	}
	return tea.Batch(cmds...)
}

// --- results -----------------------------------------------------------------

func (m *model) onMRResult(msg mrResultMsg) tea.Cmd {
	d := m.mrCache[msg.ref.String()]
	if d == nil {
		return nil
	}
	switch msg.what {
	case "mr":
		d.mrL = settled(msg.err)
		if msg.err == nil {
			d.outage = false
			d.mr, d.loadedAt = msg.out.(forge.MR), time.Now()
			if m.mr != nil && m.mr.data == d {
				return m.ensureTab() // the pipeline id may have just arrived
			}
			return nil
		}
		return m.loadFailed(d, "merge request", msg.err)
	case "threads":
		d.threadsL = settled(msg.err)
		if msg.err == nil {
			d.threads = msg.out.([]forge.Discussion)
			return nil
		}
		return m.loadFailed(d, "threads", msg.err)
	case "diff":
		d.diffL = settled(msg.err)
		if msg.err != nil {
			return m.loadFailed(d, "diff", msg.err)
		}
		d.diff = msg.out.(forge.Diff)
		if m.reviews != nil {
			s, err := review.Resume(m.reviews, d.ref, d.diff)
			if err != nil {
				return m.loadFailed(d, "saved review drafts", err)
			}
			d.session, d.drafts = s, s.Drafts
		}
		if m.mr != nil && m.mr.data == d && m.mr.tab == tabOverview {
			return m.maybeSummary() // the summary was waiting for the diff
		}
		return nil
	case "jobs":
		d.jobsL = settled(msg.err)
		if msg.err == nil {
			d.jobs = msg.out.([]forge.Job)
			// GitLab lists newest first: oldest first reads as the pipeline runs.
			sort.SliceStable(d.jobs, func(i, j int) bool { return d.jobs[i].ID < d.jobs[j].ID })
			return nil
		}
		return m.loadFailed(d, "CI jobs", msg.err)
	case "log":
		if p := m.mr; p != nil && p.modal != nil && p.modal.kind == modalLog {
			p.modal.setLog(msg)
		}
		return nil
	case "ai":
		return m.onAI(d, msg)
	case "pager":
		m.onPager(msg)
		return nil
	}
	return m.onWritten(d, msg)
}

// loadFailed records a failed read. Reads that fail together (an outage)
// raise one toast naming the problem and the recovery; the raw error, with
// its URL, goes to the audit log and the inbox detail, not to the screen.
func (m *model) loadFailed(d *mrData, what string, err error) tea.Cmd {
	if log := m.mrAudit(); log != nil {
		_ = log.Append(audit.Entry{Action: "mr.load", Item: d.ref.String(), Project: d.project, Target: what, Outcome: "error", Error: err.Error()})
	}
	if d.outage {
		return nil
	}
	d.outage = true
	m.emit(d, "mr.load.failed", events.Error, fmt.Sprintf(m.g.T("Could not load %s · U retries"), shortRef(d.ref, m.trackerOf(d.project))), shortCause(err)+"\n"+err.Error())
	return nil
}

// shortCause names why a forge call failed in a few words, for the screen:
// the request, its URL and the status line are for the audit log.
func shortCause(err error) string {
	var ne net.Error
	switch s := strings.ToLower(err.Error()); {
	case errors.Is(err, forge.ErrAuth):
		return "the forge rejected the token"
	case errors.Is(err, forge.ErrNotFound):
		return "not found on the forge"
	case errors.Is(err, syscall.ECONNREFUSED), strings.Contains(s, "connection refused"), strings.Contains(s, "no such host"):
		return "the forge is unreachable"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout(), strings.Contains(s, "timeout"):
		return "the forge did not answer in time"
	case strings.Contains(s, ": 429"):
		return "the forge is rate limiting"
	case strings.Contains(s, ": 5"):
		return "the forge reported an error"
	case strings.Contains(s, "http://"), strings.Contains(s, "https://"), strings.HasPrefix(s, "gitlab "), strings.HasPrefix(s, "github "):
		return "the forge request failed"
	}
	return oneLine(err.Error())
}

// onWritten finishes a write: clears the progress mark, raises the result
// pop-up (and inbox row), and reads back what the write changed.
func (m *model) onWritten(d *mrData, msg mrResultMsg) tea.Cmd {
	d.busy = ""
	kind := "mr." + msg.what
	if msg.what == "submit" {
		kind = "review.submitted"
		d.refreshDrafts(m.reviews) // progress is saved even when the run failed
	}
	if msg.what == "retry" {
		kind = "ci.retry"
	}
	if msg.err != nil {
		m.emit(d, kind+".failed", events.Error, fmt.Sprintf("%s failed on %s", capitalize(msg.verb), d.ref), msg.err.Error())
		if msg.what == "merge" || msg.what == "submit" {
			return tea.Batch(m.mrRefetch(d, relMR|relThreads), m.reloadHistory()) // the forge may have moved under us
		}
		return m.reloadHistory()
	}
	w, _ := msg.out.(mrWrote)
	m.emit(d, kind, events.Success, w.done, "")
	return tea.Batch(m.mrRefetch(d, w.reload), m.reloadHistory())
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// emit publishes an MR event on the bus: a pop-up now, an inbox row for what
// matters. Without a bus (a bare test model) it pops up directly.
func (m *model) emit(d *mrData, kind string, sev events.Severity, title, detail string) {
	e := events.New(kind, sev, title)
	e.Project, e.Subject, e.Detail, e.Link = d.project, d.ref.String(), detail, d.mr.WebURL
	if m.app != nil && m.app.Bus != nil {
		m.app.Bus.Publish(e)
		return
	}
	m.nc.onEvent(e)
}

// notice shows a transient pop-up for something the page refused to do.
func (m *model) notice(sev events.Severity, title string) {
	e := events.New("ui.mr.notice", sev, title)
	e.Transient = true
	m.nc.push(e)
}

// me is the signed-in user of the page's project, as the last sync saw them.
func (m *model) me() forge.User {
	for i := range m.results {
		if m.results[i].Project == m.mr.project {
			return m.results[i].Me
		}
	}
	return forge.User{}
}
