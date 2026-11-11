package fake

import (
	"fmt"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// GroupPath is the namespace the fake serves; it mirrors the sandbox group.
const GroupPath = "ade-demo-env"

const (
	groupID          = 500
	iterationEpoch   = "2026-10-19" // a Monday: Sprint 41 starts here
	iterationDays    = 14
	deliveryCadence  = "Delivery"
	maintenanceCycle = "Maintenance"
)

// seed builds the data of scripts/sandbox/gitlab-seed.sh and, for Premium,
// the data a licensed group would add on top: iterations, weights, an epic and
// approval requirements.
func seed(clock time.Time, tier Tier) *state {
	s := &state{
		clock:             clock,
		nextID:            1000,
		group:             GroupPath,
		groupID:           groupID,
		threads:           map[forge.Ref][]*discussion{},
		approvalsRequired: 2,
	}
	s.me = &user{ID: 1, Username: "sandbox-dev", Name: "Sandbox Dev"}
	rae := &user{ID: 2, Username: "reviewer-rae", Name: "Rae Reviewer"}
	s.users = []*user{s.me, rae}

	tracker := &project{ID: 1001, Path: GroupPath + "/tracker"}
	api := &project{ID: 1002, Path: GroupPath + "/api"}
	web := &project{ID: 1003, Path: GroupPath + "/web"}
	s.projects = []*project{tracker, api, web}

	s.seedMilestones()
	if tier == Premium {
		s.seedIterations()
	}
	s.seedIssues(tier, tracker, api, web)
	s.seedMRs(tier, rae, api, web)
	return s
}

func (s *state) seedMilestones() {
	for i, m := range []struct{ title, state, start, due string }{
		{"Sprint 41", "closed", "2026-10-19", "2026-10-30"},
		{"Sprint 42", "active", "2026-11-02", "2026-11-13"},
		{"Sprint 43", "active", "2026-11-16", "2026-11-27"},
	} {
		s.milestones = append(s.milestones, &milestone{
			ID: s.id(), IID: i + 1, Title: m.title, State: m.state, Start: m.start, Due: m.due,
		})
	}
}

// seedIterations lays out the 'Delivery' cadence (automatic, so titles are
// null) around the injected clock: previous, current and next, each two weeks
// Monday to Sunday and contiguous. A second cadence has one titled iteration,
// current whatever the clock, so cadence filtering has something to exclude.
func (s *state) seedIterations() {
	epoch, _ := time.Parse("2006-01-02", iterationEpoch)
	days := int(s.clock.Sub(epoch).Hours() / 24)
	idx := days / iterationDays
	if days < 0 && days%iterationDays != 0 {
		idx-- // floor, not truncate, for clocks before the epoch
	}
	start := epoch.AddDate(0, 0, idx*iterationDays)
	today := s.clock.Truncate(24 * time.Hour)
	for i := -1; i <= 1; i++ {
		from := start.AddDate(0, 0, i*iterationDays)
		s.iterations = append(s.iterations, &iteration{
			ID: s.id(), IID: len(s.iterations) + 1, Seq: len(s.iterations) + 1, Cadence: deliveryCadence,
			Start: from, Due: from.AddDate(0, 0, iterationDays-1),
		})
	}
	s.iterations = append(s.iterations, &iteration{
		ID: s.id(), IID: len(s.iterations) + 1, Seq: 1, Cadence: maintenanceCycle, Title: "Patch window",
		Start: today.AddDate(0, 0, -1), Due: today.AddDate(0, 0, 2),
	})
}

type issueSpec struct {
	title, body, labels string
	sprint              int // index into s.milestones
	assigned            bool
	weight              *int
	closed              bool
}

func (s *state) seedIssues(tier Tier, tracker, api, web *project) {
	specs := []issueSpec{
		{"Rate-limit the export endpoint", "Tenants can trigger unbounded CSV exports and starve the worker pool.\n\n## Acceptance\n- [x] Agree limits with support (50 req/min per tenant)\n- [ ] Token-bucket limiter in api\n- [ ] 429 response with Retry-After\n- [ ] Dashboard shows remaining quota", "type::story,app::api,workflow::in review", 1, true, new(3), false},
		{"Paginate the audit log view", "The audit log page renders every row and freezes for large tenants.\n\n## Acceptance\n- [ ] Cursor pagination in the UI\n- [ ] Keep current filters when paging", "type::bug,app::web,workflow::in progress", 1, true, new(5), false},
		{"Retry failed webhook deliveries", "Webhook deliveries that fail are dropped.\n\n## Acceptance\n- [ ] Exponential backoff, max 6 attempts\n- [ ] Dead-letter list visible to admins", "type::story,app::api", 1, true, new(2), false},
		{"Remove the legacy CSV export path", "The v1 CSV path is unused since the new exporter shipped. Remove it and its feature flag.", "type::story,app::api", 1, true, new(1), false},
		{"Export timestamps ignore tenant timezone", "Exports always use UTC. Tenants with a configured timezone expect local time with offset.", "type::bug,app::api,workflow::blocked", 1, true, new(3), false},
		{"Billing export times out for large tenants", "Exports over 200k rows hit the 30s gateway timeout.", "type::bug,app::api", 1, false, nil, false},
		{"Dark mode for the export dashboard", "Follow the design tokens already used by the settings pages.", "type::story,app::web", 2, false, new(2), false},
		{"Scheduled exports", "Let tenants schedule a daily or weekly export delivered by email. Needs an api scheduler and a web settings form.", "type::story,app::api,app::web", 2, true, new(8), false},
		{"Fix truncated column headers in export preview", "Long column names are cut off without a tooltip.", "type::bug,app::web,workflow::done", 0, true, new(1), true},
		{"Upgrade export worker dependencies", "Routine upgrade of the worker's HTTP and CSV libraries.", "type::story,app::api,workflow::done", 1, true, new(2), true},
		{"Exports page sometimes shows stale totals", "Reported by support: totals lag behind the table after a filter change. No labels yet (needs triage).", "", 1, true, new(3), false},
	}
	for i, sp := range specs {
		is := s.newIssue(tier, tracker, sp, i)
		if tier == Premium && sp.title == "Scheduled exports" {
			is.Epic = "Self-serve exports"
		}
	}

	parent := func(n int) string {
		return fmt.Sprintf("Parent: %s/tracker#%d\n\nImplementation issue for the tracker item above.", GroupPath, n)
	}
	children := []struct {
		project *project
		parent  int // tracker iid
		spec    issueSpec
	}{
		{api, 1, issueSpec{"Rate-limit the export endpoint", parent(1), "type::story", 1, true, new(3), false}},
		{web, 2, issueSpec{"Paginate the audit log view", parent(2), "type::bug", 1, true, new(5), false}},
		{web, 9, issueSpec{"Fix truncated column headers in export preview", parent(9), "type::bug", 1, true, new(1), true}},
		{api, 10, issueSpec{"Upgrade export worker dependencies", parent(10), "type::story", 1, true, new(2), true}},
	}
	for i, ch := range children {
		child := s.newIssue(tier, ch.project, ch.spec, len(specs)+i)
		s.links = append(s.links, &link{
			ID: s.id(), A: s.findIssue(tracker, ch.parent), B: child, Type: "relates_to", Created: s.clock.Add(-96 * time.Hour),
		})
	}
}

func (s *state) newIssue(tier Tier, p *project, sp issueSpec, order int) *issue {
	iid := 1
	for _, o := range s.issues {
		if o.Project == p {
			iid++
		}
	}
	// Newer issues were touched more recently, so updated_after has a spread.
	updated := s.clock.Add(-time.Duration(len(s.issues)+1) * 24 * time.Hour)
	is := &issue{
		ID: s.id(), IID: iid, Project: p, Title: sp.title, Desc: sp.body, State: "opened",
		Author: s.me, Milestone: s.milestones[sp.sprint],
		Created: s.clock.Add(-time.Duration(30+order) * 24 * time.Hour), Updated: updated,
	}
	if sp.labels != "" {
		is.Labels = strings.Split(sp.labels, ",")
	}
	if sp.assigned {
		is.Assignees = []*user{s.me}
	}
	if tier == Premium {
		is.Weight = sp.weight
		is.Iteration = s.iterationFor(sp.sprint)
	}
	if sp.closed {
		is.State = "closed"
		closed := updated
		is.Closed = &closed
	}
	s.issues = append(s.issues, is)
	return is
}

// iterationFor maps sprint milestones onto the Delivery iterations: sprint 41
// to the previous one, 42 to the current, 43 to the next.
func (s *state) iterationFor(sprint int) *iteration {
	var delivery []*iteration
	for _, it := range s.iterations {
		if it.Cadence == deliveryCadence {
			delivery = append(delivery, it)
		}
	}
	if sprint < len(delivery) {
		return delivery[sprint]
	}
	return nil
}

func (s *state) seedMRs(tier Tier, rae *user, api, web *project) {
	tracker := s.projects[0]
	clock := s.clock

	rate := &mr{
		ID: s.id(), IID: 1, Project: api, Title: "Rate-limit the export endpoint",
		Desc:   fmt.Sprintf("Closes #1\n\nPart of %s/tracker#1.\n\nAdds a per-tenant token bucket in front of the export handler.", GroupPath),
		State:  "opened",
		Source: "1-rate-limit-the-export-endpoint", Target: "main", SHA: "cba12b9f93fb6e59bd5f3e1adc4097f24360460c",
		Author: s.me, Assignees: []*user{s.me}, Reviewers: []*user{rae},
		Pipeline: &pipeline{ID: 9001, Status: "success", Updated: clock.Add(-2 * time.Hour)},
		Closes:   []*issue{s.findIssue(api, 1)}, Mentions: []*issue{s.findIssue(tracker, 1)},
		Created: clock.Add(-3 * 24 * time.Hour), Updated: clock.Add(-2 * time.Hour),
	}
	if tier == Premium {
		rate.Approved = []*user{rae}
	}
	audit := &mr{
		ID: s.id(), IID: 1, Project: web, Title: "Paginate the audit log view",
		Desc:  fmt.Sprintf("Closes #1\n\nPart of %s/tracker#2.", GroupPath),
		State: "opened", Draft: true,
		Source: "1-paginate-the-audit-log-view", Target: "main", SHA: "5d2c7a1e0b9f4c3d8a6e1f2b7c4d9e0a3b5c6d7e",
		Author: s.me, Assignees: []*user{s.me},
		Pipeline: &pipeline{ID: 9002, Status: "failed", Updated: clock.Add(-26 * time.Hour)},
		Closes:   []*issue{s.findIssue(web, 1)}, Mentions: []*issue{s.findIssue(tracker, 2)},
		Created: clock.Add(-2 * 24 * time.Hour), Updated: clock.Add(-26 * time.Hour),
	}
	merged := clock.Add(-3 * 24 * time.Hour)
	font := &mr{
		ID: s.id(), IID: 2, Project: web, Title: "Fix truncated column headers in export preview",
		Desc:  fmt.Sprintf("Closes #2\n\nPart of %s/tracker#9.", GroupPath),
		State: "merged", Source: "2-fix-truncated-column-headers-in-export-preview", Target: "main",
		SHA: "9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a291807", Author: s.me, Assignees: []*user{s.me},
		Closes: []*issue{s.findIssue(web, 2)}, Mentions: []*issue{s.findIssue(tracker, 9)},
		Created: clock.Add(-6 * 24 * time.Hour), Updated: merged, Merged: &merged,
	}
	s.mrs = []*mr{rate, audit, font}
	s.seedThreads(rate, rae)
	s.seedReview(api)
}

// seedThreads mirrors the sandbox review: one resolved and two open threads on
// api!1, plus a system note, which GitLab lists as an individual discussion.
func (s *state) seedThreads(m *mr, rae *user) {
	t := s.clock.Add(-2 * 24 * time.Hour)
	at := func(h int) time.Time { return t.Add(time.Duration(h) * time.Hour) }
	thread := func(first *note, replies ...*note) *discussion {
		return &discussion{ID: fmt.Sprintf("%040x", s.id()), Notes: append([]*note{first}, replies...)}
	}
	n := func(a *user, body string, h int, resolved bool, pos *position) *note {
		return &note{ID: s.id(), Author: a, Body: body, Created: at(h), Resolvable: true, Resolved: resolved, Position: pos}
	}
	ref := s.ref(m.Project, m.IID, forge.KindMR)
	system := &discussion{ID: fmt.Sprintf("%040x", s.id()), Individual: true, Notes: []*note{
		{ID: s.id(), Author: s.me, Body: "assigned to @sandbox-dev", System: true, Created: at(0)},
	}}
	s.threads[ref] = []*discussion{
		system,
		thread(n(rae, "Is a burst of 50 too high for small tenants? Support suggested 20.", 1, false, nil),
			&note{ID: s.id(), Author: s.me, Body: "Keeping 50 for now; it matches the support agreement. Open to revisit.", Created: at(2), Resolvable: true}),
		thread(n(rae, "Please add a test for the 429 path, including the Retry-After header.", 3, false, nil)),
		thread(n(rae, "Nit: rename Bucket.Rate to PerMinute for clarity.", 4, true, &position{Path: "limit/bucket.go", NewLine: 6})),
	}
}
