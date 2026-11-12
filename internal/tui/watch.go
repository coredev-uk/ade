package tui

import (
	"context"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/watch"
)

// The dashboard does not poll files. It watches two places (internal/watch,
// fsnotify; polling stat where fsnotify is unavailable) and re-reads on a
// signal:
//
//   - the cache directory: cache.db and its write-ahead log. A sync, start
//     work, `ade notify send` or any other ade process writing there brings
//     in the work items, the inbox and the agent runs.
//   - the audit directory: an AI call, from this process or another, appends
//     there, and the header's AI figure is re-read.
//
// The one thing left on a timer is the end of the local day, when the "day"
// budget period starts over without any write.

// fileWatch is the dashboard's two watchers. Nil (tests, no watching) never
// signals.
type fileWatch struct {
	cache, audit *watch.Watcher
}

// newFileWatch starts watching; they stop when ctx is done.
func newFileWatch(ctx context.Context, a *app.App, cachePath string, o watch.Options) (*fileWatch, error) {
	c, err := watch.Watch(ctx, []watch.Target{{Path: filepath.Dir(cachePath)}}, o)
	if err != nil {
		return nil, err
	}
	au, err := watch.Watch(ctx, []watch.Target{{Path: a.Audit.Dir}}, o)
	if err != nil {
		return nil, err
	}
	return &fileWatch{cache: c, audit: au}, nil
}

type (
	cacheChangedMsg struct{ open bool } // the cache directory changed (open false: the watcher is gone)
	auditChangedMsg struct{ open bool } // the audit log changed
	dayRolledMsg    struct{}            // the local day ended
)

func waitFor(w *watch.Watcher, wrap func(open bool) tea.Msg) tea.Cmd {
	if w == nil {
		return nil
	}
	ch := w.C()
	return func() tea.Msg {
		_, ok := <-ch
		return wrap(ok)
	}
}

func (m model) waitCache() tea.Cmd {
	if m.fw == nil {
		return nil
	}
	return waitFor(m.fw.cache, func(open bool) tea.Msg { return cacheChangedMsg{open} })
}

func (m model) waitAudit() tea.Cmd {
	if m.fw == nil {
		return nil
	}
	return waitFor(m.fw.audit, func(open bool) tea.Msg { return auditChangedMsg{open} })
}

// untilNextDay is how long until the local day (user.timezone) ends, with a
// second of slack so the re-read lands inside the new day.
func untilNextDay(now time.Time, loc *time.Location) time.Duration {
	y, mo, d := now.In(loc).Date()
	return time.Date(y, mo, d+1, 0, 0, 1, 0, loc).Sub(now)
}

func (m model) waitDayEnd() tea.Cmd {
	if m.app == nil || m.app.Config == nil {
		return nil
	}
	return tea.Tick(untilNextDay(time.Now(), ai.Location(m.app.Config)), func(time.Time) tea.Msg { return dayRolledMsg{} })
}

// watchStart is what Init starts: the waits for the two signals and the day timer.
func (m model) watchStart() tea.Cmd {
	return tea.Batch(m.waitCache(), m.waitAudit(), m.waitDayEnd())
}

// watchUpdate handles the file signals; ok reports the message was one.
func (m model) watchUpdate(msg tea.Msg) (model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case cacheChangedMsg:
		if !msg.open {
			return m, nil, true
		}
		cmds := []tea.Cmd{m.waitCache(), m.loadResults(), m.pollInbox()}
		if d := m.agt; d != nil && d.mgr != nil && d.err == nil {
			cmds = append(cmds, m.agLoad(d)) // runs started or moved on by another pane
		}
		return m, tea.Batch(cmds...), true
	case auditChangedMsg:
		if !msg.open {
			return m, nil, true
		}
		return m, tea.Batch(m.waitAudit(), m.loadAIChip()), true
	case dayRolledMsg:
		return m, tea.Batch(m.waitDayEnd(), m.loadAIChip()), true
	}
	return m, nil, false
}
