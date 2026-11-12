package forgesync

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// memForge is an in-memory forge.Forge. Tests mutate it between syncs with
// update; every method takes the lock so parallel fetches are safe.
type memForge struct {
	mu sync.Mutex

	me          forge.User
	issues      []forge.Issue
	links       map[forge.Ref][]forge.Link
	mrs         []forge.MR             // list-level MRs
	mrDetail    map[forge.Ref]forge.MR // detail by ref
	milestones  []forge.Milestone
	iterations  []forge.Iteration
	iterErr     error
	discussions map[forge.Ref][]forge.Discussion
	failProject string // ListIssues on this project fails

	meCalls, capsCalls int
	discCalls          map[forge.Ref]int
}

var _ forge.Forge = (*memForge)(nil)

func newMemForge() *memForge {
	return &memForge{
		me:          forge.User{ID: 1, Username: "me"},
		links:       map[forge.Ref][]forge.Link{},
		mrDetail:    map[forge.Ref]forge.MR{},
		discussions: map[forge.Ref][]forge.Discussion{},
		discCalls:   map[forge.Ref]int{},
	}
}

func (f *memForge) update(fn func(f *memForge)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *memForge) Kind() string { return "gitlab" }

func (f *memForge) Me(context.Context) (forge.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meCalls++
	return f.me, nil
}

func (f *memForge) Capabilities(context.Context, string, string) (forge.Caps, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capsCalls++
	return forge.Caps{Weight: true}, nil
}

func (f *memForge) ListIssues(_ context.Context, q forge.IssueQuery) ([]forge.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if q.Project == f.failProject {
		return nil, fmt.Errorf("boom: %s unreachable", q.Project)
	}
	var out []forge.Issue
	for _, is := range f.issues {
		if is.Ref.Project != q.Project || !stateMatches(q.State, is.State) {
			continue
		}
		if q.Assignee != "" && !hasUser(is.Assignees, q.Assignee) {
			continue
		}
		if q.Milestone != "" && (is.Milestone == nil || is.Milestone.Title != q.Milestone) {
			continue
		}
		if q.IterationID != 0 && (is.Iteration == nil || is.Iteration.ID != q.IterationID) {
			continue
		}
		if !q.UpdatedAfter.IsZero() && is.UpdatedAt.Before(q.UpdatedAfter) {
			continue
		}
		out = append(out, is)
	}
	return out, nil
}

func stateMatches(want, have string) bool {
	return want == "all" || (want == "" && have == "opened") || want == have
}

func hasUser(us []forge.User, name string) bool {
	return slices.ContainsFunc(us, func(u forge.User) bool { return u.Username == name })
}

func (f *memForge) Issue(_ context.Context, ref forge.Ref) (forge.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, is := range f.issues {
		if is.Ref == ref {
			return is, nil
		}
	}
	return forge.Issue{}, forge.ErrNotFound
}

func (f *memForge) Links(_ context.Context, ref forge.Ref) ([]forge.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.links[ref], nil
}

func (f *memForge) ListMRs(_ context.Context, q forge.MRQuery) ([]forge.MR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []forge.MR
	for _, mr := range f.mrs {
		if mr.Ref.Project != q.Project || !stateMatches(q.State, mr.State) {
			continue
		}
		if q.Reviewer != "" && !hasUser(mr.Reviewers, q.Reviewer) {
			continue
		}
		out = append(out, mr)
	}
	return out, nil
}

func (f *memForge) MR(_ context.Context, ref forge.Ref) (forge.MR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if mr, ok := f.mrDetail[ref]; ok {
		return mr, nil
	}
	return forge.MR{}, forge.ErrNotFound
}

func (f *memForge) MRsForIssue(context.Context, forge.Ref) ([]forge.MR, error) { return nil, nil }

func (f *memForge) Discussions(_ context.Context, ref forge.Ref) ([]forge.Discussion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discCalls[ref]++
	return f.discussions[ref], nil
}

func (f *memForge) Milestones(context.Context, string) ([]forge.Milestone, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.milestones, nil
}

func (f *memForge) Iterations(context.Context, string, string) ([]forge.Iteration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.iterErr != nil {
		return nil, f.iterErr
	}
	return f.iterations, nil
}

// --- builders -----------------------------------------------------------------

var testNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func ref(s string) forge.Ref {
	r, err := forge.ParseRef(s)
	if err != nil {
		panic(err)
	}
	return r
}

func user(name string) forge.User { return forge.User{Username: name} }

func milestone(id int64, title, start, due string) forge.Milestone {
	s, _ := forge.ParseDate(start)
	d, _ := forge.ParseDate(due)
	return forge.Milestone{ID: id, Title: title, State: "active", Start: s, Due: d}
}

func issue(r string, title string, ms *forge.Milestone, assignees ...string) forge.Issue {
	is := forge.Issue{
		Ref: ref(r), Title: title, State: "opened", Milestone: ms,
		Labels:    []string{"type::story", "app::api"},
		UpdatedAt: testNow.Add(-24 * time.Hour), WebURL: "https://forge.test/" + strings.Replace(r, "#", "/-/issues/", 1),
	}
	for _, a := range assignees {
		is.Assignees = append(is.Assignees, user(a))
	}
	return is
}

func note(body string) forge.Discussion {
	return forge.Discussion{ID: "d1", Notes: []forge.Note{{ID: 1, Author: user("rae"), Body: body, CreatedAt: testNow}}}
}
