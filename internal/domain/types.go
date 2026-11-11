// Package domain derives ADE's view of work from forge facts: the work item
// (tracker issue + children + MRs), its stage, who must act next (court),
// label facets, weight, and the iteration window. Everything here is pure:
// no I/O, no clock reads (callers pass now).
package domain

import (
	"github.com/coredev-uk/ade/internal/forge"
)

// Stage is how far a work item has travelled. The first seven are the
// stations drawn on a track; Done is the tracker issue closed.
type Stage int

const (
	StageAssigned Stage = iota // assigned, nothing started
	StageStarted               // child issue exists (or work began)
	StageBranch                // a branch exists with no MR
	StageDraft                 // draft MR open
	StageCI                    // MR ready, pipeline running/pending/failed
	StageReview                // MR ready, pipeline ok or none, in review
	StageMerged                // every MR merged, tracker still open
	StageDone                  // tracker issue closed
)

// Stations are the stage names drawn on a track, in order.
var Stations = []string{"assigned", "started", "branch", "draft", "ci", "review", "merged"}

func (s Stage) String() string {
	if s == StageDone {
		return "done"
	}
	if int(s) >= 0 && int(s) < len(Stations) {
		return Stations[s]
	}
	return "unknown"
}

// Court says who must act next.
type Court string

const (
	CourtYou       Court = "you"
	CourtReviewers Court = "reviewers"
	CourtCI        Court = "ci"
	CourtAuthor    Court = "author"   // you are reviewing; the author must act
	CourtUpstream  Court = "upstream" // waits on another MR (Reason names it)
	CourtBlocked   Court = "blocked"
	CourtNobody    Court = "nobody"
)

// Sprint is one iteration from any source (GitLab iteration, milestone,
// label + cadence, GitHub project field).
type Sprint struct {
	Key    string     `json:"key"`   // stable id within its source
	Title  string     `json:"title"` // display title (generated when the forge has none)
	Start  forge.Date `json:"start"`
	Due    forge.Date `json:"due"` // last day
	Source string     `json:"source"`
}

// Window is the iteration window shown on Home: the current sprint plus the
// next (near its end) or the previous (near its start).
type Window struct {
	Current  *Sprint `json:"current,omitempty"`
	Previous *Sprint `json:"previous,omitempty"` // set only within overlap of Current's start
	Next     *Sprint `json:"next,omitempty"`     // set only within overlap of Current's end
	DaysLeft int     `json:"days_left"`          // working days left in Current, today included
}

// WorkItem is the unit ADE shows: a tracker issue, its child issues in code
// repos, and their merge requests, with derived state.
type WorkItem struct {
	Project  string        `json:"project"` // ADE project (manifest name)
	Ref      forge.Ref     `json:"ref"`     // the tracker issue (or the issue itself when untracked)
	Issue    forge.Issue   `json:"issue"`
	Children []forge.Issue `json:"children,omitempty"`
	MRs      []forge.MR    `json:"mrs,omitempty"`

	Stage  Stage  `json:"stage"`
	Court  Court  `json:"court"`
	Reason string `json:"reason"` // evidence for the court: "2 unresolved threads", "pipeline failed"

	Type     string   `json:"type,omitempty"`     // labels.type facet value
	Apps     []string `json:"apps,omitempty"`     // labels.app facet values
	Repos    []string `json:"repos,omitempty"`    // manifest repos the apps route to
	Workflow string   `json:"workflow,omitempty"` // current labels.workflow value(s), newest wins
	// WantWorkflow is the workflow value the derived stage maps to; Drift is
	// true when it differs from Workflow.
	WantWorkflow string `json:"want_workflow,omitempty"`
	Drift        bool   `json:"drift,omitempty"`

	Weight  *int    `json:"weight,omitempty"` // nil = unweighted
	Sprint  *Sprint `json:"sprint,omitempty"`
	Blocked bool    `json:"blocked,omitempty"`
	// Triage lists missing facets: "type", "app", "weight".
	Triage []string `json:"triage,omitempty"`
	// Upstream is the MR this item's MR waits on (repo requires), if any.
	Upstream *forge.Ref `json:"upstream,omitempty"`
	Assigned bool       `json:"assigned"` // assigned to the user
	// KeptOpen marks a done item whose tracker issue is still open: finished by
	// its labels.workflow.done label, closed later (typically at sprint end).
	KeptOpen bool `json:"kept_open,omitempty"`
}
