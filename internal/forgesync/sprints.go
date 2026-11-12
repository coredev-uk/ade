package forgesync

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/store"
)

// LoadSprints reads the manifest's sprints from the forge exactly as a sync
// does (iterations, milestones or label-derived, by tracker.iterations). caps
// are the tracker namespace's: a user namespace has milestones only.
func LoadSprints(ctx context.Context, f forge.Forge, m *config.Manifest, caps forge.Caps, now time.Time) ([]domain.Sprint, error) {
	p := &pipeline{f: f, m: m, forgeName: m.Forge, now: now}
	return p.loadSprints(ctx, caps)
}

// RecordSprintMove writes a sprint move into the cached work item of the
// tracker issue ref, so Home shows it at once and the next sync, which would
// otherwise see the sprint change and raise issue.iteration_changed a second
// time, finds nothing new. A project or item that is not cached is not an
// error: the next sync reads the forge.
func RecordSprintMove(st *store.Store, project string, ref forge.Ref, to domain.Sprint) error {
	var raw string
	err := st.DB.QueryRow(`SELECT json FROM work_items WHERE project = ? AND ref = ?`, project, ref.String()).Scan(&raw)
	if err != nil {
		return nil //nolint:nilerr // not cached: nothing to patch
	}
	var wi domain.WorkItem
	if err := json.Unmarshal([]byte(raw), &wi); err != nil {
		return nil //nolint:nilerr // unreadable: the next sync rewrites it
	}
	wi.Sprint = &to
	b, err := json.Marshal(wi)
	if err != nil {
		return err
	}
	if _, err := st.DB.Exec(`UPDATE work_items SET json = ? WHERE project = ? AND ref = ?`, string(b), project, ref.String()); err != nil {
		return fmt.Errorf("cache item %s: %w", ref, err)
	}
	return nil
}
