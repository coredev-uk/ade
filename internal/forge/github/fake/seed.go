package fake

import (
	"maps"
	"slices"
	"time"
)

const day = 24 * time.Hour

const prTemplate = "## Summary\n\nWhat changes and why.\n\n## Checklist\n\n- [ ] Tests added\n- [ ] Docs updated\n"

// seed builds the fake instance: owner ade-demo with the repos tracker, api
// and web, mirroring scripts/sandbox/github-seed.sh. Timestamps are laid out
// relative to the clock (2026-11-11 by default).
func seed(clock time.Time) *state {
	st := &state{clock: clock, owner: "ade-demo", nextID: 100000}
	st.users = []*user{
		{ID: 1001, Login: viewer, Name: "Demo User", Type: "User"},
		{ID: 1002, Login: "teammate", Name: "Team Mate", Type: "User"},
		{ID: 1003, Login: "reviewer-bot", Name: "Reviewer Bot", Type: "Bot"},
		{ID: 2001, Login: st.owner, Name: "ADE Demo", Type: "Organization"},
	}
	tracker := st.newRepo("tracker", clock)
	api := st.newRepo("api", clock)
	web := st.newRepo("web", clock)

	st.seedTracker(tracker)
	st.seedAPI(api)
	st.seedWeb(web)

	link := func(parent *repo, n int, child *repo, m int) {
		_, p := st.lookup(itemKey{parent.Name, n})
		_, c := st.lookup(itemKey{child.Name, m})
		p.Subs = append(p.Subs, itemKey{child.Name, m})
		c.Parent = &itemKey{parent.Name, n}
	}
	link(tracker, 1, api, 1)
	link(tracker, 2, web, 1)
	link(tracker, 9, web, 3)
	link(tracker, 10, api, 3)

	for _, rp := range st.repos {
		st.seedFeed(rp)
	}
	return st
}

var seedLabels = []struct{ name, color, desc string }{
	{"type:story", "0e8a16", "A user-facing capability"},
	{"type:bug", "d73a4a", "Something is broken"},
	{"app:api", "1f75cb", "Work lands in the api repo"},
	{"app:web", "6699cc", "Work lands in the web repo"},
	{"workflow:in progress", "fbca04", "Someone is working on it"},
	{"workflow:in review", "5319e7", "A pull request is up"},
	{"workflow:blocked", "36454f", "Waiting on something outside the team"},
	{"workflow:done", "0e8a16", "Shipped"},
	{"weight:1", "ededed", "Weight 1"},
	{"weight:2", "ededed", "Weight 2"},
	{"weight:3", "ededed", "Weight 3"},
	{"weight:5", "ededed", "Weight 5"},
	{"weight:8", "ededed", "Weight 8"},
}

// newRepo creates a repo with the shared sprint milestones, labels and a main
// branch holding the PR template.
func (st *state) newRepo(name string, clock time.Time) *repo {
	rp := &repo{
		ID: st.id(), Owner: st.owner, Name: name, Default: "main",
		Commits: map[string]*commit{}, Branches: map[string]string{},
	}
	st.repos = append(st.repos, rp)
	created := clock.Add(-30 * day)
	for i, m := range []struct {
		title, start string
		due          time.Duration
		state        string
	}{
		{"Sprint 41", "2026-10-19", -12 * day, "closed"},
		{"Sprint 42", "2026-11-02", 2 * day, "open"},
		{"Sprint 43", "2026-11-16", 16 * day, "open"},
	} {
		ms := &milestone{
			ID: st.id(), Number: i + 1, Title: m.title, State: m.state,
			Description: "Start: " + m.start + "\n\n" + m.title,
			DueOn:       clock.Add(m.due), Created: created, Updated: created,
		}
		if m.state == "closed" {
			ms.Closed = ms.DueOn
		}
		rp.Milestones = append(rp.Milestones, ms)
	}
	for _, l := range seedLabels {
		rp.Labels = append(rp.Labels, &label{ID: st.id(), Name: l.name, Color: l.color, Description: l.desc})
	}
	main := st.commit(rp, "Initial commit", created, map[string]string{
		"README.md":                        "# " + name + "\n\nFictional sandbox repo for ADE.\n",
		".github/pull_request_template.md": prTemplate,
	})
	rp.Branches[rp.Default] = main.SHA
	return rp
}

// commit stores a commit, deriving its tree from files, and returns
// it; it does not move any branch.
func (st *state) commit(rp *repo, msg string, at time.Time, files map[string]string, parents ...string) *commit {
	c := &commit{
		SHA: gitSHA(append([]string{rp.Name, msg, at.String()}, parents...)...), Tree: treeSHA(files),
		Parents: parents, Message: msg, Created: at, Files: files,
	}
	rp.Commits[c.SHA] = c
	return c
}

type issueSpec struct {
	title, body string
	labels      []string
	milestone   int
	assigned    bool
	created     time.Time
	closed      time.Time // zero while open
	updated     time.Time // zero: a little after created
}

// seedIssue adds an issue, assigned to the viewer when asked.
func (st *state) seedIssue(rp *repo, s issueSpec) *item {
	it := st.addItem(rp, s.title, s.body, viewer, s.created)
	it.Labels = slices.Clone(s.labels)
	it.Milestone = s.milestone
	if s.assigned {
		it.Assignees = []string{viewer}
	}
	it.Updated = s.created.Add(time.Duration(it.Number) * time.Hour)
	if !s.updated.IsZero() {
		it.Updated = s.updated
	}
	if !s.closed.IsZero() {
		it.State, it.StateReason, it.Closed, it.Updated = "closed", "completed", s.closed, s.closed
		st.addEvent(it, "closed", viewer, s.closed)
	}
	return it
}

func (st *state) seedTracker(rp *repo) {
	c := st.clock
	ago := func(d time.Duration) time.Time { return c.Add(-d) }
	story, bug := "type:story", "type:bug"
	specs := []issueSpec{
		{
			title: "Rate-limit the export endpoint", labels: []string{story, "app:api", "workflow:in review", "weight:5"}, milestone: 2, assigned: true,
			body: "Tenants can trigger unbounded exports and starve the worker pool.\n\n## Acceptance\n" +
				"- [x] Agree limits with support (50 req/min per tenant)\n- [ ] Token-bucket limiter in the export handler\n" +
				"- [ ] Return 429 with Retry-After\n- [ ] Document the limits\n",
			created: ago(10 * day), updated: ago(2 * day),
		},
		{
			title: "Paginate the audit log view", labels: []string{bug, "app:web", "workflow:in review", "weight:3"}, milestone: 2, assigned: true,
			body:    "The audit log page renders every row and freezes for large tenants.\n\n## Acceptance\n- [ ] Cursor pagination in the UI\n",
			created: ago(10 * day),
		},
		{
			title: "Retry failed webhook deliveries", labels: []string{story, "app:api", "weight:5"}, milestone: 2, assigned: true,
			body: "Webhook deliveries that fail are dropped.", created: ago(10 * day),
		},
		{
			title: "Remove the legacy CSV export path", labels: []string{story, "app:api", "weight:2"}, milestone: 2, assigned: true,
			body: "Nothing reads the legacy path any more.", created: ago(10 * day),
		},
		{
			title: "Export timestamps ignore tenant timezone", labels: []string{bug, "app:api", "workflow:blocked", "weight:3"}, milestone: 2, assigned: true,
			body: "Exports always use UTC.", created: ago(10 * day),
		},
		{
			title: "Billing export times out for large tenants", labels: []string{bug, "app:api"}, milestone: 2,
			body: "Exports over 200k rows hit the 30s gateway timeout.", created: ago(10 * day),
		},
		{
			title: "Dark mode for the export dashboard", labels: []string{story, "app:web", "weight:3"}, milestone: 3,
			body: "Follow the existing design tokens.", created: ago(10 * day),
		},
		{
			title: "Scheduled exports", labels: []string{story, "app:api", "app:web", "weight:8"}, milestone: 3, assigned: true,
			body: "Let tenants schedule recurring exports.", created: ago(10 * day),
		},
		{
			title: "Fix truncated column headers in export preview", labels: []string{bug, "app:web", "workflow:done", "weight:1"}, milestone: 1, assigned: true,
			body: "Long headers are cut at eight characters.", created: ago(20 * day), closed: ago(14 * day),
		},
		{
			title: "Upgrade export worker dependencies", labels: []string{story, "app:api", "workflow:done", "weight:2"}, milestone: 2, assigned: true,
			body: "Routine dependency bump.", created: ago(10 * day), closed: ago(5 * day),
		},
		{
			title: "Exports page sometimes shows stale totals", milestone: 2, assigned: true,
			body: "Seen twice this week; no repro yet.", created: ago(10 * day),
		},
	}
	for _, s := range specs {
		st.seedIssue(rp, s)
	}
	// The history the label/sprint event feeds read.
	one := rp.item(1)
	st.addEvent(one, "milestoned", viewer, ago(9*day)).Milestone = "Sprint 42"
	st.addEvent(one, "labeled", viewer, ago(8*day)).Label = "workflow:in progress"
	st.addEvent(one, "unlabeled", viewer, ago(2*day)).Label = "workflow:in progress"
	st.addEvent(one, "labeled", viewer, ago(2*day).Add(time.Second)).Label = "workflow:in review"
}

// seedFeed derives the activity feed from the items and branches already
// seeded, so it always agrees with them.
func (st *state) seedFeed(rp *repo) {
	for _, it := range rp.Items {
		st.feedItem(rp, it, "opened", it.Author, it.Created)
		for _, cm := range it.Comments {
			st.feedComment(rp, it, cm)
		}
		if it.State == "closed" {
			st.feedItem(rp, it, "closed", viewer, it.Closed)
		}
		if p := it.PR; p != nil {
			st.feedPush(rp, p.Head, rp.Commits[p.HeadSHA], it.Author)
		}
	}
}

func cloneFiles(m map[string]string) map[string]string { return maps.Clone(m) }
