package agile

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/project"
)

// Forge is a forge the agile reports can read: current state plus history.
type Forge interface {
	forge.Forge
	forge.AgileReader
}

// OpenFunc opens the client of the user-config forge called name.
type OpenFunc func(ctx context.Context, name string, f config.Forge) (forge.Forge, error)

// Collector fetches what the reports need. It reads the sync cache for items
// and sprints and the forges for history.
type Collector struct {
	App  *app.App
	Open OpenFunc
	Now  func() time.Time
}

// NewCollector uses the real forge drivers and the wall clock.
func NewCollector(a *app.App) *Collector {
	return &Collector{App: a, Open: forgesync.New(a).Open, Now: time.Now}
}

func (c *Collector) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c *Collector) calendar() (Calendar, error) { return NewCalendar(c.App.Config.User) }

func (c *Collector) cached() ([]forgesync.Result, error) {
	st, err := c.App.Cache()
	if err != nil {
		return nil, err
	}
	res, err := forgesync.Load(st)
	if err != nil {
		return nil, err
	}
	if len(res) == 0 {
		return nil, ErrNoCache
	}
	return res, nil
}

// ErrNoCache says nothing has been synced yet.
var ErrNoCache = errors.New("nothing cached yet: run `ade sync` first")

// ErrNoSprints says projects are cached but none has sprints, so there is
// nothing to review.
var ErrNoSprints = errors.New("no project has sprints: run `ade sync` once the tracker has them")

// manifests returns the manifest of every registered project by name.
func (c *Collector) manifests(ctx context.Context) (map[string]*config.Manifest, []string) {
	out := map[string]*config.Manifest{}
	var warns []string
	for _, ref := range c.App.Config.Projects {
		m, err := project.FetchManifest(ctx, c.App, ref)
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s: manifest: %v", ref.Source, err))
			continue
		}
		out[m.Name] = m
	}
	return out, warns
}

func (c *Collector) forgeFor(ctx context.Context, m *config.Manifest) (Forge, error) {
	fc, ok := c.App.Config.Forges[m.Forge]
	if !ok {
		return nil, fmt.Errorf("forge %q is not defined in the user config", m.Forge)
	}
	f, err := c.Open(ctx, m.Forge, fc)
	if err != nil {
		return nil, err
	}
	af, ok := f.(Forge)
	if !ok {
		return nil, fmt.Errorf("forge %q does not provide activity history", m.Forge)
	}
	return af, nil
}

// Standup builds the standup of the day containing date (zero: now). Sources
// that cannot be read become warnings; the cache is the floor.
func (c *Collector) Standup(ctx context.Context, date time.Time) (Standup, error) {
	cal, err := c.calendar()
	if err != nil {
		return Standup{}, err
	}
	results, err := c.cached()
	if err != nil {
		return Standup{}, err
	}
	now := c.now()
	if date.IsZero() {
		date = now
	}
	in := StandupInput{Date: date, Cal: cal, Now: now}
	from, to := in.Window()

	for _, r := range results {
		if r.Err == "" || len(r.Items) > 0 {
			in.Me = append(in.Me, r.Me)
			in.Items = append(in.Items, r.Items...)
			in.Reviews = append(in.Reviews, r.Reviews...)
		}
	}
	var warns []string
	mans, mw := c.manifests(ctx)
	warns = append(warns, mw...)
	type feed struct {
		f    Forge
		path string
		evs  []forge.Event
		err  error
	}
	var feeds []*feed
	for _, r := range results {
		m := mans[r.Project]
		if m == nil {
			continue
		}
		f, err := c.forgeFor(ctx, m)
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s: activity: %v", r.Project, err))
			continue
		}
		for _, path := range activityProjects(m) {
			feeds = append(feeds, &feed{f: f, path: path})
		}
	}
	var wg sync.WaitGroup
	for _, fd := range feeds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fd.evs, fd.err = fd.f.ProjectEvents(ctx, fd.path, from.AddDate(0, 0, -1), to.AddDate(0, 0, 1))
		}()
	}
	wg.Wait()
	for _, fd := range feeds {
		if fd.err != nil {
			warns = append(warns, fmt.Sprintf("%s: events: %v", fd.path, brief(fd.err)))
			continue
		}
		in.Activity = append(in.Activity, fd.evs...)
	}
	if entries, err := c.App.Audit.Query(audit.Filter{Since: from}); err != nil {
		warns = append(warns, "audit log: "+err.Error())
	} else {
		in.Audit = entries
	}
	if st, err := c.App.Cache(); err == nil {
		if items, err := notify.NewInbox(st).List(notify.ListOpts{}); err == nil {
			for _, it := range items {
				in.Inbox = append(in.Inbox, it.Event)
			}
		}
	}
	s := BuildStandup(in)
	s.Warnings = warns
	return s, nil
}

// brief drops the request URL from a transport failure: "dial tcp …: connection
// refused" says what a warning needs, the URL repeats the project.
func brief(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// activityProjects lists the forge projects whose feed matters: the tracker
// and every code repo.
func activityProjects(m *config.Manifest) []string {
	out := []string{m.Tracker.Project}
	for _, name := range m.RepoNames() {
		if p := m.Repos[name].Path; p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// ReviewOptions selects what to review.
type ReviewOptions struct {
	Project  string // ADE project; empty: the only one, else the first with sprints
	Sprint   string // sprint title or key; empty: the current sprint, else the latest finished
	Velocity int    // closed sprints in the velocity (default 3)
	Mine     bool
}

// DefaultVelocitySprints is how many closed sprints velocity spans.
const DefaultVelocitySprints = 3

// Review builds a sprint review from live forge data.
func (c *Collector) Review(ctx context.Context, o ReviewOptions) (Review, error) {
	cal, err := c.calendar()
	if err != nil {
		return Review{}, err
	}
	results, err := c.cached()
	if err != nil {
		return Review{}, err
	}
	res, err := pickProject(results, o.Project)
	note := ""
	var several *severalProjectsError
	if errors.As(err, &several) {
		// No --project: take the first project with sprints, as the dashboard
		// page does, and say so.
		if res, err = several.first(); err == nil {
			note = several.chose(res.Project)
		}
	}
	if err != nil {
		return Review{}, err
	}
	sp, err := PickSprint(res, o.Sprint)
	if err != nil {
		return Review{}, err
	}
	mans, warns := c.manifests(ctx)
	m := mans[res.Project]
	if m == nil {
		return Review{}, fmt.Errorf("project %q: manifest unavailable (%s)", res.Project, strings.Join(warns, "; "))
	}
	f, err := c.forgeFor(ctx, m)
	if err != nil {
		return Review{}, err
	}
	n := o.Velocity
	if n <= 0 {
		n = DefaultVelocitySprints
	}
	r, err := BuildReviewLive(ctx, f, ReviewLiveInput{
		Project: res.Project, Manifest: m, Sprint: sp, Sprints: res.Sprints, Me: res.Me, Items: res.Items,
		Mine: o.Mine, Cal: cal, Now: c.now(), VelocityN: n,
	})
	if note != "" && err == nil {
		r.Warnings = append(r.Warnings, note)
	}
	return r, err
}

func pickProject(results []forgesync.Result, name string) (forgesync.Result, error) {
	var ok []forgesync.Result
	for _, r := range results {
		if r.Err == "" || len(r.Items) > 0 || len(r.Sprints) > 0 {
			ok = append(ok, r)
		}
	}
	if name != "" {
		for _, r := range ok {
			if r.Project == name {
				return r, nil
			}
		}
		names := make([]string, len(results))
		for i, r := range results {
			names[i] = r.Project
		}
		return forgesync.Result{}, fmt.Errorf("no cached project %q (have: %v)", name, names)
	}
	switch len(ok) {
	case 0:
		return forgesync.Result{}, ErrNoCache
	case 1:
		return ok[0], nil
	}
	names := make([]string, len(ok))
	for i, r := range ok {
		names[i] = r.Project
	}
	return forgesync.Result{}, &severalProjectsError{names: names, results: ok}
}

// severalProjectsError is pickProject's answer to an empty name with several
// cached projects. Review recovers from it; other callers report it.
type severalProjectsError struct {
	names   []string
	results []forgesync.Result
}

func (e *severalProjectsError) Error() string {
	return fmt.Sprintf("several projects (%v): choose one with --project", e.names)
}

// first is the first cached project that has sprints (ErrNoSprints if none).
func (e *severalProjectsError) first() (forgesync.Result, error) {
	for _, r := range e.results {
		if len(r.Sprints) > 0 {
			return r, nil
		}
	}
	return forgesync.Result{}, ErrNoSprints
}

func (e *severalProjectsError) chose(name string) string {
	return fmt.Sprintf("several projects (%s): reviewing %s; choose another with --project", strings.Join(e.names, ", "), name)
}

// PickSprint finds the sprint to review: by title or key when named, else the
// current one, else the most recently finished.
func PickSprint(res forgesync.Result, name string) (domain.Sprint, error) {
	if name != "" {
		for _, s := range res.Sprints {
			if strings.EqualFold(s.Title, name) || s.Key == name {
				return s, nil
			}
		}
		return domain.Sprint{}, fmt.Errorf("no sprint %q in %s (have: %s)", name, res.Project, sprintTitles(res.Sprints))
	}
	if res.Window.Current != nil {
		return *res.Window.Current, nil
	}
	if res.Window.Previous != nil {
		return *res.Window.Previous, nil
	}
	if len(res.Sprints) > 0 {
		// no current sprint: the newest one that has already started
		for i := len(res.Sprints) - 1; i >= 0; i-- {
			if !res.Sprints[i].Start.After(res.SyncedAt) {
				return res.Sprints[i], nil
			}
		}
	}
	return domain.Sprint{}, fmt.Errorf("%s has no sprint to review", res.Project)
}

func sprintTitles(sp []domain.Sprint) string {
	t := make([]string, len(sp))
	for i, s := range sp {
		t[i] = s.Title
	}
	return strings.Join(t, ", ")
}

// ReviewLiveInput is what BuildReviewLive needs besides a forge.
type ReviewLiveInput struct {
	Project   string
	Manifest  *config.Manifest
	Sprint    domain.Sprint
	Sprints   []domain.Sprint
	Me        forge.User
	Items     []domain.WorkItem // cached items, for merged MRs per issue
	Mine      bool
	Cal       Calendar
	Now       time.Time
	VelocityN int
}

const (
	srcIterations = "gitlab-iterations"
	srcMilestones = "milestones"
	srcLabels     = "labels"
)

// sprintQuery selects a sprint's tracker issues; ok is false when the sprint
// cannot be expressed as a filter (an unfiltered query would match everything).
func sprintQuery(project string, sp domain.Sprint) (forge.IssueQuery, bool) {
	q := forge.IssueQuery{Project: project, State: "all"}
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

// BuildReviewLive fetches the sprint's issues, their timelines and the past
// sprints, then builds the review.
func BuildReviewLive(ctx context.Context, f Forge, in ReviewLiveInput) (Review, error) {
	tracker := in.Manifest.Tracker.Project
	q, ok := sprintQuery(tracker, in.Sprint)
	if !ok {
		return Review{}, fmt.Errorf("sprint %q cannot be queried on the forge", in.Sprint.Title)
	}
	issues, err := f.ListIssues(ctx, q)
	if err != nil {
		return Review{}, fmt.Errorf("sprint issues: %w", brief(err))
	}
	hist, warns := histories(ctx, f, issues)
	mergedAt := mergedTimes(in.Items)
	var refs []forge.Ref // only merged items have a cycle time worth a notes call
	for _, is := range issues {
		if _, ok := mergedAt[is.Ref]; ok && (!in.Mine || assignedTo(is, in.Me)) {
			refs = append(refs, is.Ref)
		}
	}
	assigned, awarns := assignedTimes(ctx, f, refs, in.Me, in.Mine)
	warns = append(warns, awarns...)

	var past []PastSprint
	for _, sp := range pastSprints(in.Sprints, in.Sprint, in.VelocityN) {
		pq, ok := sprintQuery(tracker, sp)
		if !ok {
			continue
		}
		pis, err := f.ListIssues(ctx, pq)
		if err != nil {
			warns = append(warns, fmt.Sprintf("velocity: %s: %v", sp.Title, brief(err)))
			continue
		}
		past = append(past, PastSprint{Sprint: sp, Issues: pis})
	}

	r := BuildReview(ReviewInput{
		Project: in.Project, Manifest: in.Manifest, Sprint: in.Sprint, Issues: issues, History: hist, Past: past,
		Me: in.Me, Mine: in.Mine, Cal: in.Cal, Now: in.Now, Merged: mergedMRs(in.Items),
		MergedAt: mergedAt, Assigned: assigned,
	})
	r.Warnings = warns
	return r, nil
}

// pastSprints picks the last n sprints that ended before sp began.
func pastSprints(all []domain.Sprint, sp domain.Sprint, n int) []domain.Sprint {
	var out []domain.Sprint
	for _, s := range all {
		if s.Due.Before(sp.Start.Time) {
			out = append(out, s)
		}
	}
	slices.SortStableFunc(out, func(a, b domain.Sprint) int { return a.Start.Compare(b.Start.Time) })
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// mergedMRs lists the merged MRs of each cached tracker item.
func mergedMRs(items []domain.WorkItem) map[forge.Ref][]string {
	out := map[forge.Ref][]string{}
	for _, wi := range items {
		for _, mr := range wi.MRs {
			if mr.State == "merged" {
				out[wi.Ref] = append(out[wi.Ref], short(mr.Ref))
			}
		}
	}
	return out
}

// mergedTimes is, per cached tracker item, when its last MR merged.
func mergedTimes(items []domain.WorkItem) map[forge.Ref]time.Time {
	out := map[forge.Ref]time.Time{}
	for _, wi := range items {
		for _, mr := range wi.MRs {
			if mr.State == "merged" && mr.MergedAt != nil && mr.MergedAt.After(out[wi.Ref]) {
				out[wi.Ref] = *mr.MergedAt
			}
		}
	}
	return out
}

// histories reads the label and sprint timelines of every issue, four at a
// time. An issue whose timeline cannot be read falls back to its dates and is
// named in a warning.
func histories(ctx context.Context, f Forge, issues []forge.Issue) (map[forge.Ref]issueHistory, []string) {
	out := make(map[forge.Ref]issueHistory, len(issues))
	var mu sync.Mutex
	var warns []string
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, is := range issues {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			var h issueHistory
			var errs []error
			var lerr, serr error
			h.Labels, lerr = f.LabelEvents(ctx, is.Ref)
			h.Sprint, serr = f.SprintEvents(ctx, is.Ref)
			errs = append(errs, lerr, serr)
			mu.Lock()
			defer mu.Unlock()
			out[is.Ref] = h
			if err := errors.Join(errs...); err != nil && !errors.Is(err, forge.ErrUnavailable) {
				warns = append(warns, fmt.Sprintf("%s: history: %v", is.Ref, err))
			}
		}()
	}
	wg.Wait()
	slices.Sort(warns)
	return out, warns
}
