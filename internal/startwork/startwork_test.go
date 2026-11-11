package startwork_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/startwork"
)

var tiers = []fake.Tier{fake.Free, fake.Premium}

func draftApi() startwork.Choices {
	return startwork.Choices{Repos: []string{"api"}, Mode: startwork.ModeDraft, Child: startwork.ChildAuto}
}

func TestStartDraftCreatesLinkedChildMRAndWorktree(t *testing.T) {
	for _, tier := range tiers {
		t.Run(tier.String(), func(t *testing.T) {
			h := newHarness(t, tier)
			ctx := context.Background()
			_, res, err := h.start(3, draftApi())
			if err != nil {
				t.Fatalf("start: %v\n%+v", err, res.Steps)
			}
			api := target(t, res, "api")
			if api.Child == nil || api.MR == nil || api.Worktree == "" {
				t.Fatalf("target = %+v", api)
			}

			// The child: copies type label, assignee and milestone, names its parent.
			child, err := h.forge.Issue(ctx, *api.Child)
			if err != nil {
				t.Fatal(err)
			}
			if child.Ref.Project != apiProj || !strings.HasPrefix(child.Description, "Parent: "+tracker+"#3") {
				t.Errorf("child = %+v", child)
			}
			if !slices.Equal(child.Labels, []string{"type::story"}) {
				t.Errorf("child labels = %v, want only the type label", child.Labels)
			}
			if len(child.Assignees) != 1 || child.Assignees[0].Username != "sandbox-dev" {
				t.Errorf("child assignees = %v", child.Assignees)
			}
			if child.Milestone == nil || child.Milestone.Title != "Sprint 42" {
				t.Errorf("child milestone = %+v", child.Milestone)
			}
			if tier == fake.Premium {
				if child.Iteration == nil || child.Weight == nil || *child.Weight != 2 {
					t.Errorf("premium child iteration/weight = %+v / %v, want copied", child.Iteration, child.Weight)
				}
			} else if child.Iteration != nil || child.Weight != nil {
				t.Errorf("free child has iteration/weight: %+v %v", child.Iteration, child.Weight)
			}

			// Link in both directions.
			links, _ := h.forge.Links(ctx, issueRef(tracker, 3))
			if !slices.ContainsFunc(links, func(l forge.Link) bool { return l.Ref == *api.Child && l.Type == "relates_to" }) {
				t.Errorf("tracker links = %+v", links)
			}

			// Workflow label moved on the tracker.
			labels, _ := h.srv.IssueLabels(issueRef(tracker, 3))
			if !slices.Contains(labels, "workflow::in progress") {
				t.Errorf("tracker labels = %v, want workflow::in progress", labels)
			}

			// Branch per convention; draft MR from the template with Closes + parent.
			wantBranch := "3-retry-failed-webhook-deliveries"
			if api.Branch != wantBranch || !slices.Contains(h.srv.BranchNames(apiProj), wantBranch) {
				t.Errorf("branch = %q (forge has %v)", api.Branch, h.srv.BranchNames(apiProj))
			}
			mr, err := h.forge.MR(ctx, *api.MR)
			if err != nil {
				t.Fatal(err)
			}
			if !mr.Draft || mr.SourceBranch != wantBranch || mr.TargetBranch != "main" {
				t.Errorf("mr = %+v", mr)
			}
			for _, want := range []string{"Closes #3", "Part of " + tracker + "#3", "## Testing"} {
				if !strings.Contains(mr.Description, want) {
					t.Errorf("MR description missing %q:\n%s", want, mr.Description)
				}
			}
			mrLabels, _ := h.srv.MRLabels(*api.MR)
			if !slices.Contains(mrLabels, "type::story") {
				t.Errorf("template quick action /label did not run: %v", mrLabels)
			}
			closing, _ := h.forge.MRsForIssue(ctx, *api.Child)
			if !slices.ContainsFunc(closing, func(m forge.MR) bool { return m.Ref == *api.MR }) {
				t.Errorf("MR does not close the child: %+v", closing)
			}

			// Worktree on the branch, tracking origin, hook ran.
			if want := filepath.Join(h.app.Root, "demo-gl", "api", wantBranch); api.Worktree != want {
				t.Errorf("worktree = %s, want %s", api.Worktree, want)
			}
			if up, err := (git.Client{}).Upstream(ctx, api.Worktree); err != nil || up != "origin/"+wantBranch {
				t.Errorf("upstream = %q, %v", up, err)
			}
			if _, err := os.Stat(filepath.Join(api.Worktree, ".hook-ran")); err != nil {
				t.Errorf("post_worktree hook did not run: %v", err)
			}

			// Every forge write is audited.
			actions := h.auditActions()
			for _, want := range []string{"forge.issue.create", "forge.issue.labels", "forge.issue.link", "forge.branch.create", "forge.mr.create", "git.worktree.add"} {
				if !slices.Contains(actions, want) {
					t.Errorf("audit log lacks %s: %v", want, actions)
				}
			}
			if tier == fake.Premium && !slices.Contains(actions, "forge.issue.iteration") {
				t.Errorf("iteration copy not audited: %v", actions)
			}
			var started bool
			for _, ev := range h.drain() {
				started = started || ev.Kind == "work.started"
			}
			if !started {
				t.Error("no work.started event")
			}
		})
	}
}

func TestStartIsIdempotent(t *testing.T) {
	for _, tier := range tiers {
		t.Run(tier.String(), func(t *testing.T) {
			h := newHarness(t, tier)
			_, first, err := h.start(3, draftApi())
			if err != nil {
				t.Fatal(err)
			}
			h.drain()
			before := h.writes()
			plan, second, err := h.start(3, draftApi())
			if err != nil {
				t.Fatal(err)
			}
			if got := h.writes() - before; got != 0 {
				t.Errorf("re-run made %d forge writes, want 0", got)
			}
			if plan.Pending() != 0 || second.Created() != 0 {
				t.Errorf("re-run pending=%d created=%d: %+v", plan.Pending(), second.Created(), second.Steps)
			}
			a, b := target(t, first, "api"), target(t, second, "api")
			if *a.Child != *b.Child || *a.MR != *b.MR || a.Branch != b.Branch || a.Worktree != b.Worktree {
				t.Errorf("IDs changed between runs:\n first  %+v\n second %+v", a, b)
			}
			var resumed bool
			for _, ev := range h.drain() {
				resumed = resumed || ev.Kind == "work.resumed"
			}
			if !resumed {
				t.Error("no work.resumed event for the no-op run")
			}
		})
	}
}

func TestStartResumesAfterFailure(t *testing.T) {
	for _, point := range []string{"CreateBranch", "LinkIssues", "CreateMR"} {
		t.Run(point, func(t *testing.T) {
			h := newHarness(t, fake.Free)
			h.forge.FailNext(point, errBoom)
			_, res, err := h.start(3, draftApi())
			if err == nil || !strings.Contains(err.Error(), "boom") {
				t.Fatalf("err = %v, want the injected failure", err)
			}
			api := target(t, res, "api")
			if api.Child == nil {
				t.Fatal("child was not created before the failure")
			}
			if countOutcome(res, "failed") != 1 {
				t.Errorf("steps = %+v", res.Steps)
			}
			child := *api.Child

			_, res2, err := h.start(3, draftApi())
			if err != nil {
				t.Fatalf("resume: %v\n%+v", err, res2.Steps)
			}
			api2 := target(t, res2, "api")
			if *api2.Child != child {
				t.Errorf("resume created another child: %v then %v", child, api2.Child)
			}
			if api2.MR == nil || api2.Worktree == "" || api2.Branch == "" {
				t.Errorf("resume left work missing: %+v", api2)
			}
			issues, _ := h.forge.ListIssues(context.Background(), forge.IssueQuery{Project: apiProj, State: "all"})
			n := 0
			for _, is := range issues {
				if strings.HasPrefix(is.Description, "Parent: "+tracker+"#3") {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%d children carry the Parent: line, want 1", n)
			}
			links, _ := h.forge.Links(context.Background(), issueRef(tracker, 3))
			if !slices.ContainsFunc(links, func(l forge.Link) bool { return l.Ref == child }) {
				t.Errorf("link missing after resume: %+v", links)
			}
		})
	}
}

func TestBranchOnlyThenMR(t *testing.T) {
	h := newHarness(t, fake.Free)
	ch := draftApi()
	ch.Mode = startwork.ModeBranch
	_, res, err := h.start(4, ch)
	if err != nil {
		t.Fatal(err)
	}
	api := target(t, res, "api")
	if api.Child == nil || api.Branch == "" || api.Worktree == "" || api.MR != nil {
		t.Fatalf("branch only target = %+v", api)
	}
	for _, a := range h.auditActions() {
		if a == "forge.mr.create" {
			t.Fatal("Branch only created an MR")
		}
	}
	mrs, _ := h.forge.ListMRs(context.Background(), forge.MRQuery{Project: apiProj, SourceBranch: api.Branch, State: "all"})
	if len(mrs) != 0 {
		t.Fatalf("MRs on the branch: %+v", mrs)
	}

	// `M` later: only the MR, from the same template.
	before := h.writes()
	ch.OnlyMR, ch.Mode = true, startwork.ModeDraft
	_, res, err = h.start(4, ch)
	if err != nil {
		t.Fatal(err)
	}
	api2 := target(t, res, "api")
	if api2.MR == nil || *api2.Child != *api.Child || api2.Branch != api.Branch {
		t.Fatalf("after M: %+v", api2)
	}
	if got := h.writes() - before; got != 1 {
		t.Errorf("M made %d writes, want exactly the MR", got)
	}
	mr, _ := h.forge.MR(context.Background(), *api2.MR)
	if !mr.Draft || !strings.Contains(mr.Description, "## Testing") || !strings.Contains(mr.Description, "Closes #") {
		t.Errorf("mr from template = %+v", mr)
	}
	// And M again finds the MR.
	before = h.writes()
	_, res, err = h.start(4, ch)
	if err != nil || res.Created() != 0 || h.writes() != before {
		t.Errorf("second M created %d (err %v, %d writes)", res.Created(), err, h.writes()-before)
	}
}

func TestOnlyMRNeedsABranch(t *testing.T) {
	h := newHarness(t, fake.Free)
	ch := draftApi()
	ch.OnlyMR = true
	before := h.writes()
	_, _, err := h.start(4, ch)
	if err == nil || !strings.Contains(err.Error(), "ade start") {
		t.Fatalf("err = %v, want a hint to run start first", err)
	}
	if h.writes() != before {
		t.Error("a failed M still wrote to the forge")
	}
}

func TestBrowseWritesNothing(t *testing.T) {
	h := newHarness(t, fake.Free)
	res := h.resolve(3)
	reqs := len(h.srv.Requests())
	ch := draftApi()
	ch.Mode = startwork.ModeBrowse
	_, out, err := h.env.Start(context.Background(), res, ch, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(h.srv.Requests()) - reqs; got != 0 {
		t.Errorf("browse made %d forge requests, want none", got)
	}
	for _, a := range h.auditActions() {
		if strings.HasPrefix(a, "forge.") {
			t.Errorf("browse audited a forge action: %s", a)
		}
	}
	api := target(t, out, "api")
	if want := filepath.Join(h.app.Root, "demo-gl", "api", "main"); api.Worktree != want || api.Child != nil || api.MR != nil {
		t.Errorf("browse target = %+v, want the baseline tree %s", api, want)
	}
	if labels, _ := h.srv.IssueLabels(issueRef(tracker, 3)); slices.Contains(labels, "workflow::in progress") {
		t.Error("browse moved the workflow label")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	h := newHarness(t, fake.Free)
	r := h.resolve(3)
	before := h.writes()
	plan, res, err := h.env.Start(context.Background(), r, draftApi(), true)
	if err != nil {
		t.Fatal(err)
	}
	if h.writes() != before || !res.DryRun || plan.Writes() < 5 {
		t.Errorf("dry run: writes %d→%d, dry=%v, planned writes=%d", before, h.writes(), res.DryRun, plan.Writes())
	}
	if _, err := os.Stat(filepath.Join(h.app.Root, "demo-gl", "api", "3-retry-failed-webhook-deliveries")); err == nil {
		t.Error("dry run created a worktree")
	}
	if actions := h.auditActions(); !slices.Equal(actions, []string{"start.plan"}) {
		t.Errorf("audit = %v, want one start.plan entry", actions)
	}
}

func TestStrippingQuickActionsOnlyAffectsThisMR(t *testing.T) {
	h := newHarness(t, fake.Free)
	ch := draftApi()
	ch.StripActions = []string{"assign_reviewer"}
	plan, res, err := h.start(3, ch)
	if err != nil {
		t.Fatal(err)
	}
	api := target(t, res, "api")
	if rev, _ := h.srv.MRReviewers(*api.MR); len(rev) != 0 {
		t.Errorf("stripped template still requested review from %v", rev)
	}
	if labels, _ := h.srv.MRLabels(*api.MR); !slices.Contains(labels, "type::story") {
		t.Errorf("unstripped /label was lost: %v", labels)
	}
	// The plan listed what the template would do.
	var summaries []string
	for _, qa := range plan.Targets[0].MR.Actions {
		summaries = append(summaries, qa.Summary)
	}
	if !slices.Contains(summaries, "request review from @reviewer-rae") {
		t.Errorf("template actions = %v", summaries)
	}

	// Kept, the same template requests the review.
	h2 := newHarness(t, fake.Free)
	_, res, err = h2.start(3, draftApi())
	if err != nil {
		t.Fatal(err)
	}
	if rev, _ := h2.srv.MRReviewers(*target(t, res, "api").MR); !slices.Equal(rev, []string{"reviewer-rae"}) {
		t.Errorf("kept template reviewers = %v, want reviewer-rae", rev)
	}
	// The template in the repo is untouched.
	body, _ := h.forge.ReadFile(context.Background(), apiProj, "main", startwork.TemplateDir+"/Default.md")
	if string(body) != defaultTemplate {
		t.Errorf("repo template changed: %q", body)
	}
}

func TestEditedMRBodyReplacesTemplate(t *testing.T) {
	h := newHarness(t, fake.Free)
	ch := draftApi()
	ch.MRBody = "Closes " + startwork.ChildPlaceholder + "\n\nhand written\n"
	_, res, err := h.start(3, ch)
	if err != nil {
		t.Fatal(err)
	}
	api := target(t, res, "api")
	mr, _ := h.forge.MR(context.Background(), *api.MR)
	if want := "Closes #" + strconv.Itoa(api.Child.IID) + "\n\nhand written"; strings.TrimSpace(mr.Description) != want {
		t.Errorf("description = %q, want %q", mr.Description, want)
	}
	if labels, _ := h.srv.MRLabels(*api.MR); len(labels) != 0 {
		t.Errorf("template ran though the body was replaced: %v", labels)
	}
}

func TestExistingChildIsFoundByLinkAndBranch(t *testing.T) {
	// tracker#2: web!1 (draft) and its child web#1 are seeded.
	h := newHarness(t, fake.Free)
	h.remote["web"].Branch("1-paginate-the-audit-log-view")
	ch := startwork.Choices{Repos: []string{"web"}, Mode: startwork.ModeDraft, Child: startwork.ChildAuto}
	before := h.writes()
	plan, res, err := h.start(2, ch)
	if err != nil {
		t.Fatal(err)
	}
	web := target(t, res, "web")
	if web.Child == nil || web.Child.String() != webProj+"#1" || web.MR == nil || web.MR.String() != webProj+"!1" {
		t.Fatalf("web target = %+v", web)
	}
	if h.writes() != before {
		t.Errorf("starting seeded work wrote to the forge (%d writes)", h.writes()-before)
	}
	// Only the missing local checkout was left to do.
	for _, s := range plan.Steps {
		if !s.Done && s.Kind != startwork.StepWorktree && s.Kind != startwork.StepWorkspace {
			t.Errorf("unexpected pending step %+v", s)
		}
	}
	if web.Worktree == "" {
		t.Error("worktree for the existing branch was not added")
	}
}

func TestWorkflowLabelMoves(t *testing.T) {
	cases := []struct {
		name   string
		labels []string // set on tracker#3 before starting
		sync   string
		choice func(*startwork.Choices)
		want   []string // workflow labels after
	}{
		{"none → in progress", nil, "", nil, []string{"workflow::in progress"}},
		{"removes the old label explicitly", []string{"workflow::backlog"}, "", nil, []string{"workflow::in progress"}},
		{"removes every other workflow label (Free keeps them all)", []string{"workflow::backlog", "workflow::blocked"}, "", nil, []string{"workflow::in progress"}},
		{"already there is untouched", []string{"workflow::in progress"}, "", nil, []string{"workflow::in progress"}},
		{"further along is never moved back", []string{"workflow::in review"}, "", nil, []string{"workflow::in review"}},
		{"done stays done", []string{"workflow::done"}, "", nil, []string{"workflow::done"}},
		{"off does nothing", []string{"workflow::backlog"}, "off", nil, []string{"workflow::backlog"}},
		{"ask without confirmation does nothing", []string{"workflow::backlog"}, "ask", nil, []string{"workflow::backlog"}},
		{"ask with confirmation moves", []string{"workflow::backlog"}, "ask", func(c *startwork.Choices) { c.MoveLabel = true }, []string{"workflow::in progress"}},
		{"auto declined in the dialog", []string{"workflow::backlog"}, "", func(c *startwork.Choices) { c.NoMoveLabel = true }, []string{"workflow::backlog"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, fake.Free)
			if tc.sync != "" {
				h.m.Labels.Workflow.Sync = tc.sync
			}
			if len(tc.labels) > 0 {
				if err := h.forge.SetLabels(context.Background(), issueRef(tracker, 3), tc.labels, nil); err != nil {
					t.Fatal(err)
				}
			}
			ch := draftApi()
			if tc.choice != nil {
				tc.choice(&ch)
			}
			if _, _, err := h.start(3, ch); err != nil {
				t.Fatal(err)
			}
			labels, _ := h.srv.IssueLabels(issueRef(tracker, 3))
			var got []string
			for _, l := range labels {
				if strings.HasPrefix(l, "workflow::") {
					got = append(got, l)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("workflow labels = %v, want %v", got, tc.want)
			}
			if !slices.Contains(labels, "type::story") || !slices.Contains(labels, "app::api") {
				t.Errorf("unrelated labels lost: %v", labels)
			}
		})
	}
}

func TestCrossRepoItemGetsAChildInEach(t *testing.T) {
	h := newHarness(t, fake.Free)
	r := h.resolve(8) // app::api + app::web
	ch := startwork.DefaultChoices(h.m, r.Item)
	if !slices.Equal(ch.Repos, []string{"api", "web"}) {
		t.Fatalf("preselected repos = %v, want both", ch.Repos)
	}
	_, res, err := h.env.Start(context.Background(), r, ch, false)
	if err != nil {
		t.Fatal(err)
	}
	api, web := target(t, res, "api"), target(t, res, "web")
	if api.Child == nil || web.Child == nil || api.Child.Project != apiProj || web.Child.Project != webProj {
		t.Fatalf("children = %+v / %+v", api, web)
	}
	if api.MR == nil || web.MR == nil {
		t.Errorf("MRs = %v / %v", api.MR, web.MR)
	}
	// One label move for the item, not one per repo.
	n := 0
	for _, a := range h.auditActions() {
		if a == "forge.issue.labels" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d label moves, want 1", n)
	}
	links, _ := h.forge.Links(context.Background(), issueRef(tracker, 8))
	if len(links) != 2 {
		t.Errorf("links = %+v, want one per child", links)
	}
}

func TestNoChildMeansBranchAndMRReferenceTracker(t *testing.T) {
	h := newHarness(t, fake.Free)
	ch := draftApi()
	ch.Child = startwork.ChildNever
	_, res, err := h.start(4, ch)
	if err != nil {
		t.Fatal(err)
	}
	api := target(t, res, "api")
	if api.Child != nil || api.Branch != "4-remove-the-legacy-csv-export-path" || api.MR == nil {
		t.Fatalf("target = %+v", api)
	}
	mr, _ := h.forge.MR(context.Background(), *api.MR)
	if !strings.Contains(mr.Description, "Closes "+tracker+"#4") {
		t.Errorf("MR does not close the tracker issue:\n%s", mr.Description)
	}
	if strings.Contains(mr.Description, "Part of") {
		t.Errorf("redundant parent mention:\n%s", mr.Description)
	}
	for _, a := range h.auditActions() {
		if a == "forge.issue.create" || a == "forge.issue.link" {
			t.Errorf("child policy never still wrote %s", a)
		}
	}
	// Re-run: found by exact branch name, so nothing is created.
	before := h.writes()
	if _, res2, err := h.start(4, ch); err != nil || res2.Created() != 0 || h.writes() != before {
		t.Errorf("re-run without child: created %d, %v", res2.Created(), err)
	}
}

func TestTrackerProjectAsTargetNeedsNoChild(t *testing.T) {
	h := newHarness(t, fake.Free)
	h.srv.AddFile(tracker, startwork.TemplateDir+"/Default.md", "tracker template\n")
	plan, err := startwork.BuildPlan(startwork.PlanInput{
		Item: h.resolve(4).Item, Manifest: h.m, Root: h.app.Root,
		Choices: startwork.Choices{Repos: []string{startwork.TrackerRepo}, Mode: startwork.ModeBranch, Child: startwork.ChildAuto},
	})
	if err != nil {
		t.Fatal(err)
	}
	tp := plan.Targets[0]
	if tp.UseChild || !tp.Target.Tracker {
		t.Errorf("target = %+v", tp)
	}
	for _, s := range plan.Steps {
		if s.Kind == startwork.StepChild || s.Kind == startwork.StepLink {
			t.Errorf("tracker target planned %s", s.Kind)
		}
	}
	if tp.Branch != "4-remove-the-legacy-csv-export-path" {
		t.Errorf("branch = %s", tp.Branch)
	}
}

func TestUnknownRepoAndEmptySelection(t *testing.T) {
	h := newHarness(t, fake.Free)
	ch := draftApi()
	ch.Repos = []string{"nope"}
	if _, _, err := h.start(3, ch); err == nil || !strings.Contains(err.Error(), "unknown repo") {
		t.Errorf("err = %v", err)
	}
	ch.Repos = nil
	if _, _, err := h.start(3, ch); err == nil || !strings.Contains(err.Error(), "no repo chosen") {
		t.Errorf("err = %v", err)
	}
	if got := startwork.RequiredRepos(h.m, []string{"web"}); !slices.Equal(got, []string{"api"}) {
		t.Errorf("RequiredRepos(web) = %v, want api", got)
	}
	if got := startwork.RequiredRepos(h.m, []string{"web", "api"}); len(got) != 0 {
		t.Errorf("RequiredRepos(web, api) = %v, want none", got)
	}
}

func TestMissingCloneFailsTheWorktreeStepOnly(t *testing.T) {
	h := newHarness(t, fake.Free)
	if err := os.RemoveAll(filepath.Join(h.app.Root, "demo-gl", "api")); err != nil {
		t.Fatal(err)
	}
	_, res, err := h.start(3, draftApi())
	if err == nil || !strings.Contains(err.Error(), "ade project add") {
		t.Fatalf("err = %v, want a hint to add the project", err)
	}
	api := target(t, res, "api")
	if api.MR == nil {
		t.Errorf("forge work should be complete before the local step fails: %+v", api)
	}
}

func TestWorkspaceHookRunsAfterWorktree(t *testing.T) {
	h := newHarness(t, fake.Free)
	var got []startwork.WorkspaceRequest
	h.env.Workspace = func(_ context.Context, req startwork.WorkspaceRequest) error {
		if _, err := os.Stat(req.Worktree); err != nil {
			t.Errorf("workspace opened before the worktree exists: %v", err)
		}
		got = append(got, req)
		return nil
	}
	_, res, err := h.start(3, draftApi())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Repo != "api" || got[0].Project != "demo-gl" || got[0].Worktree != target(t, res, "api").Worktree ||
		got[0].Title != "3-retry-failed-webhook-deliveries" || got[0].ItemTrees["api"] != got[0].Worktree {
		t.Errorf("workspace requests = %+v", got)
	}

	// A host that cannot open the tab does not fail the start.
	h2 := newHarness(t, fake.Free)
	h2.env.Workspace = func(context.Context, startwork.WorkspaceRequest) error { return errBoom }
	_, res, err = h2.start(3, draftApi())
	if err != nil {
		t.Fatalf("workspace failure failed the start: %v", err)
	}
	if !slices.ContainsFunc(res.Notes, func(n string) bool { return strings.Contains(n, "workspace not opened") }) {
		t.Errorf("notes = %v", res.Notes)
	}
}

func TestResolveRefusesClosedAndUnknownIssues(t *testing.T) {
	h := newHarness(t, fake.Free)
	ctx := context.Background()
	if _, err := startwork.ResolveWith(ctx, h.forge, h.m, issueRef(tracker, 10)); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("closed issue err = %v", err)
	}
	if _, err := startwork.ResolveWith(ctx, h.forge, h.m, issueRef(tracker, 999)); err == nil {
		t.Error("unknown issue was accepted")
	}
}
