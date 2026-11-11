package fake

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// The agile endpoints: project events and the label and sprint-scope
// timelines of issues. Data is derived from the seeded state (creation,
// closing, merging, notes, current labels and sprint); tests add explicit
// history through the methods below, which replaces the derived timeline of
// the issue they name.

func init() { registerRoutes(agileRoutes) }

func agileRoutes(s *Server, mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v4/projects/{id}/events", s.projectEvents)
	mux.HandleFunc("GET /api/v4/projects/{id}/issues/{iid}/resource_label_events", s.labelEvents)
	mux.HandleFunc("GET /api/v4/projects/{id}/issues/{iid}/resource_milestone_events", s.milestoneEvents)
	mux.HandleFunc("GET /api/v4/projects/{id}/issues/{iid}/resource_iteration_events", s.iterationEvents)
}

// agileState is the explicit history tests recorded.
type agileState struct {
	labels  map[forge.Ref][]forge.LabelEvent
	sprints map[forge.Ref][]forge.SprintEvent
	events  []forge.Event
}

func (s *Server) agile() *agileState {
	return s.ext("agile", func() any {
		return &agileState{labels: map[forge.Ref][]forge.LabelEvent{}, sprints: map[forge.Ref][]forge.SprintEvent{}}
	}).(*agileState)
}

// IssueSpec describes an issue for AddIssue. Project is the full path
// (default: the group's tracker). Milestone names a seeded or added milestone.
type IssueSpec struct {
	Project   string
	Title     string
	Labels    []string
	Milestone string
	Weight    *int   // Premium only
	Epic      string // Premium only
	Assigned  bool   // to the sandbox user
	Created   time.Time
	Closed    *time.Time
}

// AddIssue creates an issue and returns its reference.
func (s *Server) AddIssue(sp IssueSpec) (forge.Ref, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := sp.Project
	if path == "" {
		path = GroupPath + "/tracker"
	}
	p := s.st.findProject(path)
	if p == nil {
		return forge.Ref{}, fmt.Errorf("fake: no project %q", path)
	}
	iid := 1
	for _, o := range s.st.issues {
		if o.Project == p {
			iid++
		}
	}
	created := sp.Created
	if created.IsZero() {
		created = s.st.clock
	}
	is := &issue{
		ID: s.st.id(), IID: iid, Project: p, Title: sp.Title, State: "opened", Labels: sp.Labels,
		Author: s.st.me, Created: created, Updated: created,
	}
	if sp.Assigned {
		is.Assignees = []*user{s.st.me}
	}
	if sp.Milestone != "" {
		idx := slices.IndexFunc(s.st.milestones, func(m *milestone) bool { return m.Title == sp.Milestone })
		if idx < 0 {
			return forge.Ref{}, fmt.Errorf("fake: no milestone %q", sp.Milestone)
		}
		is.Milestone = s.st.milestones[idx]
		if s.tier == Premium {
			is.Iteration = s.st.iterationFor(idx)
		}
	}
	if s.tier == Premium {
		is.Weight, is.Epic = sp.Weight, sp.Epic
	}
	if sp.Closed != nil {
		t := *sp.Closed
		is.State, is.Closed, is.Updated = "closed", &t, t
	}
	s.st.issues = append(s.st.issues, is)
	return s.st.ref(p, iid, forge.KindIssue), nil
}

// AddMilestone adds a sprint milestone (dates "YYYY-MM-DD").
func (s *Server) AddMilestone(title, state, start, due string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.milestones = append(s.st.milestones, &milestone{
		ID: s.st.id(), IID: len(s.st.milestones) + 1, Title: title, State: state, Start: start, Due: due,
	})
}

// SetClosedAt closes an issue at t (or reopens it when t is zero).
func (s *Server) SetClosedAt(ref forge.Ref, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, err := s.issue(ref)
	if err != nil {
		return err
	}
	if t.IsZero() {
		is.State, is.Closed = "opened", nil
		return nil
	}
	is.State, is.Closed, is.Updated = "closed", &t, t
	return nil
}

// SetLabels replaces an issue's labels.
func (s *Server) SetLabels(ref forge.Ref, labels ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, err := s.issue(ref)
	if err != nil {
		return err
	}
	is.Labels = labels
	return nil
}

// AddLabelEvent records a label change; the issue's timeline is then exactly
// the recorded events.
func (s *Server) AddLabelEvent(ref forge.Ref, label, action string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.issue(ref); err != nil {
		return err
	}
	a := s.agile()
	a.labels[ref] = append(a.labels[ref], forge.LabelEvent{Label: label, Action: action, At: at})
	return nil
}

// AddSprintEvent records an issue entering ("add") or leaving ("remove") the
// milestone called title; the issue's sprint timeline is then exactly the
// recorded events.
func (s *Server) AddSprintEvent(ref forge.Ref, title, action string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.issue(ref); err != nil {
		return err
	}
	idx := slices.IndexFunc(s.st.milestones, func(m *milestone) bool { return m.Title == title })
	if idx < 0 {
		return fmt.Errorf("fake: no milestone %q", title)
	}
	a := s.agile()
	a.sprints[ref] = append(a.sprints[ref], forge.SprintEvent{
		Source: forge.SprintByMilestone, ID: s.st.milestones[idx].ID, Title: title, Action: action, At: at,
	})
	return nil
}

// AddEvent records an activity-feed entry for project events (author is the
// sandbox user when empty).
func (s *Server) AddEvent(e forge.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Author.Username == "" {
		e.Author = forge.User{ID: s.st.me.ID, Username: s.st.me.Username, Name: s.st.me.Name}
	}
	a := s.agile()
	e.ID = s.st.id()
	a.events = append(a.events, e)
}

func (s *Server) issueFor(w http.ResponseWriter, r *http.Request) (*issue, bool) {
	p := s.st.findProject(r.PathValue("id"))
	iid, _ := strconv.Atoi(r.PathValue("iid"))
	if p == nil {
		notFound(w)
		return nil, false
	}
	is := s.st.findIssue(p, iid)
	if is == nil {
		notFound(w)
		return nil, false
	}
	return is, true
}

func (s *Server) labelEvents(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, ok := s.issueFor(w, r)
	if !ok {
		return
	}
	ref := s.st.ref(is.Project, is.IID, forge.KindIssue)
	evs, explicit := s.agile().labels[ref]
	if !explicit {
		for _, l := range is.Labels {
			at := is.Created
			if strings.HasPrefix(l, "workflow::") {
				at = is.Updated
			}
			evs = append(evs, forge.LabelEvent{Label: l, Action: "add", At: at})
		}
	}
	slices.SortStableFunc(evs, func(a, b forge.LabelEvent) int { return a.At.Compare(b.At) })
	paged(s, w, r, evs, func(e forge.LabelEvent) any {
		return object{
			"id": s.st.id(), "user": userJSON(s.st.me), "created_at": ts(e.At), "resource_type": "Issue",
			"resource_id": is.ID, "label": object{"id": 1, "name": e.Label}, "action": e.Action,
		}
	})
}

// sprintEventsFor returns the recorded or derived scope timeline of an issue.
func (s *Server) sprintEventsFor(is *issue) []forge.SprintEvent {
	ref := s.st.ref(is.Project, is.IID, forge.KindIssue)
	if evs, ok := s.agile().sprints[ref]; ok {
		return evs
	}
	var evs []forge.SprintEvent
	if m := is.Milestone; m != nil {
		evs = append(evs, forge.SprintEvent{Source: forge.SprintByMilestone, ID: m.ID, Title: m.Title, Action: "add", At: is.Created})
	}
	if it := is.Iteration; it != nil {
		evs = append(evs, forge.SprintEvent{Source: forge.SprintByIteration, ID: it.ID, Title: it.Title, Action: "add", At: is.Created})
	}
	return evs
}

func (s *Server) milestoneEvents(w http.ResponseWriter, r *http.Request) {
	s.sprintEvents(w, r, forge.SprintByMilestone)
}

func (s *Server) iterationEvents(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	tier := s.tier
	s.mu.Unlock()
	if tier != Premium {
		forbidden(w)
		return
	}
	s.sprintEvents(w, r, forge.SprintByIteration)
}

func (s *Server) sprintEvents(w http.ResponseWriter, r *http.Request, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, ok := s.issueFor(w, r)
	if !ok {
		return
	}
	var evs []forge.SprintEvent
	for _, e := range s.sprintEventsFor(is) {
		if e.Source == source {
			evs = append(evs, e)
		}
	}
	slices.SortStableFunc(evs, func(a, b forge.SprintEvent) int { return a.At.Compare(b.At) })
	key := "milestone"
	if source == forge.SprintByIteration {
		key = "iteration"
	}
	paged(s, w, r, evs, func(e forge.SprintEvent) any {
		return object{
			"id": s.st.id(), "user": userJSON(s.st.me), "created_at": ts(e.At), "resource_type": "Issue",
			"resource_id": is.ID, key: object{"id": e.ID, "title": e.Title}, "action": e.Action,
		}
	})
}

// derivedEvents is the activity feed implied by the state: issues opened and
// closed, MRs opened and merged, and discussion notes.
func (s *Server) derivedEvents(p *project) []forge.Event {
	var out []forge.Event
	add := func(at time.Time, action, typ string, ref forge.Ref, title string, by *user) {
		out = append(out, forge.Event{
			Action: action, TargetType: typ, Target: ref, Title: title, At: at,
			Author: forge.User{ID: by.ID, Username: by.Username, Name: by.Name},
		})
	}
	for _, is := range s.st.issues {
		if is.Project != p {
			continue
		}
		ref := s.st.ref(p, is.IID, forge.KindIssue)
		add(is.Created, "opened", "Issue", ref, is.Title, is.Author)
		if is.Closed != nil {
			add(*is.Closed, "closed", "Issue", ref, is.Title, s.st.me)
		}
	}
	for _, m := range s.st.mrs {
		if m.Project != p {
			continue
		}
		ref := s.st.ref(p, m.IID, forge.KindMR)
		add(m.Created, "opened", "MergeRequest", ref, m.Title, m.Author)
		if m.Merged != nil {
			add(*m.Merged, "accepted", "MergeRequest", ref, m.Title, m.Author)
		}
		for _, d := range s.st.threads[ref] {
			for _, n := range d.Notes {
				if !n.System {
					add(n.Created, "commented on", "Note", ref, m.Title, n.Author)
				}
			}
		}
	}
	return out
}

func (s *Server) projectEvents(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.st.findProject(r.PathValue("id"))
	if p == nil {
		notFound(w)
		return
	}
	evs := s.derivedEvents(p)
	for _, e := range s.agile().events {
		if e.Target.Project == p.Path || (e.Target.IsZero() && e.TargetType == "") {
			evs = append(evs, e)
		}
	}
	q := r.URL.Query()
	after, _ := time.Parse("2006-01-02", q.Get("after"))
	before, _ := time.Parse("2006-01-02", q.Get("before"))
	var out []forge.Event
	for _, e := range evs {
		day := e.At.UTC().Truncate(24 * time.Hour)
		if (!after.IsZero() && !day.After(after)) || (!before.IsZero() && !day.Before(before)) {
			continue
		}
		out = append(out, e)
	}
	slices.SortStableFunc(out, func(a, b forge.Event) int { return b.At.Compare(a.At) }) // newest first, like GitLab
	paged(s, w, r, out, func(e forge.Event) any {
		o := object{
			"id": s.st.id(), "project_id": p.ID, "action_name": e.Action, "target_type": nilIfEmpty(e.TargetType),
			"target_title": e.Title, "created_at": ts(e.At), "author_id": e.Author.ID, "author_username": e.Author.Username,
			"author": object{"id": e.Author.ID, "username": e.Author.Username, "name": e.Author.Name},
		}
		if !e.Target.IsZero() {
			o["target_iid"] = e.Target.IID
			if e.TargetType == "Note" {
				typ := "Issue"
				if e.Target.Kind == forge.KindMR {
					typ = "MergeRequest"
				}
				o["note"] = object{"noteable_type": typ, "noteable_iid": e.Target.IID} //nolint:misspell // GitLab's field name
			}
		}
		if e.Branch != "" {
			o["push_data"] = object{"ref": e.Branch, "ref_type": "branch", "action": "pushed"}
		}
		return o
	})
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
