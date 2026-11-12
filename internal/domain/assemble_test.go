package domain

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

const (
	grp = "ade-demo-env"
	me  = "paul"
)

var now = time.Date(2026, time.November, 11, 10, 0, 0, 0, time.UTC)

func tracker(iid int, title string, assigned bool, state string, labels ...string) forge.Issue {
	is := forge.Issue{
		Ref: forge.Ref{Project: grp + "/tracker", IID: iid}, Title: title, State: state, Labels: labels,
		Milestone: &forge.Milestone{ID: 2, Title: "Sprint 42", Start: d("2026-11-02"), Due: d("2026-11-13")},
	}
	if assigned {
		is.Assignees = []forge.User{{Username: me}}
	}
	return is
}

func child(project string, iid, parent int) forge.Issue {
	return forge.Issue{
		Ref:         forge.Ref{Project: grp + "/" + project, IID: iid},
		State:       "opened",
		Description: "Parent: " + grp + "/tracker#" + strconv.Itoa(parent) + "\n\nImplementation issue.",
	}
}

func mr(project string, iid int, mut func(*forge.MR)) forge.MR {
	m := forge.MR{
		Ref: forge.Ref{Project: grp + "/" + project, IID: iid, Kind: forge.KindMR}, State: "opened",
		Author: forge.User{Username: me}, Approvals: forge.Approvals{Required: -1},
	}
	if mut != nil {
		mut(&m)
	}
	return m
}

func link(c forge.Issue) forge.Link { return forge.Link{Ref: c.Ref, Type: "relates_to"} }

// TestSandboxTrackerIssues models the 11 tracker issues scripts/sandbox/gitlab-seed.sh creates.
func TestSandboxTrackerIssues(t *testing.T) {
	apiRate := child("api", 1, 1)
	webAudit := child("web", 1, 2)
	webFont := child("web", 2, 9)
	apiDeps := child("api", 2, 10)
	for _, c := range []*forge.Issue{&webFont, &apiDeps} {
		c.State = "closed"
	}
	mrRate := mr("api", 1, func(m *forge.MR) { m.Unresolved, m.Resolved = 2, 1 })
	mrAudit := mr("web", 1, func(m *forge.MR) { m.Draft = true })
	mrFont := mr("web", 2, func(m *forge.MR) { m.State = "merged" })
	all := []forge.Issue{apiRate, webAudit, webFont, apiDeps}

	tests := []struct {
		name       string
		tracker    forge.Issue
		links      []forge.Link
		mrs        []forge.MR
		stage      Stage
		court      Court
		reason     string
		apps       []string
		repos      []string
		workflow   string
		want       string
		drift      bool
		triage     []string
		blocked    bool
		hasSprint  bool
		assigned   bool
		childCount int
	}{
		{
			name: "#1 rate limit: review, 2 unresolved", tracker: tracker(1, "Rate-limit", true, "opened", "type::story", "app::api", "workflow::in review"),
			links: []forge.Link{link(apiRate)}, mrs: []forge.MR{mrRate},
			stage: StageReview, court: CourtYou, reason: "2 unresolved threads",
			apps: []string{"api"}, repos: []string{"api"}, workflow: "in review", want: "in review",
			triage: []string{"weight"}, hasSprint: true, assigned: true, childCount: 1,
		},
		{
			name: "#2 audit: draft", tracker: tracker(2, "Paginate", true, "opened", "type::bug", "app::web", "workflow::in progress"),
			links: []forge.Link{link(webAudit)}, mrs: []forge.MR{mrAudit},
			stage: StageDraft, court: CourtYou, reason: "draft MR · mark ready",
			apps: []string{"web"}, repos: []string{"web"}, workflow: "in progress", want: "in progress",
			triage: []string{"weight"}, hasSprint: true, assigned: true, childCount: 1,
		},
		{
			name: "#3 webhook: assigned, start work", tracker: tracker(3, "Retry", true, "opened", "type::story", "app::api"),
			stage: StageAssigned, court: CourtYou, reason: "start work",
			apps: []string{"api"}, repos: []string{"api"}, triage: []string{"weight"}, hasSprint: true, assigned: true,
		},
		{
			name: "#4 csv: assigned, start work", tracker: tracker(4, "Remove CSV", true, "opened", "type::story", "app::api"),
			stage: StageAssigned, court: CourtYou, reason: "start work",
			apps: []string{"api"}, repos: []string{"api"}, triage: []string{"weight"}, hasSprint: true, assigned: true,
		},
		{
			name: "#5 timezone: blocked", tracker: tracker(5, "TZ", true, "opened", "type::bug", "app::api", "workflow::blocked"),
			stage: StageAssigned, court: CourtBlocked, reason: "label workflow::blocked", blocked: true,
			apps: []string{"api"}, repos: []string{"api"}, workflow: "blocked", triage: []string{"weight"}, hasSprint: true, assigned: true,
		},
		{
			name: "#6 billing: unassigned", tracker: tracker(6, "Billing", false, "opened", "type::bug", "app::api"),
			stage: StageAssigned, court: CourtNobody, reason: "unassigned",
			apps: []string{"api"}, repos: []string{"api"}, triage: []string{"weight"}, hasSprint: true,
		},
		{
			name: "#7 dark mode: unassigned", tracker: tracker(7, "Dark", false, "opened", "type::story", "app::web"),
			stage: StageAssigned, court: CourtNobody, reason: "unassigned",
			apps: []string{"web"}, repos: []string{"web"}, triage: []string{"weight"}, hasSprint: true,
		},
		{
			name: "#8 scheduled exports: multi-app", tracker: tracker(8, "Scheduled", true, "opened", "type::story", "app::api", "app::web"),
			stage: StageAssigned, court: CourtYou, reason: "start work",
			apps: []string{"api", "web"}, repos: []string{"api", "web"}, triage: []string{"weight"}, hasSprint: true, assigned: true,
		},
		{
			name: "#9 font: done", tracker: tracker(9, "Font", true, "closed", "type::bug", "app::web", "workflow::done"),
			links: []forge.Link{link(webFont)}, mrs: []forge.MR{mrFont},
			stage: StageDone, court: CourtNobody, reason: "closed",
			apps: []string{"web"}, repos: []string{"web"}, workflow: "done", hasSprint: true, assigned: true, childCount: 1,
		},
		{
			name: "#10 deps: done", tracker: tracker(10, "Deps", true, "closed", "type::story", "app::api", "workflow::done"),
			links: []forge.Link{link(apiDeps)}, stage: StageDone, court: CourtNobody, reason: "closed",
			apps: []string{"api"}, repos: []string{"api"}, workflow: "done", hasSprint: true, assigned: true, childCount: 1,
		},
		{
			name: "#11 stale totals: needs triage", tracker: tracker(11, "Stale", true, "opened"),
			stage: StageAssigned, court: CourtYou, reason: "start work",
			triage: []string{"type", "app", "weight"}, hasSprint: true, assigned: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Assemble(AssembleInput{
				Project: "demo-gl", Manifest: sandboxManifest(), Me: me, Tracker: tt.tracker,
				Links: tt.links, Candidates: all, MRs: tt.mrs, Now: now,
			})
			if got.Stage != tt.stage || got.Court != tt.court || got.Reason != tt.reason {
				t.Errorf("stage/court/reason = %v/%v/%q, want %v/%v/%q", got.Stage, got.Court, got.Reason, tt.stage, tt.court, tt.reason)
			}
			if !reflect.DeepEqual(got.Apps, tt.apps) || !reflect.DeepEqual(got.Repos, tt.repos) {
				t.Errorf("apps/repos = %v/%v, want %v/%v", got.Apps, got.Repos, tt.apps, tt.repos)
			}
			if got.Workflow != tt.workflow || got.WantWorkflow != tt.want || got.Drift != tt.drift {
				t.Errorf("workflow/want/drift = %q/%q/%v, want %q/%q/%v", got.Workflow, got.WantWorkflow, got.Drift, tt.workflow, tt.want, tt.drift)
			}
			if !reflect.DeepEqual(got.Triage, tt.triage) {
				t.Errorf("triage = %v, want %v", got.Triage, tt.triage)
			}
			if got.Blocked != tt.blocked || got.Assigned != tt.assigned || len(got.Children) != tt.childCount || (got.Sprint != nil) != tt.hasSprint {
				t.Errorf("blocked/assigned/children/sprint = %v/%v/%d/%v", got.Blocked, got.Assigned, len(got.Children), got.Sprint != nil)
			}
			if got.Sprint != nil && (got.Sprint.Title != "Sprint 42" || got.Sprint.Source != "milestones") {
				t.Errorf("sprint = %+v", got.Sprint)
			}
		})
	}
}

func TestAssembleDriftAndTriage(t *testing.T) {
	m := sandboxManifest()
	m.Tracker.Weight.Source = "none"
	api := child("api", 1, 1)
	// in review per MR, but labelled "in progress": drift.
	got := Assemble(AssembleInput{
		Manifest: m, Me: me, Tracker: tracker(1, "x", true, "opened", "type::story", "app::api", "workflow::in progress"),
		Links: []forge.Link{link(api)}, Candidates: []forge.Issue{api}, MRs: []forge.MR{mr("api", 1, nil)},
	})
	if !got.Drift || got.WantWorkflow != "in review" || got.Workflow != "in progress" {
		t.Errorf("drift = %v want=%q have=%q", got.Drift, got.WantWorkflow, got.Workflow)
	}
	if len(got.Triage) != 0 {
		t.Errorf("triage = %v, weight source none must not demand weight", got.Triage)
	}

	// no app pattern configured: missing app is not triage.
	m.Labels.App.Pattern = ""
	got = Assemble(AssembleInput{Manifest: m, Me: me, Tracker: tracker(2, "y", true, "opened", "type::story")})
	if len(got.Triage) != 0 {
		t.Errorf("triage = %v, want none", got.Triage)
	}
}

func TestAssembleWeightedNotInTriage(t *testing.T) {
	m := sandboxManifest()
	m.Tracker.Weight = configWeight("label")
	got := Assemble(AssembleInput{Manifest: m, Me: me, Tracker: tracker(1, "x", true, "opened", "type::story", "app::api", "weight::5")})
	if got.Weight == nil || *got.Weight != 5 || len(got.Triage) != 0 {
		t.Errorf("weight=%v triage=%v", deref(got.Weight), got.Triage)
	}
}

func TestAssembleNilManifest(t *testing.T) {
	got := Assemble(AssembleInput{Tracker: tracker(1, "x", true, "opened"), Me: me})
	if got.Stage != StageAssigned || got.Court != CourtYou {
		t.Errorf("got %v/%v", got.Stage, got.Court)
	}
}

// Teams that close sprint work only at sprint end label it done and leave the
// tracker open: that is done (counted, no nagging), unless an MR is still open.
func TestAssembleDoneKeptOpen(t *testing.T) {
	m := sandboxManifest()
	m.Labels.Workflow.Done = "done"
	api := child("api", 1, 1)
	merged := mr("api", 1, func(x *forge.MR) { x.State = "merged" })
	in := func(mrs []forge.MR, labels ...string) AssembleInput {
		return AssembleInput{
			Manifest: m, Me: me, Tracker: tracker(1, "x", true, "opened", append([]string{"type::story", "app::api"}, labels...)...),
			Links: []forge.Link{link(api)}, Candidates: []forge.Issue{api}, MRs: mrs,
		}
	}
	tests := []struct {
		name     string
		in       AssembleInput
		stage    Stage
		keptOpen bool
		court    Court
		reason   string
		drift    bool
	}{
		{"merged and labelled done", in([]forge.MR{merged}, "workflow::in review", "workflow::done"), StageDone, true, CourtNobody, "done · issue kept open", false},
		{"labelled done without MR", in(nil, "workflow::done"), StageDone, true, CourtNobody, "done · issue kept open", false},
		{"merged, label not moved yet", in([]forge.MR{merged}, "workflow::in review"), StageMerged, false, CourtYou, "set workflow::done", true},
		{"done label ahead of an open MR", in([]forge.MR{mr("api", 1, nil)}, "workflow::done"), StageReview, false, CourtReviewers, "waiting on review", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Assemble(tt.in)
			if got.Stage != tt.stage || got.KeptOpen != tt.keptOpen || got.Court != tt.court || got.Reason != tt.reason || got.Drift != tt.drift {
				t.Errorf("stage/kept/court/reason/drift = %v/%v/%v/%q/%v, want %v/%v/%v/%q/%v",
					got.Stage, got.KeptOpen, got.Court, got.Reason, got.Drift, tt.stage, tt.keptOpen, tt.court, tt.reason, tt.drift)
			}
		})
	}
}

func TestStage(t *testing.T) {
	open := func(mut func(*forge.MR)) forge.MR { return mr("api", 1, mut) }
	pipe := func(s string) func(*forge.MR) {
		return func(m *forge.MR) { m.Pipeline = &forge.Pipeline{Status: s} }
	}
	merged := open(func(m *forge.MR) { m.State = "merged" })
	closedMR := open(func(m *forge.MR) { m.State = "closed" })
	kid := []forge.Issue{child("api", 1, 1)}
	tests := []struct {
		name     string
		state    string
		kids     []forge.Issue
		mrs      []forge.MR
		branches []string
		want     Stage
	}{
		{"nothing", "opened", nil, nil, nil, StageAssigned},
		{"child", "opened", kid, nil, nil, StageStarted},
		{"branch evidence", "opened", kid, nil, []string{"1-x"}, StageBranch},
		{"branch without child", "opened", nil, nil, []string{"1-x"}, StageBranch},
		{"draft", "opened", kid, []forge.MR{open(func(m *forge.MR) { m.Draft = true })}, nil, StageDraft},
		{"ready no pipeline", "opened", kid, []forge.MR{open(nil)}, nil, StageReview},
		{"pipeline success", "opened", kid, []forge.MR{open(pipe("success"))}, nil, StageReview},
		{"pipeline running", "opened", kid, []forge.MR{open(pipe("running"))}, nil, StageCI},
		{"pipeline pending", "opened", kid, []forge.MR{open(pipe("pending"))}, nil, StageCI},
		{"pipeline created", "opened", kid, []forge.MR{open(pipe("created"))}, nil, StageCI},
		{"pipeline failed", "opened", kid, []forge.MR{open(pipe("failed"))}, nil, StageCI},
		{"pipeline skipped is review", "opened", kid, []forge.MR{open(pipe("skipped"))}, nil, StageReview},
		{"draft with running pipeline is draft", "opened", kid, []forge.MR{open(func(m *forge.MR) { m.Draft = true; pipe("running")(m) })}, nil, StageDraft},
		{"all merged", "opened", kid, []forge.MR{merged}, nil, StageMerged},
		{"merged beats branch", "opened", kid, []forge.MR{merged}, []string{"x"}, StageMerged},
		{"only closed MR", "opened", kid, []forge.MR{closedMR}, nil, StageStarted},
		{"merged plus closed", "opened", kid, []forge.MR{merged, closedMR}, nil, StageMerged},
		{"least advanced open wins", "opened", kid, []forge.MR{open(nil), mr("web", 1, func(m *forge.MR) { m.Draft = true })}, nil, StageDraft},
		{"open beats merged", "opened", kid, []forge.MR{merged, mr("web", 1, nil)}, nil, StageReview},
		{"closed tracker", "closed", kid, []forge.MR{open(nil)}, nil, StageDone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := tracker(1, "x", true, tt.state)
			if got := deriveStage(tr, tt.kids, tt.mrs, tt.branches); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCourt(t *testing.T) {
	pipe := func(s string) func(*forge.MR) {
		return func(m *forge.MR) { m.Pipeline = &forge.Pipeline{Status: s} }
	}
	approved := func(n, required int) func(*forge.MR) {
		return func(m *forge.MR) {
			m.Approvals.Required = required
			for range n {
				m.Approvals.ApprovedBy = append(m.Approvals.ApprovedBy, forge.User{Username: "rev"})
			}
		}
	}
	both := func(fs ...func(*forge.MR)) func(*forge.MR) {
		return func(m *forge.MR) {
			for _, f := range fs {
				f(m)
			}
		}
	}
	kid := child("api", 1, 1)
	tests := []struct {
		name     string
		labels   []string
		links    []forge.Link
		assigned bool
		kids     []forge.Issue
		mrs      []forge.MR
		branches []string
		court    Court
		reason   string
	}{
		{"pipeline failed", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, pipe("failed"))}, nil, CourtYou, "pipeline failed"},
		{"failed beats unresolved", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, both(pipe("failed"), func(m *forge.MR) { m.Unresolved = 3 }))}, nil, CourtYou, "pipeline failed"},
		{"one unresolved thread", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, func(m *forge.MR) { m.Unresolved = 1 })}, nil, CourtYou, "1 unresolved thread"},
		{"three unresolved threads", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, func(m *forge.MR) { m.Unresolved = 3 })}, nil, CourtYou, "3 unresolved threads"},
		{"draft", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, func(m *forge.MR) { m.Draft = true })}, nil, CourtYou, "draft MR · mark ready"},
		{"pipeline running", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, pipe("running"))}, nil, CourtCI, "pipeline running"},
		{"pipeline pending", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, pipe("pending"))}, nil, CourtCI, "pipeline pending"},
		{"approvals unknown, none yet", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, nil)}, nil, CourtReviewers, "waiting on review"},
		{"approvals 1 of 2", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, approved(1, 2))}, nil, CourtReviewers, "1 of 2 approvals"},
		{"approvals 0 of 2", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, approved(0, 2))}, nil, CourtReviewers, "0 of 2 approvals"},
		{"approved, required known", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, both(approved(2, 2), pipe("success")))}, nil, CourtYou, "ready to merge"},
		{"approved, required unknown", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, approved(1, -1))}, nil, CourtYou, "ready to merge"},
		{"no approvals required", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, approved(0, 0))}, nil, CourtYou, "ready to merge"},
		{"conflicts", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, both(approved(1, 1), func(m *forge.MR) { m.HasConflicts = true }))}, nil, CourtYou, "merge conflicts"},
		{"others' MR unresolved: author acts", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, func(m *forge.MR) { m.Author.Username = "sam"; m.Unresolved = 1 })}, nil, CourtAuthor, "1 unresolved thread"},
		{"merged, tracker open", nil, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, func(m *forge.MR) { m.State = "merged" })}, nil, CourtYou, "close tracker"},
		{"child, no MR", nil, nil, true, []forge.Issue{kid}, nil, nil, CourtYou, "no MR yet"},
		{"branch, no MR", nil, nil, true, nil, nil, []string{"x"}, CourtYou, "branch pushed · open MR"},
		{"assigned, nothing", nil, nil, true, nil, nil, nil, CourtYou, "start work"},
		{"unassigned", nil, nil, false, nil, nil, nil, CourtNobody, "unassigned"},
		{"unassigned but my MR needs me", nil, nil, false, []forge.Issue{kid}, []forge.MR{mr("api", 1, func(m *forge.MR) { m.Draft = true })}, nil, CourtYou, "draft MR · mark ready"},
		{"blocked label", []string{"Blocked"}, nil, true, nil, nil, nil, CourtBlocked, "label Blocked"},
		{"blocked workflow label", []string{"workflow::blocked"}, nil, true, nil, nil, nil, CourtBlocked, "label workflow::blocked"},
		{"blocked beats MR state", []string{"workflow::blocked"}, nil, true, []forge.Issue{kid}, []forge.MR{mr("api", 1, pipe("failed"))}, nil, CourtBlocked, "label workflow::blocked"},
		{"blocked by link", nil, []forge.Link{{Ref: ref("g/p#9"), Type: "is_blocked_by"}}, true, nil, nil, nil, CourtBlocked, "blocked by g/p#9"},
		{"blocks link is not blocked", nil, []forge.Link{{Ref: ref("g/p#9"), Type: "blocks"}}, true, nil, nil, nil, CourtYou, "start work"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Assemble(AssembleInput{
				Manifest: sandboxManifest(), Me: me, Tracker: tracker(1, "x", tt.assigned, "opened", append([]string{"type::story", "app::api"}, tt.labels...)...),
				Links: tt.links, Candidates: tt.kids, MRs: tt.mrs, Branches: tt.branches,
			})
			if got.Court != tt.court || got.Reason != tt.reason {
				t.Errorf("court/reason = %v/%q, want %v/%q", got.Court, got.Reason, tt.court, tt.reason)
			}
		})
	}
}

func TestUpstream(t *testing.T) {
	apiKid, webKid := child("api", 1, 1), child("web", 1, 1)
	draft := func(m *forge.MR) { m.Draft = true }
	failed := func(m *forge.MR) { m.Pipeline = &forge.Pipeline{Status: "failed"} }
	tests := []struct {
		name   string
		mrs    []forge.MR
		court  Court
		reason string
		up     string
		stage  Stage
	}{
		{"web draft waits on api review", []forge.MR{mr("api", 1, nil), mr("web", 1, draft)}, CourtUpstream, "waits on " + grp + "/api!1", grp + "/api!1", StageDraft},
		{"web ready waits on api ready (tie: dependent shown)", []forge.MR{mr("api", 1, nil), mr("web", 1, nil)}, CourtUpstream, "waits on " + grp + "/api!1", grp + "/api!1", StageReview},
		{"api is the laggard: act on api", []forge.MR{mr("api", 1, draft), mr("web", 1, nil)}, CourtYou, "draft MR · mark ready", "", StageDraft},
		{"api failing, web ready: act on api", []forge.MR{mr("api", 1, failed), mr("web", 1, nil)}, CourtYou, "pipeline failed", "", StageCI},
		{"api merged: web no longer waits", []forge.MR{mr("api", 1, func(m *forge.MR) { m.State = "merged" }), mr("web", 1, draft)}, CourtYou, "draft MR · mark ready", "", StageDraft},
		{"api needs nothing from web", []forge.MR{mr("api", 1, draft), mr("web", 1, draft)}, CourtUpstream, "waits on " + grp + "/api!1", grp + "/api!1", StageDraft},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Assemble(AssembleInput{
				Manifest: sandboxManifest(), Me: me, Tracker: tracker(1, "x", true, "opened", "type::story", "app::api", "app::web"),
				Links: []forge.Link{link(apiKid), link(webKid)}, Candidates: []forge.Issue{apiKid, webKid}, MRs: tt.mrs,
			})
			if got.Court != tt.court || got.Reason != tt.reason || got.Stage != tt.stage {
				t.Errorf("stage/court/reason = %v/%v/%q, want %v/%v/%q", got.Stage, got.Court, got.Reason, tt.stage, tt.court, tt.reason)
			}
			switch {
			case tt.up == "" && got.Upstream != nil:
				t.Errorf("Upstream = %v, want nil", got.Upstream)
			case tt.up != "" && (got.Upstream == nil || got.Upstream.String() != tt.up):
				t.Errorf("Upstream = %v, want %s", got.Upstream, tt.up)
			}
		})
	}
}

func TestCourtNamesTheAssigneeOfSomeoneElsesTicket(t *testing.T) {
	is := tracker(1, "x", false, "opened", "type::story", "app::api")
	is.Assignees = []forge.User{{ID: 7, Username: "sam", Name: "Sam Smith"}, {Username: "kim"}}
	got := Assemble(AssembleInput{Manifest: sandboxManifest(), Me: me, Tracker: is})
	if got.Assigned || got.Court != CourtNobody || got.Reason != "assigned to Sam Smith (sam), kim" {
		t.Errorf("assigned/court/reason = %v/%v/%q", got.Assigned, got.Court, got.Reason)
	}
}

func TestAssignedToMatchesByIDThenHandle(t *testing.T) {
	is := forge.Issue{Assignees: []forge.User{{ID: 5, Username: "paul-new"}}}
	tests := []struct {
		name string
		me   string
		meID int64
		want bool
	}{
		{"same id, renamed handle", "paul", 5, true},
		{"other id, same handle", "paul-new", 6, false},
		{"id unknown, handle cased differently", "Paul-New", 0, true},
		{"id unknown, other handle", "paul", 0, false},
		{"nobody", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := assignedTo(is, tt.me, tt.meID); got != tt.want {
				t.Errorf("assignedTo = %v, want %v", got, tt.want)
			}
		})
	}
}

// Work is mine when I hold an open child of the tracker issue, whoever holds
// the tracker issue itself; a closed child does not make it mine.
func TestAssembleIsAssignedThroughAnOpenChildOfMine(t *testing.T) {
	mine := func(state string) forge.Issue {
		c := child("web", 3, 1)
		c.State = state
		c.Assignees = []forge.User{{Username: me}}
		return c
	}
	tests := []struct {
		name string
		kid  forge.Issue
		want bool
	}{
		{"open child of mine", mine("opened"), true},
		{"closed child of mine", mine("closed"), false},
		{"open child nobody holds", child("web", 3, 1), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := tracker(1, "x", false, "opened", "type::story", "app::web")
			tr.Assignees = []forge.User{{Username: "sam"}}
			got := Assemble(AssembleInput{Manifest: sandboxManifest(), Me: me, Tracker: tr, Links: []forge.Link{link(tt.kid)}, Candidates: []forge.Issue{tt.kid}})
			if got.Assigned != tt.want {
				t.Errorf("Assigned = %v, want %v (reason %q)", got.Assigned, tt.want, got.Reason)
			}
		})
	}
}
