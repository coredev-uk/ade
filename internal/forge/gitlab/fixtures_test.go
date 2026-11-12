package gitlab_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
)

// route answers one request from a fixture. file is relative to testdata/;
// list wraps a single-object fixture in an array.
type route struct {
	file string
	list bool
}

// fixtureClient serves testdata fixtures keyed by "METHOD path?query" (the
// query only when the route needs one) and returns a client pointed at them.
// Anything unrouted is an empty 200 list, which keeps probes simple.
func fixtureClient(t *testing.T, routes map[string]route) *gitlab.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt, ok := routes[r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.Query().Get("state")]
		if !ok {
			rt, ok = routes[r.Method+" "+r.URL.EscapedPath()]
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case !ok:
			_, _ = w.Write([]byte("[]"))
		case rt.file == "403":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"403 Forbidden"}`))
		default:
			body, err := os.ReadFile(filepath.Join("testdata", rt.file))
			if err != nil {
				t.Error(err)
			}
			if rt.list {
				body = append(append([]byte("["), body...), ']')
			}
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(srv.Close)
	return gitlab.New(srv.URL, "token", gitlab.WithClock(func() time.Time { return clock }))
}

const proj = "/api/v4/projects/my-group%2Fmy-project"

func TestDecodeIssueFixtures(t *testing.T) {
	c := fixtureClient(t, map[string]route{
		"GET " + proj + "/issues/6":                       {file: "free/issue.json"},
		"GET " + proj + "/issues/7":                       {file: "premium/issue_unweighted.json"},
		"GET /api/v4/projects/premium%2Fproject/issues/6": {file: "premium/issue.json"},
	})
	ctx := context.Background()
	ref := issueRef

	t.Run("free", func(t *testing.T) {
		is, err := c.Issue(ctx, ref("my-group/my-project", 6))
		if err != nil {
			t.Fatal(err)
		}
		if is.Ref != ref("my-group/my-project", 6) || is.State != "opened" || is.ID != 76 {
			t.Errorf("identity = %+v", is)
		}
		if is.Weight != nil || is.Iteration != nil || is.Epic != "" {
			t.Errorf("Free issue carries premium data: %+v", is)
		}
		if is.Tasks != (forge.TaskCount{Done: 10, Total: 15}) {
			t.Errorf("tasks = %+v", is.Tasks)
		}
		m := is.Milestone
		if m == nil || m.ID != 17 || m.Title != "v4.0" || m.State != "closed" || !m.Due.IsZero() || !m.Start.IsZero() {
			t.Errorf("milestone (null dates) = %+v", m)
		}
		if len(is.Assignees) != 1 || is.Assignees[0].Username != "root" || is.Author.Username != "eileen.lowe" {
			t.Errorf("people = %+v / %+v", is.Assignees, is.Author)
		}
		if len(is.Labels) != 2 || is.ClosedAt != nil || is.Description == "" || is.WebURL == "" {
			t.Errorf("fields = %+v", is)
		}
		if !is.CreatedAt.Equal(time.Date(2016, 1, 4, 15, 31, 51, 81000000, time.UTC)) {
			t.Errorf("created = %v", is.CreatedAt)
		}
	})

	t.Run("premium fields", func(t *testing.T) {
		is, err := c.Issue(ctx, ref("premium/project", 6))
		if err != nil {
			t.Fatal(err)
		}
		if is.Ref != ref("my-group/my-project", 6) {
			t.Errorf("ref must come from references.full, got %v", is.Ref)
		}
		if is.Weight == nil || *is.Weight != 3 || is.Epic != "My epic epic" {
			t.Errorf("weight/epic = %v / %q", is.Weight, is.Epic)
		}
		it := is.Iteration
		if it == nil || it.ID != 90 || it.IID != 4 || it.Title != "" || it.Start.String() != "2022-03-08" || it.Due.String() != "2022-03-14" {
			t.Fatalf("iteration = %+v", it)
		}
		if it.State != "closed" { // derived from dates: the integer state is never decoded
			t.Errorf("iteration state = %q, want closed", it.State)
		}
		if is.ClosedAt == nil || is.Milestone != nil || len(is.Assignees) != 0 {
			t.Errorf("fields = %+v", is)
		}
	})

	t.Run("premium issue left unweighted", func(t *testing.T) {
		is, err := c.Issue(ctx, ref("my-group/my-project", 7))
		if err != nil {
			t.Fatal(err)
		}
		if is.Weight != nil || is.Iteration != nil || is.Epic != "" || is.Description != "" {
			t.Errorf("nulls must decode as absent: %+v", is)
		}
	})
}

func TestDecodeLinkFixtures(t *testing.T) {
	for _, tc := range []struct{ dir, typ string }{{"free", "relates_to"}, {"premium", "blocks"}} {
		t.Run(tc.dir, func(t *testing.T) {
			c := fixtureClient(t, map[string]route{
				"GET " + proj + "/issues/1/links": {file: tc.dir + "/issue_links.json"},
			})
			got, err := c.Links(context.Background(), issueRef("my-group/my-project", 1))
			if err != nil {
				t.Fatal(err)
			}
			// The docs example has no references object: the queried project
			// stands in for the unknown one.
			want := forge.Link{Ref: issueRef("my-group/my-project", 14), Type: tc.typ}
			if len(got) != 1 || got[0] != want {
				t.Errorf("links = %+v, want %+v", got, want)
			}
		})
	}
}

func TestDecodeMilestoneFixture(t *testing.T) {
	c := fixtureClient(t, map[string]route{"GET /api/v4/groups/gitlab-org/milestones": {file: "free/group_milestones.json"}})
	ms, err := c.Milestones(context.Background(), "gitlab-org")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("milestones = %+v", ms)
	}
	// Dated milestones sort by start; an undated one sorts first.
	someday, ten := ms[0], ms[1]
	if someday.Title != "Someday" || someday.State != "closed" || !someday.Start.IsZero() || !someday.Due.IsZero() {
		t.Errorf("undated = %+v", someday)
	}
	if ten.ID != 12 || ten.Title != "10.0" || ten.State != "active" || ten.Start.String() != "2013-11-10" || ten.Due.String() != "2013-11-29" {
		t.Errorf("dated = %+v", ten)
	}
}

func TestDecodeMRFixtures(t *testing.T) {
	mr133 := "/api/v4/projects/marcel.amirault%2Ftest-project/merge_requests/133"
	c := fixtureClient(t, map[string]route{
		"GET " + mr133:                  {file: "free/merge_request.json"},
		"GET " + mr133 + "/approvals":   {file: "free/merge_request_approvals.json"},
		"GET " + mr133 + "/discussions": {file: "free/discussions.json"},

		"GET " + proj + "/merge_requests":                  {file: "free/merge_request_pre19.json", list: true},
		"GET " + proj + "/merge_requests/3":                {file: "free/merge_request_pre19.json"},
		"GET " + proj + "/merge_requests/3/approvals":      {file: "premium/merge_request_approvals.json"},
		"GET " + proj + "/merge_requests/3/discussions":    {file: "free/discussions.json"},
		"GET " + proj + "/issues/1/related_merge_requests": {file: "free/merge_request.json", list: true},
	})
	ctx := context.Background()

	t.Run("single MR with Free approvals", func(t *testing.T) {
		m, err := c.MR(ctx, mrRef("marcel.amirault/test-project", 133))
		if err != nil {
			t.Fatal(err)
		}
		if m.Ref != mrRef("marcel.amirault/test-project", 133) || m.State != "opened" || m.Draft || m.ID != 155016530 {
			t.Errorf("identity = %+v", m)
		}
		if m.Pipeline == nil || m.Pipeline.ID != 538317940 || m.Pipeline.Status != "success" {
			t.Errorf("pipeline = %+v", m.Pipeline)
		}
		if m.DetailedStatus != "mergeable" || m.HasConflicts || m.HeadSHA == "" || len(m.Reviewers) != 1 {
			t.Errorf("status = %+v", m)
		}
		if m.Approvals.Required != -1 || len(m.Approvals.ApprovedBy) != 1 || m.Approvals.ApprovedBy[0].Username != "root" {
			t.Errorf("CE approvals = %+v, want unknown requirement and root approved", m.Approvals)
		}
		if m.Unresolved != 1 || m.Resolved != 1 {
			t.Errorf("threads = %d/%d, want 1 open 1 resolved (system note ignored)", m.Unresolved, m.Resolved)
		}
	})

	t.Run("older instance spells draft work_in_progress", func(t *testing.T) {
		got, err := c.ListMRs(ctx, forge.MRQuery{Project: "my-group/my-project"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !got[0].Draft || got[0].Pipeline == nil || got[0].Pipeline.Status != "running" {
			t.Fatalf("mrs = %+v", got)
		}
		if got[0].Approvals.Required != -1 || got[0].Description != "" {
			t.Errorf("list entry = %+v", got[0])
		}
	})

	t.Run("Premium approvals", func(t *testing.T) {
		m, err := c.MR(ctx, mrRef("my-group/my-project", 3))
		if err != nil {
			t.Fatal(err)
		}
		if m.Approvals.Required != 2 || len(m.Approvals.ApprovedBy) != 1 {
			t.Errorf("approvals = %+v", m.Approvals)
		}
	})

	t.Run("related MRs", func(t *testing.T) {
		got, err := c.MRsForIssue(context.Background(), issueRef("my-group/my-project", 1))
		if err != nil || len(got) != 1 || got[0].Ref != mrRef("marcel.amirault/test-project", 133) {
			t.Errorf("mrs = %+v, %v", got, err)
		}
	})
}

func TestDecodeDiscussionFixture(t *testing.T) {
	c := fixtureClient(t, map[string]route{
		"GET " + proj + "/merge_requests/3/discussions": {file: "free/discussions.json"},
	})
	ds, err := c.Discussions(context.Background(), mrRef("my-group/my-project", 3))
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 {
		t.Fatalf("discussions = %d", len(ds))
	}
	thread, diff, system := ds[0], ds[1], ds[2]
	if thread.ID != "6a9c1750b37d513a43987b574953fceb50b03ce7" || len(thread.Notes) != 2 || !thread.Resolvable || thread.Resolved {
		t.Errorf("thread = %+v", thread)
	}
	if thread.Notes[1].Body != "a reply" || thread.Notes[0].Author.Username != "root" {
		t.Errorf("notes = %+v", thread.Notes)
	}
	if !diff.Resolved || diff.Position == nil || *diff.Position != (forge.Position{Path: "package.json", OldPath: "package.json", NewLine: 27, OldLine: 27}) {
		t.Errorf("diff thread = %+v pos %+v", diff, diff.Position)
	}
	if system.Resolvable || system.Resolved || !system.Notes[0].System {
		t.Errorf("system = %+v", system)
	}
}

func TestDecodeUserFixture(t *testing.T) {
	c := fixtureClient(t, map[string]route{"GET /api/v4/user": {file: "free/user.json"}})
	me, err := c.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if me != (forge.User{ID: 1, Username: "john_smith", Name: "John Smith"}) {
		t.Errorf("me = %+v", me)
	}
}

// The REST iteration state is an undocumented integer (the fixtures carry 2).
// The driver must report the state of the filter the iteration was listed
// under, whatever integer it carries.
func TestIterationStateComesFromTheFilter(t *testing.T) {
	const path = "/api/v4/groups/my-group/iterations"
	c := fixtureClient(t, map[string]route{"GET " + path + "?closed": {file: "premium/group_iterations.json"}})
	its, err := c.Iterations(context.Background(), "my-group", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(its) != 2 {
		t.Fatalf("iterations = %+v", its)
	}
	for _, it := range its {
		if it.State != "closed" {
			t.Errorf("%+v: state not taken from the closed filter", it)
		}
	}
	titled, generated := its[0], its[1]
	if titled.ID != 53 || titled.Title != "Iteration II" || titled.Start.String() != "2020-02-14" || titled.Due.String() != "2020-02-29" {
		t.Errorf("titled = %+v", titled)
	}
	if generated.ID != 90 || generated.Title != "" || generated.IID != 4 {
		t.Errorf("cadence-generated = %+v", generated)
	}
}

// With no issue or merge request to sample, weight and approval rules follow
// iterations (Premium features licensed together); GraphQL answers come from
// the documented shapes.
func TestCapabilitiesFromFixtures(t *testing.T) {
	const iterations = "GET /api/v4/groups/my-group/iterations"
	const namespace = "GET /api/v4/namespaces/my-group"
	tests := []struct {
		name   string
		routes map[string]route
		want   forge.Caps
	}{
		{
			"free",
			map[string]route{namespace: {file: "namespace_group.json"}, iterations: {file: "403"}, "POST /api/graphql": {file: "free/graphql_capabilities.json"}},
			forge.Caps{ProbedAt: clock},
		},
		{
			"premium",
			map[string]route{namespace: {file: "namespace_group.json"}, iterations: {file: "premium/group_iterations.json"}, "POST /api/graphql": {file: "premium/graphql_capabilities.json"}},
			forge.Caps{Iterations: true, Cadences: true, Weight: true, ApprovalRules: true, BlocksLinks: true, Epics: true, ProbedAt: clock},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fixtureClient(t, tc.routes).Capabilities(context.Background(), "my-group", "")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("caps = %+v\nwant   %+v", got, tc.want)
			}
		})
	}
}
