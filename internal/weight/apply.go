package weight

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/startwork"
)

// Target is a tracker issue with the forge that serves it and the children
// start work made for it.
type Target struct {
	Project  string // ADE project (manifest name)
	Manifest *config.Manifest
	Forge    startwork.Forge
	Issue    forge.Issue
	Caps     forge.Caps
	Children []Child
}

// Child is a child issue that follows its tracker issue's weight.
type Child struct {
	Issue forge.Issue
	Caps  forge.Caps
}

var bareIID = regexp.MustCompile(`^#?(\d+)$`)

// Resolve finds the registered project whose tracker holds the issue named by
// arg ("group/tracker#3", or "3" when one project is registered) and reads it.
// open nil uses startwork.OpenForge.
func Resolve(ctx context.Context, a *app.App, open startwork.OpenFunc, arg string) (*Target, error) {
	if open == nil {
		open = startwork.OpenForge
	}
	if len(a.Config.Projects) == 0 {
		return nil, errors.New("no projects registered: run `ade project add <manifest>`")
	}
	var ms []*config.Manifest
	for _, pr := range a.Config.Projects {
		m, err := project.FetchManifest(ctx, a, pr)
		if err != nil {
			return nil, fmt.Errorf("manifest %s: %w", pr.Source, err)
		}
		ms = append(ms, m)
	}
	var ref forge.Ref
	if mt := bareIID.FindStringSubmatch(strings.TrimSpace(arg)); mt != nil {
		if len(ms) != 1 {
			return nil, fmt.Errorf("%q is ambiguous with %d projects registered: use group/project#N", arg, len(ms))
		}
		iid, _ := strconv.Atoi(mt[1])
		ref = forge.Ref{Project: ms[0].Tracker.Project, IID: iid, Kind: forge.KindIssue}
	} else {
		var err error
		if ref, err = forge.ParseRef(arg); err != nil {
			return nil, err
		}
		if ref.Kind != forge.KindIssue {
			return nil, fmt.Errorf("%s is a merge request: weights belong to issues", ref)
		}
	}
	for _, m := range ms {
		if m.Tracker.Project != ref.Project {
			continue
		}
		fc, ok := a.Config.Forges[m.Forge]
		if !ok {
			return nil, fmt.Errorf("forge %q is not defined in the user config", m.Forge)
		}
		f, err := open(ctx, m.Forge, fc)
		if err != nil {
			return nil, err
		}
		return ResolveWith(ctx, f, m, ref)
	}
	return nil, fmt.Errorf("%s is not in the tracker project of any registered project", ref)
}

// ResolveWith reads the issue, its capabilities and its children through an
// open forge.
func ResolveWith(ctx context.Context, f startwork.Forge, m *config.Manifest, ref forge.Ref) (*Target, error) {
	is, err := f.Issue(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	caps, err := f.Capabilities(ctx, forge.Group(ref.Project))
	if err != nil {
		return nil, fmt.Errorf("capabilities of %s: %w", ref.Project, err)
	}
	t := &Target{Project: m.Name, Manifest: m, Forge: f, Issue: is, Caps: caps}
	links, err := f.Links(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("links of %s: %w", ref, err)
	}
	for _, l := range links {
		if !domain.IsChildLink(l) || l.Ref == ref {
			continue
		}
		kid, err := f.Issue(ctx, l.Ref)
		if err != nil {
			return nil, fmt.Errorf("child %s: %w", l.Ref, err)
		}
		// A relates_to link is also how unrelated issues are linked: only an
		// issue that names this one as its parent (or a native sub-issue)
		// follows the weight.
		if l.Type != "sub_issue" && !slices.Contains(domain.ParentRefs(kid), ref) {
			continue
		}
		if kid.State != "opened" || slices.ContainsFunc(t.Children, func(c Child) bool { return c.Issue.Ref == kid.Ref }) {
			continue
		}
		kc := caps
		if forge.Group(kid.Ref.Project) != forge.Group(ref.Project) {
			if kc, err = f.Capabilities(ctx, forge.Group(kid.Ref.Project)); err != nil {
				return nil, fmt.Errorf("capabilities of %s: %w", kid.Ref.Project, err)
			}
		}
		t.Children = append(t.Children, Child{Issue: kid, Caps: kc})
	}
	return t, nil
}

// Plans plans the weight change for the issue and, when withChildren, for
// every open child start work made for it.
func (t *Target) Plans(to *int, withChildren bool) ([]Plan, error) {
	src := t.Manifest.Tracker.Weight
	p, err := BuildPlan(t.Issue, t.Caps, src, to)
	if err != nil {
		return nil, err
	}
	plans := []Plan{p}
	if !withChildren {
		return plans, nil
	}
	for _, c := range t.Children {
		cp, err := BuildPlan(c.Issue, c.Caps, src, to)
		if err != nil {
			return nil, fmt.Errorf("child %s: %w", c.Issue.Ref, err)
		}
		cp.Child = true
		plans = append(plans, cp)
	}
	return plans, nil
}

// Result is a plan after Apply.
type Result struct {
	Plan    Plan   `json:"plan"`
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}

// Apply writes the plans in order, one audited forge.issue.weight entry per
// write (the label changes of a label plan are part of that one action). An
// unchanged plan writes and audits nothing. It stops at the first failure: the
// results so far, and the failing one, are returned with the error.
func Apply(ctx context.Context, w startwork.Forge, a *app.App, project, actor string, plans []Plan) ([]Result, error) {
	var out []Result
	for _, p := range plans {
		r := Result{Plan: p}
		if p.Unchanged {
			out = append(out, r)
			continue
		}
		err := write(ctx, w, p)
		ent := audit.Entry{
			Actor: actor, Action: "forge.issue.weight", Target: p.Ref.String(), Item: p.Ref.String(), Project: project,
			Params: map[string]any{
				"from": Value(p.From), "to": Value(p.To), "mode": p.Mode,
				"add": p.Add, "remove": p.Remove, "child": p.Child,
			},
			Outcome: "ok",
		}
		if err != nil {
			ent.Outcome, ent.Error = "error", err.Error()
			r.Error = err.Error()
		} else {
			r.Applied = true
		}
		if a != nil && a.Audit != nil {
			if aerr := a.Audit.Append(ent); aerr != nil && err == nil {
				err = fmt.Errorf("weight written, but the audit entry failed: %w", aerr)
			}
		}
		out = append(out, r)
		if err != nil {
			return out, fmt.Errorf("%s: %w", p.Ref, err)
		}
	}
	return out, nil
}

func write(ctx context.Context, w startwork.Forge, p Plan) error {
	if p.Mode == ModeField {
		ww, ok := w.(forge.WeightWriter)
		if !ok {
			return fmt.Errorf("forge %s cannot write the weight field", w.Kind())
		}
		return ww.SetWeight(ctx, p.Ref, p.To)
	}
	return w.SetLabels(ctx, p.Ref, p.Add, p.Remove)
}
