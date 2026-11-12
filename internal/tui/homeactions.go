package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/agile"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/workspace"
)

// homeState is what Home adds to the model beyond the rows: search, the
// palette or picker, the project and facet filters, the Activity section and
// the sprint-load average.
type homeState struct {
	facet, facetVal string
	project         string
	search          searchState
	pal             *palette
	activity        bool // Activity expanded (it starts collapsed)

	vel      map[string]myVel // per ADE project: your recent per-sprint average
	velAsked map[string]bool
}

// myVel is the per-sprint average of what you finished, in weight or, when
// nothing was weighted, in items.
type myVel struct {
	avg   float64
	basis string
}

func (v myVel) String() string {
	n := fmt.Sprintf("%.0f", v.avg)
	if float64(int(v.avg)) != v.avg && v.avg < 10 {
		n = fmt.Sprintf("%.1f", v.avg)
	}
	if v.basis == agile.BasisCount {
		return "your avg " + n + " " + plural(int(v.avg+0.5), "item")
	}
	return "your avg " + n + "w"
}

type (
	velMsg struct {
		project string
		v       agile.Velocity
		err     error
	}
	// restartedMsg reports a services restart.
	restartedMsg struct {
		node string
		res  []workspace.Result
		err  error
	}
	// browserMsg reports an attempt to open a URL.
	browserMsg struct {
		url string
		err error
	}
)

// homeUpdate handles Home's overlays and background answers. It runs before
// the page keys, so an open palette or search input owns the keyboard.
func (m model) homeUpdate(msg tea.Msg) (model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case velMsg:
		if msg.err == nil && msg.v.Average != nil {
			if m.hs.vel == nil {
				m.hs.vel = map[string]myVel{}
			}
			m.hs.vel[msg.project] = myVel{avg: *msg.v.Average, basis: msg.v.Basis}
		}
		return m, nil, true
	case restartedMsg:
		return m.onRestarted(msg), nil, true
	case browserMsg:
		return m.onBrowser(msg), nil, true
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, nil, false
		}
		switch {
		case m.hs.pal != nil:
			next, cmd := m.paletteKey(msg)
			return next, cmd, true
		case m.hs.search.typing && m.page == pageHome && m.mr == nil:
			next, cmd := m.searchKey(msg)
			return next, cmd, true
		}
		return m, nil, false
	}
	return m.searchUpdate(msg)
}

// --- keys on Home ----------------------------------------------------------------

func (m model) onHomePage() bool { return m.page == pageHome && m.mr == nil && !m.isAgilePage() }

func (m model) selectedRow() (homeRow, bool) {
	if !m.onHomePage() || m.homeSel >= len(m.homeRows) {
		return homeRow{}, false
	}
	return m.homeRows[m.homeSel], true
}

func (m model) toast(kind string, sev events.Severity, title, subject string) model {
	e := events.New(kind, sev, title)
	e.Subject = subject
	e.Transient = true
	m.nc.push(e)
	return m
}

// cycleProject is `p`: all projects, then each in turn.
func (m model) cycleProject() (model, tea.Cmd) {
	names := m.projectNames()
	if len(names) < 2 {
		return m.toast("ui.hint", events.Info, "one project: nothing to filter", ""), nil
	}
	next := ""
	if m.hs.project == "" {
		next = names[0]
	} else {
		for i, n := range names {
			if n == m.hs.project && i+1 < len(names) {
				next = names[i+1]
			}
		}
	}
	return m.setProject(next), nil
}

func (m model) toggleActivity() model {
	m.hs.activity = !m.hs.activity
	return m
}

// --- o: open in the browser --------------------------------------------------------

// openURL starts the platform opener; tests replace it so no browser ever
// opens.
var openURL = func(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// canOpenBrowser is false on a plain host (host.mode plain), over ssh and on
// a Linux session with no display: there the URL is shown instead.
func (m model) canOpenBrowser() bool {
	if m.app != nil && m.app.Config != nil && m.app.Config.Host.Mode == "plain" {
		return false
	}
	if os.Getenv("SSH_CONNECTION") != "" {
		return false
	}
	switch runtime.GOOS {
	case "darwin":
		return true
	default:
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
}

// rowURL is the web page of the selected row: the issue, or the MR itself.
func (r homeRow) url() string {
	if r.item != nil {
		return r.item.Issue.WebURL
	}
	if r.mr != nil {
		return r.mr.WebURL
	}
	return ""
}

// openBrowser handles `o`.
func (m model) openBrowser() (model, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok || r.url() == "" {
		return m, nil
	}
	url := r.url()
	if !m.canOpenBrowser() {
		// No browser here: show the address and copy it, like `y`.
		m = m.toast("ui.url", events.Info, r.ref()+": "+url, "no browser here: copied through OSC 52, paste it in a browser")
		return m, tea.SetClipboard(url)
	}
	return m, func() tea.Msg { return browserMsg{url: url, err: openURL(url)} }
}

func (m model) onBrowser(msg browserMsg) model {
	if msg.err != nil {
		e := events.New("ui.browser", events.Warn, "no browser opened: "+firstLine(msg.err.Error()))
		e.Subject = msg.url
		e.Transient = true
		m.nc.push(e)
		return m
	}
	return m.toast("ui.browser", events.Info, "opened in the browser", msg.url)
}

// --- r: restart blocked services, else refresh --------------------------------------

// blockedChip is the first blocked service of the selected item's Services
// strip.
func (m model) blockedChip() (svcChip, bool) {
	r, ok := m.selectedRow()
	if !ok || r.item == nil {
		return svcChip{}, false
	}
	for _, c := range m.ws.chips[r.item.Ref.String()] {
		if c.state == "blocked" {
			return c, true
		}
	}
	return svcChip{}, false
}

// restartOrRefresh handles `r`: when the selected item's services strip shows a
// blocked service, restart the subtree its dead requirement heads; otherwise
// refresh like `U`.
func (m model) restartOrRefresh() (model, tea.Cmd) {
	c, ok := m.blockedChip()
	if !ok {
		return m, m.refresh()
	}
	ctx, a := m.ctx, m.app
	if ctx == nil {
		ctx = context.Background()
	}
	node := c.row.RestartNode()
	m = m.toast("ws.restart", events.Info, "restarting "+node+" and what needs it", "")
	return m, func() tea.Msg {
		mgr, err := workspace.NewManager(a)
		if err != nil {
			return restartedMsg{node: node, err: err}
		}
		res, err := mgr.RestartRow(ctx, a.Root, c.row, workspace.UpOptions{})
		return restartedMsg{node: node, res: res, err: err}
	}
}

func (m model) onRestarted(msg restartedMsg) model {
	if msg.err != nil {
		e := events.New("ws.restart.failed", events.Error, "restart of "+msg.node+" failed")
		e.Subject = popupText(msg.err)
		e.Detail = fullError(msg.err)
		m.nc.push(e)
		return m
	}
	n := len(msg.res)
	return m.toast("ws.restarted", events.Success, fmt.Sprintf("restarted %s and %d %s", msg.node, max(n-1, 0), plural(max(n-1, 0), "dependent")), "")
}

// --- sprint-load average ---------------------------------------------------------------

// loadVelocity asks the forge, in the background, for your per-sprint average
// of every project with a running sprint. With missingOnly it skips projects
// already asked this session.
func (m *model) loadVelocity(missingOnly bool) tea.Cmd {
	if m.app == nil {
		return nil
	}
	if m.hs.velAsked == nil {
		m.hs.velAsked = map[string]bool{}
	}
	var cmds []tea.Cmd
	for i := range m.results {
		res := &m.results[i]
		if res.Window.Current == nil || res.Err != "" || (missingOnly && m.hs.velAsked[res.Project]) {
			continue
		}
		m.hs.velAsked[res.Project] = true
		proj, ctx, a := res.Project, m.ctx, m.app
		if ctx == nil {
			ctx = context.Background()
		}
		cmds = append(cmds, func() tea.Msg {
			v, err := agile.NewCollector(a).MyVelocity(ctx, proj, agile.DefaultVelocitySprints)
			return velMsg{project: proj, v: v, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// --- Activity ---------------------------------------------------------------------------

// recentEvents are the newest inbox rows, scoped to the project filter.
func (m model) recentEvents(n int) []notify.Item {
	var out []notify.Item
	for _, it := range m.nc.items {
		if m.hs.project != "" && it.Project != "" && it.Project != m.hs.project {
			continue
		}
		out = append(out, it)
		if len(out) == n {
			break
		}
	}
	return out
}

// eventLine is one Activity entry: severity glyph, age, title.
func (m model) eventLine(it notify.Item, w int) string {
	age := stFaint.Render(pad(m.g, agoShort(it.Time), 4))
	title := it.Title
	if it.Read {
		title = stFaint.Render(ansi.Truncate(title, max(w-8, 8), m.g.Ell))
	} else {
		title = ansi.Truncate(title, max(w-8, 8), m.g.Ell)
	}
	return " " + sevGlyph(m.g, it.Severity) + " " + age + " " + title
}

// activityLines is the Activity section of the list (narrow and middle
// widths): collapsed to its rule, expanded to the latest events.
func (m model) activityLines(w int) []string {
	evs := m.recentEvents(8)
	guide := "e shows"
	if m.hs.activity {
		guide = "e hides"
	}
	switch {
	case len(evs) == 0:
		guide = "nothing yet"
	default:
		guide = fmt.Sprintf("%d recent %s %s", len(evs), m.g.Sep, guide)
	}
	out := []string{subRule("Activity", guide, w, m.g)}
	if m.hs.activity {
		for _, it := range evs {
			out = append(out, "  "+m.eventLine(it, w-2))
		}
	}
	return out
}

// railView is the third region at wide widths: Activity above, and below it the
// selected item's services.
func (m model) railView(w, h int) string {
	var l []string
	evs := m.recentEvents(8)
	guide := "nothing yet"
	if len(evs) > 0 {
		guide = fmt.Sprintf("%d recent", len(evs))
	}
	l = append(l, section("Activity", guide, w, m.g))
	for _, it := range evs {
		l = append(l, m.eventLine(it, w))
	}
	if len(evs) == 0 {
		l = append(l, " "+stFaint.Render("No events yet."))
	}
	l = append(l, "")
	if r, ok := m.selectedRow(); ok && r.item != nil {
		chips := m.ws.chips[r.item.Ref.String()]
		l = append(l, section("Services", fmt.Sprintf("%d running", len(chips)), w, m.g))
		if len(chips) == 0 {
			l = append(l, " "+stFaint.Render("None running."))
		}
		for _, line := range m.serviceChipLines(chips, w-1) {
			l = append(l, " "+line)
		}
	}
	return fit(m.g, strings.Join(l, "\n"), w, h)
}

// threeRegion is true at wide widths (>= 140 cells): the list, the preview
// and a rail with Activity and Services.
func (m model) threeRegion() bool { return m.w >= wideMinW }

// homeFilterName names what narrows Home, for the footer: the filter or facet
// value, with the project when one is chosen.
func (m model) homeFilterName() string {
	name := m.homeFilter.String()
	if m.hs.facet != "" {
		name = m.hs.facet + " " + m.hs.facetVal
	}
	if m.hs.project != "" {
		name += " " + m.g.Sep + " " + m.hs.project
	}
	return name
}

// agoShort is a compact age for narrow columns: now, 5m, 3h, 2d.
func agoShort(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
