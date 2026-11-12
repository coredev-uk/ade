package forgesync

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/store"
)

const (
	// activityWindow bounds how far back code-repo issues and MRs are listed.
	activityWindow = 60 * 24 * time.Hour
	defaultOverlap = 1

	issueOpened = "opened"
	issueAll    = "all"
	mrOpened    = "opened"

	srcIterations = "gitlab-iterations"
	srcMilestones = "milestones"
	srcLabels     = "labels"
)

// pipeline fetches and assembles one project.
type pipeline struct {
	f         forge.Forge
	st        *store.Store
	m         *config.Manifest
	cfg       *config.Config
	forgeName string
	now       time.Time
}

func (p *pipeline) run(ctx context.Context, res *Result) error {
	me, err := p.me(ctx)
	if err != nil {
		return fmt.Errorf("current user: %w", err)
	}
	caps, err := p.caps(ctx)
	if err != nil {
		return fmt.Errorf("capabilities: %w", err)
	}
	sprints, err := p.loadSprints(ctx)
	if err != nil {
		return fmt.Errorf("sprints: %w", err)
	}
	workdays, err := domain.ParseWorkdays(p.cfg.User.Workdays)
	if err != nil {
		return fmt.Errorf("user.workdays: %w", err)
	}
	window := domain.WindowAt(sprints, p.now, workdays, p.overlap())

	items, reviews, err := p.work(ctx, me, sprints, window)
	if err != nil {
		return err
	}
	res.Me, res.Caps, res.Sprints, res.Window = me, caps, sprints, window
	res.Items, res.Reviews = items, reviews
	return nil
}

func (p *pipeline) overlap() int {
	if o := p.m.Tracker.Iterations.Overlap; o > 0 {
		return o
	}
	return defaultOverlap
}

func (p *pipeline) me(ctx context.Context) (forge.User, error) {
	return store.Cached(p.st, startwork.MeKey(p.forgeName), startwork.CacheTTL, p.now, func() (forge.User, error) {
		return p.f.Me(ctx)
	})
}

func (p *pipeline) caps(ctx context.Context) (forge.Caps, error) {
	group := forge.Group(p.m.Tracker.Project)
	key := startwork.CapsKey(p.forgeName, group)
	return store.Cached(p.st, key, startwork.CacheTTL, p.now, func() (forge.Caps, error) {
		return p.f.Capabilities(ctx, group)
	})
}

// repoData is what one code repo contributed.
type repoData struct {
	path    string
	issues  []forge.Issue
	mrs     []forge.MR
	reviews []forge.MR
}

// work fetches everything for the window and assembles the work items and
// the review queue.
func (p *pipeline) work(ctx context.Context, me forge.User, sprints []domain.Sprint, w domain.Window) ([]domain.WorkItem, []forge.MR, error) {
	tracker, err := p.trackerIssues(ctx, me, w)
	if err != nil {
		return nil, nil, fmt.Errorf("tracker issues: %w", err)
	}
	repos, err := p.loadRepos(ctx, me)
	if err != nil {
		return nil, nil, err
	}
	if tracker, err = p.adoptParents(ctx, me, tracker, repos); err != nil {
		return nil, nil, fmt.Errorf("parent issues: %w", err)
	}
	links, err := p.links(ctx, tracker)
	if err != nil {
		return nil, nil, fmt.Errorf("issue links: %w", err)
	}
	cands, err := p.candidates(ctx, repos, links)
	if err != nil {
		return nil, nil, fmt.Errorf("child issues: %w", err)
	}

	roots := p.roots(me, tracker, links, cands, repos)
	reviews := collectReviews(repos)
	detail, err := p.mrDetails(ctx, roots, reviews)
	if err != nil {
		return nil, nil, fmt.Errorf("merge requests: %w", err)
	}
	for i := range roots {
		applyDetail(roots[i].mrs, detail)
	}
	applyDetail(reviews, detail)

	items := make([]domain.WorkItem, len(roots))
	for i, r := range roots {
		items[i] = domain.Assemble(domain.AssembleInput{
			Project: p.m.Name, Manifest: p.m, Me: me.Username,
			Tracker: r.issue, Links: r.links, Candidates: cands,
			MRs: r.mrs, Branches: branches(r.mrs), Sprints: sprints, Now: p.now,
		})
	}
	return items, reviews, nil
}

// --- sprints ----------------------------------------------------------------

func (p *pipeline) loadSprints(ctx context.Context) ([]domain.Sprint, error) {
	it := p.m.Tracker.Iterations
	group := forge.Group(p.m.Tracker.Project)
	switch it.Source {
	case "", "auto":
		sp, err := p.iterations(ctx, group)
		if errors.Is(err, forge.ErrUnavailable) {
			return p.milestones(ctx, group)
		}
		return sp, err
	case srcIterations, "cadence":
		return p.iterations(ctx, group)
	case srcMilestones:
		return p.milestones(ctx, group)
	case srcLabels:
		return p.labelSprints(ctx)
	}
	return nil, fmt.Errorf("iterations source %q is not supported by this forge", it.Source)
}

func (p *pipeline) iterations(ctx context.Context, group string) ([]domain.Sprint, error) {
	cadence := p.m.Tracker.Iterations.Cadence
	its, err := p.f.Iterations(ctx, group, cadence)
	if err != nil {
		return nil, err
	}
	return domain.SprintsFromIterations(its, cadence), nil
}

func (p *pipeline) milestones(ctx context.Context, group string) ([]domain.Sprint, error) {
	if p.f.Kind() == "github" {
		group = p.m.Tracker.Project // GitHub milestones belong to one repository, the tracker
	}
	ms, err := p.f.Milestones(ctx, group)
	if err != nil {
		return nil, err
	}
	return domain.SprintsFromMilestones(ms), nil
}

func (p *pipeline) labelSprints(ctx context.Context) ([]domain.Sprint, error) {
	it := p.m.Tracker.Iterations
	if it.Pattern == "" || it.Start == "" || it.Length == "" {
		return nil, errors.New("labels iterations need pattern, start and length")
	}
	start, err := forge.ParseDate(it.Start)
	if err != nil {
		return nil, fmt.Errorf("tracker.iterations.start: %w", err)
	}
	length, err := config.ParseSprintLength(it.Length)
	if err != nil {
		return nil, fmt.Errorf("tracker.iterations.length: %w", err)
	}
	issues, err := p.f.ListIssues(ctx, forge.IssueQuery{
		Project: p.m.Tracker.Project, State: issueAll, UpdatedAfter: p.now.Add(-activityWindow),
	})
	if err != nil {
		return nil, err
	}
	var labels []string
	for _, is := range issues {
		labels = append(labels, is.Labels...)
	}
	return domain.SprintsFromLabels(it.Pattern, start, length, labels), nil
}

// sprintQuery selects a sprint's tracker issues; ok is false when the sprint
// cannot be expressed as a filter (an unfiltered query would match everything).
func sprintQuery(project string, sp domain.Sprint, state string) (forge.IssueQuery, bool) {
	q := forge.IssueQuery{Project: project, State: state}
	switch sp.Source {
	case srcIterations:
		id, err := strconv.ParseInt(sp.Key, 10, 64)
		q.IterationID = id
		return q, err == nil && id > 0
	case srcMilestones:
		q.Milestone = sp.Title
		return q, sp.Title != ""
	case srcLabels:
		q.Labels = []string{sp.Key}
		return q, sp.Key != ""
	}
	return q, false
}

// --- tracker issues -----------------------------------------------------------

// trackerIssues returns open issues assigned to me, every issue of the current
// sprint (closed ones count as done) and the open issues of the adjacent ones.
func (p *pipeline) trackerIssues(ctx context.Context, me forge.User, w domain.Window) ([]forge.Issue, error) {
	proj := p.m.Tracker.Project
	qs := []forge.IssueQuery{{Project: proj, Assignee: me.Username, State: issueOpened}}
	if w.Current != nil {
		if q, ok := sprintQuery(proj, *w.Current, issueAll); ok {
			qs = append(qs, q)
		}
	}
	for _, sp := range []*domain.Sprint{w.Next, w.Previous} {
		if sp == nil {
			continue
		}
		if q, ok := sprintQuery(proj, *sp, issueOpened); ok {
			qs = append(qs, q)
		}
	}
	lists := make([][]forge.Issue, len(qs))
	err := parallel(ctx, len(qs), func(ctx context.Context, i int) (err error) {
		lists[i], err = p.f.ListIssues(ctx, qs[i])
		return err
	})
	if err != nil {
		return nil, err
	}
	return mergeIssues(lists...), nil
}

func mergeIssues(lists ...[]forge.Issue) []forge.Issue {
	seen := map[forge.Ref]bool{}
	var out []forge.Issue
	for _, l := range lists {
		for _, is := range l {
			if !seen[is.Ref] {
				seen[is.Ref] = true
				out = append(out, is)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b forge.Issue) int { return cmp.Compare(a.Ref.IID, b.Ref.IID) })
	return out
}

// adoptParents adds tracker issues that parent an open issue assigned to me
// in a code repo, so that work shows under its tracker item.
func (p *pipeline) adoptParents(ctx context.Context, me forge.User, tracker []forge.Issue, repos []repoData) ([]forge.Issue, error) {
	have := map[forge.Ref]bool{}
	for _, t := range tracker {
		have[t.Ref] = true
	}
	var missing []forge.Ref
	for _, r := range repos {
		for _, is := range r.issues {
			if is.State != issueOpened || !assignedTo(is, me) {
				continue
			}
			for _, pr := range domain.ParentRefs(is) {
				if pr.Project == p.m.Tracker.Project && !have[pr] {
					have[pr] = true
					missing = append(missing, pr)
				}
			}
		}
	}
	fetched, err := p.fetchIssues(ctx, missing)
	if err != nil {
		return nil, err
	}
	return mergeIssues(tracker, fetched), nil
}

// fetchIssues loads issues by ref in parallel, skipping ones that do not exist.
func (p *pipeline) fetchIssues(ctx context.Context, refs []forge.Ref) ([]forge.Issue, error) {
	got := make([]*forge.Issue, len(refs))
	err := parallel(ctx, len(refs), func(ctx context.Context, i int) error {
		is, err := p.f.Issue(ctx, refs[i])
		if errors.Is(err, forge.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", refs[i], err)
		}
		got[i] = &is
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []forge.Issue
	for _, g := range got {
		if g != nil {
			out = append(out, *g)
		}
	}
	return out, nil
}

func (p *pipeline) links(ctx context.Context, tracker []forge.Issue) ([][]forge.Link, error) {
	out := make([][]forge.Link, len(tracker))
	err := parallel(ctx, len(tracker), func(ctx context.Context, i int) (err error) {
		out[i], err = p.f.Links(ctx, tracker[i].Ref)
		if err != nil {
			err = fmt.Errorf("%s: %w", tracker[i].Ref, err)
		}
		return err
	})
	return out, err
}

// --- code repos -----------------------------------------------------------------

func (p *pipeline) loadRepos(ctx context.Context, me forge.User) ([]repoData, error) {
	var repos []repoData
	for _, name := range p.m.RepoNames() {
		if path := p.m.Repos[name].Path; path != "" {
			repos = append(repos, repoData{path: path})
		}
	}
	since := p.now.Add(-activityWindow)
	err := parallel(ctx, len(repos)*3, func(ctx context.Context, i int) (err error) {
		r := &repos[i/3]
		switch i % 3 {
		case 0:
			r.issues, err = p.f.ListIssues(ctx, forge.IssueQuery{Project: r.path, State: issueAll, UpdatedAfter: since})
		case 1:
			r.mrs, err = p.f.ListMRs(ctx, forge.MRQuery{Project: r.path, State: issueAll, UpdatedAfter: since})
		case 2:
			r.reviews, err = p.f.ListMRs(ctx, forge.MRQuery{Project: r.path, State: mrOpened, Reviewer: me.Username})
		}
		if err != nil {
			err = fmt.Errorf("%s: %w", r.path, err)
		}
		return err
	})
	return repos, err
}

// candidates are the issues that may be children of a tracker issue: the
// recently active ones of every repo plus linked children that were not.
func (p *pipeline) candidates(ctx context.Context, repos []repoData, links [][]forge.Link) ([]forge.Issue, error) {
	var lists [][]forge.Issue
	seen := map[forge.Ref]bool{}
	for _, r := range repos {
		lists = append(lists, r.issues)
		for _, is := range r.issues {
			seen[is.Ref] = true
		}
	}
	var missing []forge.Ref
	for _, ls := range links {
		for _, l := range ls {
			if domain.IsChildLink(l) && !seen[l.Ref] && p.knownProject(l.Ref.Project) {
				seen[l.Ref] = true
				missing = append(missing, l.Ref)
			}
		}
	}
	fetched, err := p.fetchIssues(ctx, missing)
	if err != nil {
		return nil, err
	}
	return mergeIssues(append(lists, fetched)...), nil
}

func (p *pipeline) knownProject(path string) bool {
	if path == p.m.Tracker.Project {
		return true
	}
	for _, r := range p.m.Repos {
		if r.Path == path {
			return true
		}
	}
	return false
}

// --- roots ----------------------------------------------------------------------

// root is one work item's raw material: its issue, links and attached MRs.
type root struct {
	issue forge.Issue
	links []forge.Link
	mrs   []forge.MR
}

// roots lists tracker items first, then open code-repo issues assigned to me
// that no tracker item claims.
func (p *pipeline) roots(me forge.User, tracker []forge.Issue, links [][]forge.Link, cands []forge.Issue, repos []repoData) []root {
	index := indexMRs(repos)
	claimed := map[forge.Ref]bool{}
	var out []root
	for i, t := range tracker {
		refs := []forge.Ref{t.Ref}
		for _, k := range childRefs(t, links[i], cands) {
			claimed[k] = true
			refs = append(refs, k)
		}
		out = append(out, root{issue: t, links: links[i], mrs: mrsFor(index, refs)})
	}
	for _, r := range repos {
		if r.path == p.m.Tracker.Project {
			continue
		}
		for _, is := range r.issues {
			if is.State == issueOpened && assignedTo(is, me) && !claimed[is.Ref] && len(domain.ParentRefs(is)) == 0 {
				claimed[is.Ref] = true
				out = append(out, root{issue: is, mrs: mrsFor(index, []forge.Ref{is.Ref})})
			}
		}
	}
	return out
}

// childRefs mirrors domain's child rule: linked by a child link, or naming the
// tracker as parent.
func childRefs(tracker forge.Issue, links []forge.Link, cands []forge.Issue) []forge.Ref {
	linked := map[forge.Ref]bool{}
	for _, l := range links {
		if domain.IsChildLink(l) {
			linked[l.Ref] = true
		}
	}
	var out []forge.Ref
	for _, c := range cands {
		if c.Ref != tracker.Ref && (linked[c.Ref] || slices.Contains(domain.ParentRefs(c), tracker.Ref)) {
			out = append(out, c.Ref)
		}
	}
	return out
}

func assignedTo(is forge.Issue, me forge.User) bool {
	return slices.ContainsFunc(is.Assignees, func(u forge.User) bool {
		return strings.EqualFold(u.Username, me.Username)
	})
}

func branches(mrs []forge.MR) []string {
	var out []string
	for _, mr := range mrs {
		if mr.SourceBranch != "" && !slices.Contains(out, mr.SourceBranch) {
			out = append(out, mr.SourceBranch)
		}
	}
	return out
}

// --- merge requests -------------------------------------------------------------

// indexMRs maps each issue an MR targets to the MRs, in repo list order.
func indexMRs(repos []repoData) map[forge.Ref][]forge.MR {
	index := map[forge.Ref][]forge.MR{}
	seen := map[forge.Ref]bool{}
	for _, r := range repos {
		for _, mr := range r.mrs {
			if seen[mr.Ref] {
				continue
			}
			seen[mr.Ref] = true
			for _, t := range mrTargets(mr) {
				index[t] = append(index[t], mr)
			}
		}
	}
	return index
}

func mrsFor(index map[forge.Ref][]forge.MR, refs []forge.Ref) []forge.MR {
	var out []forge.MR
	seen := map[forge.Ref]bool{}
	for _, ref := range refs {
		for _, mr := range index[ref] {
			if !seen[mr.Ref] {
				seen[mr.Ref] = true
				out = append(out, mr)
			}
		}
	}
	return out
}

func collectReviews(repos []repoData) []forge.MR {
	var out []forge.MR
	seen := map[forge.Ref]bool{}
	for _, r := range repos {
		for _, mr := range r.reviews {
			if !seen[mr.Ref] {
				seen[mr.Ref] = true
				out = append(out, mr)
			}
		}
	}
	return out
}

// mrDetails fetches full detail (pipeline, approvals, threads) once for every
// open MR attached to an item or awaiting my review. A forge that can batch
// (forge.MRDetailer) reads them in a few requests; others one by one.
func (p *pipeline) mrDetails(ctx context.Context, roots []root, reviews []forge.MR) (map[forge.Ref]forge.MR, error) {
	var open []forge.MR
	seen := map[forge.Ref]bool{}
	add := func(mrs []forge.MR) {
		for _, mr := range mrs {
			if mr.State == mrOpened && !seen[mr.Ref] {
				seen[mr.Ref] = true
				open = append(open, mr)
			}
		}
	}
	for _, r := range roots {
		add(r.mrs)
	}
	add(reviews)

	got := make([]forge.MR, len(open))
	if b, ok := p.f.(forge.MRDetailer); ok {
		caps, err := p.caps(ctx)
		if err != nil {
			return nil, fmt.Errorf("capabilities: %w", err)
		}
		if got, err = b.MRDetails(ctx, open, caps); err != nil {
			return nil, err
		}
	} else {
		err := parallel(ctx, len(open), func(ctx context.Context, i int) (err error) {
			got[i], err = p.f.MR(ctx, open[i].Ref)
			if err != nil {
				err = fmt.Errorf("%s: %w", open[i].Ref, err)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	out := make(map[forge.Ref]forge.MR, len(got))
	for _, mr := range got {
		out[mr.Ref] = mr
	}
	return out, nil
}

func applyDetail(mrs []forge.MR, detail map[forge.Ref]forge.MR) {
	for i, mr := range mrs {
		if d, ok := detail[mr.Ref]; ok {
			mrs[i] = d
		}
	}
}
