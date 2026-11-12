// Package sprintmove is the explicit, audited move of an issue to another
// sprint (PLAN 3, "Moving to the next sprint"): `ade issue move` and the TUI's
// `>` both plan with Target.Plans, show the plan, and write it with Apply.
// Where the forge licenses iterations (GitLab Premium) the iteration is set;
// where sprints are milestones the milestone is; where they are numbered labels
// the old sprint label is removed and the new one added. ADE moves issues
// between sprints but never closes them: the project lead does that.
package sprintmove

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/startwork"
	"github.com/coredev-uk/ade/internal/weight"
)

// Modes of a plan: how the sprint is stored.
const (
	ModeIteration = "iteration"
	ModeMilestone = "milestone"
	ModeLabel     = "label"
)

// Children says which child issues follow their tracker issue.
type Children int

const (
	// ChildrenSame moves the children that carry the tracker issue's sprint.
	ChildrenSame Children = iota
	// ChildrenAll moves every open child, whatever its sprint.
	ChildrenAll
	// ChildrenNone leaves the children alone.
	ChildrenNone
)

// Target is a tracker issue with its forge, its open children (weight.Target)
// and the project's sprints.
type Target struct {
	*weight.Target
	Sprints []domain.Sprint
	Now     time.Time
}

// Resolve finds the registered project whose tracker holds the issue named by
// arg and reads it with its sprints. open nil uses startwork.OpenForge.
func Resolve(ctx context.Context, a *app.App, open startwork.OpenFunc, arg string, now time.Time) (*Target, error) {
	wt, err := weight.Resolve(ctx, a, open, arg)
	if err != nil {
		return nil, err
	}
	return withSprints(ctx, wt, now)
}

// ResolveAll resolves several tracker issues, reading each project's sprints
// once. It stops at the first issue that cannot be read.
func ResolveAll(ctx context.Context, a *app.App, open startwork.OpenFunc, args []string, now time.Time) ([]*Target, error) {
	byProject := map[string][]domain.Sprint{}
	var out []*Target
	for _, arg := range args {
		wt, err := weight.Resolve(ctx, a, open, arg)
		if err != nil {
			return nil, err
		}
		sp, ok := byProject[wt.Project]
		if !ok {
			t, err := withSprints(ctx, wt, now)
			if err != nil {
				return nil, err
			}
			byProject[wt.Project] = t.Sprints
			out = append(out, t)
			continue
		}
		out = append(out, &Target{Target: wt, Sprints: sp, Now: now})
	}
	return out, nil
}

// ResolveWith reads the issue, its children and the sprints through an open forge.
func ResolveWith(ctx context.Context, f startwork.Forge, m *config.Manifest, ref forge.Ref, now time.Time) (*Target, error) {
	wt, err := weight.ResolveWith(ctx, f, m, ref)
	if err != nil {
		return nil, err
	}
	return withSprints(ctx, wt, now)
}

func withSprints(ctx context.Context, wt *weight.Target, now time.Time) (*Target, error) {
	sp, err := forgesync.LoadSprints(ctx, wt.Forge, wt.Manifest, wt.Caps, now)
	if err != nil {
		return nil, fmt.Errorf("sprints of %s: %w", wt.Project, err)
	}
	return &Target{Target: wt, Sprints: sp, Now: now}, nil
}

// SprintOf is the sprint an issue is in under the manifest's source; nil for none.
func (t *Target) SprintOf(is forge.Issue) *domain.Sprint {
	return domain.SprintOf(is, t.Manifest.Tracker.Iterations, t.Sprints)
}

// Current is the tracker issue's own sprint.
func (t *Target) Current() *domain.Sprint { return t.SprintOf(t.Issue) }

// Next is the sprint "next" names for this issue: the first sprint that starts
// after the issue's own sprint and has not ended (the Home window's next sprint
// for an issue in the current sprint; the current sprint for work left behind in
// a past one). An issue in no sprint gets the window's next sprint. Nothing is
// created: without a next sprint the error says what to create.
func (t *Target) Next() (domain.Sprint, error) {
	cur := t.Current()
	if cur == nil {
		w := domain.WindowAt(t.Sprints, t.Now, nil, 1<<30)
		if w.Next != nil {
			return *w.Next, nil
		}
		return domain.Sprint{}, t.noNext(nil)
	}
	sorted := slices.Clone(t.Sprints)
	slices.SortStableFunc(sorted, func(a, b domain.Sprint) int { return a.Start.Compare(b.Start.Time) })
	today := dateOf(t.Now)
	for _, s := range sorted {
		if s.Start.After(cur.Due.Time) && !s.Due.Before(today) {
			return s, nil
		}
	}
	return domain.Sprint{}, t.noNext(cur)
}

func dateOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// noNext is the "no next sprint" error with the exact fix for the source.
func (t *Target) noNext(cur *domain.Sprint) error {
	after := "today"
	if cur != nil {
		after = cur.Title + " (ends " + cur.Due.String() + ")"
	}
	src := t.Manifest.Tracker.Iterations
	group := forge.Group(t.Manifest.Tracker.Project)
	// A user namespace has no group to create milestones in.
	milestoneScope := "group " + group
	if t.Caps.UserNamespace {
		milestoneScope = "project " + t.Manifest.Tracker.Project
	}
	var fix string
	switch {
	case cur != nil && cur.Source == "gitlab-iterations", src.Source == "gitlab-iterations", src.Source == "cadence":
		fix = fmt.Sprintf("iteration cadence has no upcoming iteration: in group %s turn on automatic scheduling with at least one upcoming iteration, or create the next iteration, then run this again", group)
	case cur != nil && cur.Source == "labels", src.Source == "labels":
		fix = fmt.Sprintf("the label for the next sprint (pattern %s) is not on any issue yet: create it, or apply it to one issue, then run this again", src.Pattern)
	default:
		fix = fmt.Sprintf("create the next milestone in %s (Plan > Milestones > New milestone, starting after that sprint ends), then run this again", milestoneScope)
	}
	return fmt.Errorf("no sprint starts after %s: %s (ADE creates no sprints)", after, fix)
}

// Find picks the sprint spec names: "" or "next", else a sprint's title or key.
func (t *Target) Find(spec string) (domain.Sprint, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "next") {
		return t.Next()
	}
	for _, s := range t.Sprints {
		if strings.EqualFold(s.Title, spec) || s.Key == spec {
			return s, nil
		}
	}
	var titles []string
	for _, s := range t.Sprints {
		if !s.Due.Before(dateOf(t.Now)) {
			titles = append(titles, s.Title)
		}
	}
	return domain.Sprint{}, fmt.Errorf("no sprint %q in %s (sprints still to come: %s)", spec, t.Project, strings.Join(titles, ", "))
}

// Plan is one issue's sprint change.
type Plan struct {
	Ref   forge.Ref `json:"ref"`
	Title string    `json:"title,omitempty"`
	URL   string    `json:"url,omitempty"`
	Mode  string    `json:"mode"`
	// From is the sprint the issue is in; nil for none.
	From *domain.Sprint `json:"from"`
	To   domain.Sprint  `json:"to"`
	// Add and Remove are the label changes of a label plan.
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
	// Unchanged is true when the issue already is in To: nothing is written.
	Unchanged bool `json:"unchanged,omitempty"`
	// Child marks a child issue that follows its tracker issue.
	Child bool     `json:"child,omitempty"`
	Notes []string `json:"notes,omitempty"`
}

// SprintName renders a sprint for words: its title, or "no sprint".
func SprintName(s *domain.Sprint) string {
	if s == nil {
		return "no sprint"
	}
	return s.Title
}

func sameSprint(a *domain.Sprint, b domain.Sprint) bool {
	return a != nil && a.Source == b.Source && a.Key == b.Key
}

func sameOrNone(a, b *domain.Sprint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Source == b.Source && a.Key == b.Key
}

func modeOf(s domain.Sprint) (string, error) {
	switch s.Source {
	case "gitlab-iterations":
		return ModeIteration, nil
	case "milestones":
		return ModeMilestone, nil
	case "labels":
		return ModeLabel, nil
	}
	return "", fmt.Errorf("sprints of source %q are read-only: ADE moves issues between iterations, milestones and sprint labels", s.Source)
}

// buildPlan plans moving one issue into to.
func (t *Target) buildPlan(is forge.Issue, to domain.Sprint) (Plan, error) {
	mode, err := modeOf(to)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Ref: is.Ref, Title: is.Title, URL: is.WebURL, Mode: mode, From: t.SprintOf(is), To: to}
	if mode != ModeLabel {
		p.Unchanged = sameSprint(p.From, to)
		return p, nil
	}
	pattern := t.Manifest.Tracker.Iterations.Pattern
	old := domain.SprintLabels(is.Labels, pattern)
	for _, l := range old {
		if !strings.EqualFold(l, to.Key) {
			p.Remove = append(p.Remove, l)
		}
	}
	if !slices.ContainsFunc(old, func(l string) bool { return strings.EqualFold(l, to.Key) }) {
		p.Add = []string{to.Key}
	}
	p.Unchanged = len(p.Add) == 0 && len(p.Remove) == 0
	return p, nil
}

// Plans plans moving the tracker issue to the sprint and, as ch says, its open
// children. A child that stays behind is named in the tracker plan's notes.
func (t *Target) Plans(to domain.Sprint, ch Children) ([]Plan, error) {
	if t.Issue.State != "opened" {
		return nil, fmt.Errorf("%s is %s: a closed issue stays in its sprint", t.Issue.Ref, t.Issue.State)
	}
	p, err := t.buildPlan(t.Issue, to)
	if err != nil {
		return nil, err
	}
	plans := []Plan{p}
	if ch == ChildrenNone {
		return plans, nil
	}
	for _, c := range t.Children {
		if ch == ChildrenSame && !sameOrNone(t.SprintOf(c.Issue), p.From) {
			plans[0].Notes = append(plans[0].Notes, fmt.Sprintf("child %s is in %s, not %s: left alone (--with-children moves it)",
				c.Issue.Ref, SprintName(t.SprintOf(c.Issue)), SprintName(p.From)))
			continue
		}
		cp, err := t.buildPlan(c.Issue, to)
		if err != nil {
			return nil, fmt.Errorf("child %s: %w", c.Issue.Ref, err)
		}
		cp.Child = true
		plans = append(plans, cp)
	}
	return plans, nil
}

// Describe is the plan in one line, for dry runs and confirmations.
func (p Plan) Describe() string {
	who := p.Ref.String()
	if p.Child {
		who += " (child)"
	}
	if p.Unchanged {
		return fmt.Sprintf("%s: already in %s; nothing to write", who, p.To.Title)
	}
	line := fmt.Sprintf("%s: %s → %s (%s", who, SprintName(p.From), p.To.Title, p.Mode)
	if p.Mode == ModeLabel {
		var parts []string
		if len(p.Add) > 0 {
			parts = append(parts, "add "+strings.Join(p.Add, ", "))
		}
		if len(p.Remove) > 0 {
			parts = append(parts, "remove "+strings.Join(p.Remove, ", "))
		}
		line += ": " + strings.Join(parts, "; ")
	}
	return line + ")"
}

// AnyWrite reports whether any plan writes something.
func AnyWrite(plans []Plan) bool {
	return slices.ContainsFunc(plans, func(p Plan) bool { return !p.Unchanged })
}

var errNoMilestoneWriter = errors.New("this forge cannot set milestones")
