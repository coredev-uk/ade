package forgesync

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/store"
)

// RefreshItem re-reads one work item from the forge and replaces its cached
// copy: the issue and its links, the issues linked as children, the merge
// requests of the manifest's repos that work on any of them (with their
// pipeline, approvals and threads), and nothing else. A dialog that has just
// written to an issue, or start work that has just created a child and its MR,
// calls it instead of a full sync: a handful of requests instead of the whole
// project, and the rest of the cache stays as the last sync left it (so do the
// sprints and the review queue).
//
// Items the cache has not seen are added. A running sync is not waited for;
// if it finishes after this call its result, read before the write, replaces
// the row with the next sync's data.
func (s *Service) RefreshItem(ctx context.Context, m *config.Manifest, f forge.Forge, ref forge.Ref) (domain.WorkItem, error) {
	st, err := s.App.Cache()
	if err != nil {
		return domain.WorkItem{}, fmt.Errorf("open cache: %w", err)
	}
	cached, _ := Load(st)
	var prev Result
	var old *domain.WorkItem
	for _, r := range cached {
		if r.Project != m.Name {
			continue
		}
		prev = r
		for i := range r.Items {
			if r.Items[i].Ref == ref {
				old = &r.Items[i]
			}
		}
	}
	p := &pipeline{f: f, st: st, m: m, cfg: s.App.Config, forgeName: m.Forge, now: s.Now()}

	var (
		tracker forge.Issue
		links   []forge.Link
	)
	if err := parallel(ctx, 2, func(ctx context.Context, i int) (err error) {
		if i == 0 {
			tracker, err = f.Issue(ctx, ref)
		} else {
			links, err = f.Links(ctx, ref)
		}
		return err
	}); err != nil {
		return domain.WorkItem{}, fmt.Errorf("%s: %w", ref, err)
	}
	me := prev.Me
	if me.Username == "" {
		if me, err = startwork.CachedMe(ctx, s.App, m.Forge, f); err != nil {
			return domain.WorkItem{}, fmt.Errorf("current user: %w", err)
		}
	}

	// Children: linked as one, or already known as one (an issue that only
	// names the tracker as its parent is not in the links).
	var kidRefs []forge.Ref
	for _, l := range links {
		if domain.IsChildLink(l) && l.Ref != ref && p.knownProject(l.Ref.Project) && !slices.Contains(kidRefs, l.Ref) {
			kidRefs = append(kidRefs, l.Ref)
		}
	}
	if old != nil {
		for _, k := range old.Children {
			if k.Ref != ref && !slices.Contains(kidRefs, k.Ref) {
				kidRefs = append(kidRefs, k.Ref)
			}
		}
	}
	kids, err := p.fetchIssues(ctx, kidRefs)
	if err != nil {
		return domain.WorkItem{}, fmt.Errorf("child issues: %w", err)
	}

	// Merge requests: the manifest's repos that hold the item or a child,
	// listed the way a sync lists them and matched to the issues they work on.
	refs := append([]forge.Ref{ref}, kidRefs...)
	var repos []repoData
	for _, name := range m.RepoNames() {
		path := m.Repos[name].Path
		if path != "" && slices.ContainsFunc(refs, func(r forge.Ref) bool { return r.Project == path }) {
			repos = append(repos, repoData{path: path})
		}
	}
	since := p.now.Add(-activityWindow)
	if err := parallel(ctx, len(repos), func(ctx context.Context, i int) (err error) {
		repos[i].mrs, err = f.ListMRs(ctx, forge.MRQuery{Project: repos[i].path, State: issueAll, UpdatedAfter: since})
		return err
	}); err != nil {
		return domain.WorkItem{}, fmt.Errorf("merge requests: %w", err)
	}
	mrs := mrsFor(indexMRs(repos), refs)
	detail, err := p.mrDetails(ctx, []root{{issue: tracker, mrs: mrs}}, nil)
	if err != nil {
		return domain.WorkItem{}, fmt.Errorf("merge requests: %w", err)
	}
	applyDetail(mrs, detail)

	item := domain.Assemble(domain.AssembleInput{
		Project: m.Name, Manifest: m, Me: me.Username,
		Tracker: tracker, Links: links, Candidates: kids,
		MRs: mrs, Branches: branches(mrs), Sprints: prev.Sprints, Now: p.now,
	})
	if err := saveItem(st, item); err != nil {
		return domain.WorkItem{}, err
	}
	return item, nil
}

// saveItem replaces (or adds) one item's cached row.
func saveItem(st *store.Store, wi domain.WorkItem) error {
	b, err := json.Marshal(wi)
	if err != nil {
		return err
	}
	res, err := st.DB.Exec(`UPDATE work_items SET json = ? WHERE project = ? AND ref = ?`, string(b), wi.Project, wi.Ref.String())
	if err != nil {
		return fmt.Errorf("cache item %s: %w", wi.Ref, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, err := st.DB.Exec(`INSERT INTO work_items (project, ref, json) VALUES (?, ?, ?)`, wi.Project, wi.Ref.String(), string(b)); err != nil {
		return fmt.Errorf("cache item %s: %w", wi.Ref, err)
	}
	return nil
}
