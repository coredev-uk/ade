package gitlab

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// The wire types decode only what ADE reads. GitLab adds fields freely, and
// Premium fields (weight, iteration, epic) are absent on Free, so every
// optional field is a pointer or has a usable zero value.

type wireUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

func (u wireUser) user() forge.User {
	return forge.User{ID: u.ID, Username: u.Username, Name: u.Name}
}

func users(in []wireUser) []forge.User {
	if len(in) == 0 {
		return nil
	}
	out := make([]forge.User, len(in))
	for i, u := range in {
		out[i] = u.user()
	}
	return out
}

// wireDate decodes "YYYY-MM-DD" or null. forge.Date's own JSON decoding is
// the embedded time.Time's (RFC 3339), which GitLab's date fields are not.
type wireDate forge.Date

func (d *wireDate) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" {
		return nil
	}
	p, err := forge.ParseDate(s)
	if err != nil {
		return fmt.Errorf("date %q: %w", s, err)
	}
	*d = wireDate(p)
	return nil
}

func (d wireDate) date() forge.Date { return forge.Date(d) }

type wireMilestone struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	StartDate wireDate `json:"start_date"`
	DueDate   wireDate `json:"due_date"`
}

func (m wireMilestone) milestone() forge.Milestone {
	return forge.Milestone{ID: m.ID, Title: m.Title, State: m.State, Start: m.StartDate.date(), Due: m.DueDate.date()}
}

// wireIteration deliberately has no state field: REST reports state as an
// undocumented integer. State is derived from the dates or from the filter the
// iteration was listed under.
type wireIteration struct {
	ID        int64    `json:"id"`
	IID       int      `json:"iid"`
	Title     string   `json:"title"` // null for cadence-generated iterations
	StartDate wireDate `json:"start_date"`
	DueDate   wireDate `json:"due_date"`
}

// iteration converts to the forge type; state is "" to derive it from dates.
func (w wireIteration) iteration(state, cadence string, now time.Time) forge.Iteration {
	it := forge.Iteration{
		ID: w.ID, IID: w.IID, Title: w.Title, Cadence: cadence,
		State: state, Start: w.StartDate.date(), Due: w.DueDate.date(),
	}
	if it.State == "" {
		it.State = iterationState(it.Start, it.Due, now)
	}
	return it
}

// iterationState classifies by calendar date; Due is the last day, inclusive.
func iterationState(start, due forge.Date, now time.Time) string {
	today := forge.NewDate(now.Year(), now.Month(), now.Day())
	switch {
	case today.Before(start.Time):
		return "upcoming"
	case today.After(due.Time):
		return "closed"
	default:
		return "current"
	}
}

type wireRefs struct {
	Full string `json:"full"`
}

type wireIssue struct {
	ID          int64          `json:"id"`
	IID         int            `json:"iid"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	State       string         `json:"state"`
	WebURL      string         `json:"web_url"`
	Labels      []string       `json:"labels"`
	Author      wireUser       `json:"author"`
	Assignees   []wireUser     `json:"assignees"`
	Milestone   *wireMilestone `json:"milestone"`
	Iteration   *wireIteration `json:"iteration"`
	Weight      *int           `json:"weight"`
	UserNotes   *int           `json:"user_notes_count"`
	Tasks       struct {
		Count     int `json:"count"`
		Completed int `json:"completed_count"`
	} `json:"task_completion_status"`
	Epic *struct {
		Title string `json:"title"`
	} `json:"epic"`
	References wireRefs   `json:"references"`
	LinkType   string     `json:"link_type"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ClosedAt   *time.Time `json:"closed_at"`
}

// refOf prefers references.full, which names the owning project whichever
// project the request addressed. project is the fallback for responses that
// omit references.
func refOf(full, project string, iid int, kind forge.Kind) forge.Ref {
	if r, err := forge.ParseRef(full); err == nil && r.Kind == kind {
		return r
	}
	return forge.Ref{Project: project, IID: iid, Kind: kind}
}

func (w wireIssue) issue(project string, now time.Time) forge.Issue {
	is := forge.Issue{
		Ref:         refOf(w.References.Full, project, w.IID, forge.KindIssue),
		ID:          w.ID,
		Title:       w.Title,
		Description: w.Description,
		State:       w.State,
		WebURL:      w.WebURL,
		Labels:      w.Labels,
		Author:      w.Author.user(),
		Assignees:   users(w.Assignees),
		Weight:      w.Weight,
		Comments:    w.UserNotes,
		Tasks:       forge.TaskCount{Done: w.Tasks.Completed, Total: w.Tasks.Count},
		CreatedAt:   w.CreatedAt,
		UpdatedAt:   w.UpdatedAt,
		ClosedAt:    w.ClosedAt,
	}
	if w.Milestone != nil {
		m := w.Milestone.milestone()
		is.Milestone = &m
	}
	if w.Iteration != nil {
		it := w.Iteration.iteration("", "", now)
		is.Iteration = &it
	}
	if w.Epic != nil {
		is.Epic = w.Epic.Title
	}
	return is
}

func (w wireIssue) link(project string) forge.Link {
	return forge.Link{Ref: refOf(w.References.Full, project, w.IID, forge.KindIssue), Type: w.LinkType}
}

type wirePipeline struct {
	ID        int64     `json:"id"`
	Status    string    `json:"status"`
	WebURL    string    `json:"web_url"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (p *wirePipeline) pipeline() *forge.Pipeline {
	if p == nil {
		return nil
	}
	return &forge.Pipeline{ID: p.ID, Status: p.Status, WebURL: p.WebURL, UpdatedAt: p.UpdatedAt}
}

type wireMR struct {
	ID             int64         `json:"id"`
	IID            int           `json:"iid"`
	Title          string        `json:"title"`
	Description    string        `json:"description"`
	State          string        `json:"state"`
	Draft          bool          `json:"draft"`
	WIP            bool          `json:"work_in_progress"` // pre-19.0 spelling of draft
	SourceBranch   string        `json:"source_branch"`
	TargetBranch   string        `json:"target_branch"`
	SHA            string        `json:"sha"`
	WebURL         string        `json:"web_url"`
	Author         wireUser      `json:"author"`
	Assignees      []wireUser    `json:"assignees"`
	Reviewers      []wireUser    `json:"reviewers"`
	HeadPipeline   *wirePipeline `json:"head_pipeline"`
	Pipeline       *wirePipeline `json:"pipeline"`
	HasConflicts   bool          `json:"has_conflicts"`
	DetailedStatus string        `json:"detailed_merge_status"`
	References     wireRefs      `json:"references"`
	UpdatedAt      time.Time     `json:"updated_at"`
	MergedAt       *time.Time    `json:"merged_at"`
	MergeUser      *wireUser     `json:"merge_user"`
	MergedBy       *wireUser     `json:"merged_by"` // deprecated spelling of merge_user
}

// mr converts the list/single payload. Approvals and thread counts come from
// other endpoints and are filled in by Client.MR.
func (w wireMR) mr(project string) forge.MR {
	p := w.HeadPipeline
	if p == nil {
		p = w.Pipeline
	}
	return forge.MR{
		Ref:            refOf(w.References.Full, project, w.IID, forge.KindMR),
		ID:             w.ID,
		Title:          w.Title,
		Description:    w.Description,
		State:          w.State,
		Draft:          w.Draft || w.WIP,
		SourceBranch:   w.SourceBranch,
		TargetBranch:   w.TargetBranch,
		HeadSHA:        w.SHA,
		WebURL:         w.WebURL,
		Author:         w.Author.user(),
		Assignees:      users(w.Assignees),
		Reviewers:      users(w.Reviewers),
		Pipeline:       p.pipeline(),
		Approvals:      forge.Approvals{Required: -1},
		HasConflicts:   w.HasConflicts,
		DetailedStatus: w.DetailedStatus,
		UpdatedAt:      w.UpdatedAt,
		MergedAt:       w.MergedAt,
		MergedBy:       w.mergedBy(),
	}
}

func (w wireMR) mergedBy() forge.User {
	switch {
	case w.MergeUser != nil:
		return w.MergeUser.user()
	case w.MergedBy != nil:
		return w.MergedBy.user()
	}
	return forge.User{}
}

// wireReviewer is one entry of GET .../reviewers: the reviewer and their
// verdict (unreviewed, review_started, approved, requested_changes).
type wireReviewer struct {
	User  wireUser `json:"user"`
	State string   `json:"state"`
}

// changesRequested lists the reviewers whose verdict is requested_changes.
func changesRequested(rs []wireReviewer) []forge.User {
	var out []forge.User
	for _, r := range rs {
		if r.State == "requested_changes" {
			out = append(out, r.User.user())
		}
	}
	return out
}

type wireApprovals struct {
	Required   *int `json:"approvals_required"`
	ApprovedBy []struct {
		User wireUser `json:"user"`
	} `json:"approved_by"`
	// ApproversAvailable is GitLab's own licence check for approvers. Free
	// still answers approvals_required: 0, which would read as "none
	// required" rather than "unknown".
	ApproversAvailable *bool `json:"merge_request_approvers_available"`
}

func (w wireApprovals) approvals() forge.Approvals {
	a := forge.Approvals{Required: -1}
	if w.Required != nil && (w.ApproversAvailable == nil || *w.ApproversAvailable) {
		a.Required = *w.Required
	}
	for _, by := range w.ApprovedBy {
		a.ApprovedBy = append(a.ApprovedBy, by.User.user())
	}
	return a
}

type wireNote struct {
	ID         int64     `json:"id"`
	Author     wireUser  `json:"author"`
	Body       string    `json:"body"`
	System     bool      `json:"system"`
	CreatedAt  time.Time `json:"created_at"`
	Resolvable bool      `json:"resolvable"`
	Resolved   bool      `json:"resolved"`
	Position   *struct {
		NewPath string `json:"new_path"`
		OldPath string `json:"old_path"`
		NewLine int    `json:"new_line"`
		OldLine int    `json:"old_line"`
	} `json:"position"`
}

func (n wireNote) note() forge.Note {
	return forge.Note{ID: n.ID, Author: n.Author.user(), Body: n.Body, System: n.System, CreatedAt: n.CreatedAt}
}

type wireDiscussion struct {
	ID    string     `json:"id"`
	Notes []wireNote `json:"notes"`
}

// discussion converts a thread. A thread is resolvable when any note is, and
// resolved when every resolvable note is, which is how GitLab's UI counts.
func (w wireDiscussion) discussion() forge.Discussion {
	d := forge.Discussion{ID: w.ID, Notes: make([]forge.Note, 0, len(w.Notes))}
	resolved := true
	for _, n := range w.Notes {
		d.Notes = append(d.Notes, n.note())
		if n.Resolvable {
			d.Resolvable = true
			resolved = resolved && n.Resolved
		}
		if n.Position != nil && d.Position == nil {
			d.Position = &forge.Position{
				Path: positionPath(n.Position.NewPath, n.Position.OldPath), OldPath: n.Position.OldPath,
				NewLine: n.Position.NewLine, OldLine: n.Position.OldLine,
			}
		}
	}
	d.Resolved = d.Resolvable && resolved
	return d
}

func positionPath(newPath, oldPath string) string {
	if newPath != "" {
		return newPath
	}
	return oldPath
}

// threadCounts tallies resolvable threads, the numbers an MR header shows.
func threadCounts(ds []forge.Discussion) (unresolved, resolved int) {
	for _, d := range ds {
		switch {
		case !d.Resolvable:
		case d.Resolved:
			resolved++
		default:
			unresolved++
		}
	}
	return unresolved, resolved
}
