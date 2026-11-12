package domain

import (
	"slices"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
)

// AssembleInput is everything needed to derive one WorkItem. The caller does
// the fetching; Assemble only interprets.
type AssembleInput struct {
	Project  string // ADE project (manifest name)
	Manifest *config.Manifest
	Me       string // current forge username
	MeID     int64  // current forge user id; 0 when unknown
	Tracker  forge.Issue
	// Parent is the tracker issue above Tracker when Tracker is an issue of a
	// code repo: it supplies the type, apps and weight Tracker lacks.
	Parent     *forge.Issue
	Links      []forge.Link  // the tracker's links
	Candidates []forge.Issue // issues that may be children; unrelated ones are ignored
	// Parents maps a candidate to the tracker issue the forge placed it under
	// (hierarchy or relates_to link); a Parent: line in its description counts
	// without an entry.
	Parents  map[forge.Ref]forge.Ref
	MRs      []forge.MR // MRs of the tracker and its children
	Branches []string   // branches known for the item's work (no MR yet)
	Sprints  []Sprint   // for resolving the item's sprint
	Now      time.Time  // evaluation time; stage/court do not depend on it today
}

// Assemble derives a work item from forge facts.
func Assemble(in AssembleInput) WorkItem {
	m := in.Manifest
	if m == nil {
		m = &config.Manifest{}
	}
	f, weight := Inherit(in.Tracker, in.Parent, m)
	kids := children(in.Tracker, in.Links, in.Parents, in.Candidates)

	item := WorkItem{
		Project:  in.Project,
		Ref:      in.Tracker.Ref,
		Issue:    in.Tracker,
		Children: kids,
		MRs:      in.MRs,
		Type:     f.Type,
		Apps:     f.Apps,
		Repos:    reposFor(m, f.Apps),
		Workflow: f.Workflow,
		Weight:   weight,
		Sprint:   SprintOf(in.Tracker, m.Tracker.Iterations, in.Sprints),
		Assigned: assignedTo(in.Tracker, in.Me, in.MeID) || slices.ContainsFunc(kids, func(k forge.Issue) bool {
			return k.State == "opened" && assignedTo(k, in.Me, in.MeID)
		}),
	}
	item.Stage = deriveStage(in.Tracker, kids, in.MRs, in.Branches)
	// The team's done label finishes an open tracker, unless an MR is still
	// open: then the label is ahead of the work and shows as drift instead.
	if d := m.Labels.Workflow.Done; item.Stage != StageDone && d != "" &&
		strings.EqualFold(item.Workflow, d) && len(openMRs(in.MRs)) == 0 {
		item.Stage, item.KeptOpen = StageDone, true
	}
	blocked := blockedReason(in.Tracker, in.Links, m.Labels)
	item.Blocked = blocked != "" && item.Stage != StageDone
	item.Court, item.Reason, item.Upstream = deriveCourt(&item, m, in.Me, blocked)

	item.WantWorkflow = WantWorkflow(item.Stage, m.Labels)
	if item.KeptOpen {
		item.WantWorkflow = item.Workflow
	}
	// A deliberate "blocked" label is not drift from the derived stage.
	item.Drift = item.WantWorkflow != "" && item.Workflow != item.WantWorkflow && !strings.EqualFold(item.Workflow, blockedValue)
	item.Triage = triage(item, m)
	return item
}

// Inherit returns the facets and weight of an issue of a code repo: its own
// when it has them, else those of its tracker parent (nil for none).
func Inherit(issue forge.Issue, parent *forge.Issue, m *config.Manifest) (Facets, *int) {
	f := FacetsOf(issue.Labels, m.Labels)
	weight := ResolveWeight(issue, m.Tracker.Weight)
	if parent == nil || parent.Ref == issue.Ref {
		return f, weight
	}
	pf := FacetsOf(parent.Labels, m.Labels)
	if f.Type == "" {
		f.Type = pf.Type
	}
	if len(f.Apps) == 0 {
		f.Apps = pf.Apps
	}
	if weight == nil {
		weight = ResolveWeight(*parent, m.Tracker.Weight)
	}
	return f, weight
}

// assignedTo reports whether me is among the issue's assignees: by user id
// when both sides know one (a renamed account keeps its id), else by handle.
func assignedTo(issue forge.Issue, me string, meID int64) bool {
	return slices.ContainsFunc(issue.Assignees, func(u forge.User) bool {
		if u.ID != 0 && meID != 0 {
			return u.ID == meID
		}
		return me != "" && strings.EqualFold(u.Username, me)
	})
}

// reposFor lists the manifest repos any of apps routes to, in manifest order.
func reposFor(m *config.Manifest, apps []string) []string {
	var out []string
	for _, name := range m.RepoNames() {
		for _, a := range m.Repos[name].App {
			if slices.ContainsFunc(apps, func(x string) bool { return strings.EqualFold(x, a) }) {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// triage lists the facets still missing on an open item, limited to those the
// manifest configures. Closed items need no triage.
func triage(item WorkItem, m *config.Manifest) []string {
	if item.Stage == StageDone {
		return nil
	}
	var out []string
	if item.Type == "" {
		out = append(out, "type")
	}
	if m.Labels.App.Pattern != "" && len(item.Apps) == 0 {
		out = append(out, "app")
	}
	if m.Tracker.Weight.Source != "none" && item.Weight == nil {
		out = append(out, "weight")
	}
	return out
}
