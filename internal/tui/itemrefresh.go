package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
)

// refreshItem is what a dialog does after it has written to an issue: read
// that one item again (a handful of requests) and show it, instead of a sync
// of every project. It returns the cache as it stands afterwards; a failed
// read leaves the cached item as it was, and the next sync mends it.
func (m model) refreshItem(man *config.Manifest, f forge.Forge, ref forge.Ref) tea.Cmd {
	if m.sync == nil || man == nil || f == nil {
		return nil
	}
	svc, ctx, cache := m.sync, m.ctx, m.cache
	return func() tea.Msg {
		_, _ = svc.RefreshItem(ctx, man, f, ref)
		rs, err := forgesync.Load(cache)
		return resultsMsg{results: rs, err: err}
	}
}
