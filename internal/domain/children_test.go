package domain

import (
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
)

// An item with one tracker issue and three children in three repos (none of
// them ordered by the manifest, so no MR waits on another).
func TestSeveralChildrenStageAndCourt(t *testing.T) {
	kids := []forge.Issue{child("api", 1, 1), child("docs", 2, 1), child("infra", 3, 1)}
	merged := func(m *forge.MR) { m.State = "merged" }
	draft := func(m *forge.MR) { m.Draft = true }
	failed := func(m *forge.MR) { m.Pipeline = &forge.Pipeline{Status: "failed"} }
	running := func(m *forge.MR) { m.Pipeline = &forge.Pipeline{Status: "running"} }
	ready := func(m *forge.MR) { m.Approvals.Required = 0 }
	threads := func(m *forge.MR) { m.Unresolved = 2 }
	theirs := func(m *forge.MR) { m.Author.Username = "sam"; m.Unresolved = 1 }
	tests := []struct {
		name     string
		kids     []forge.Issue
		mrs      []forge.MR
		branches []string
		stage    Stage
		court    Court
		reason   string
	}{
		{
			"failed pipeline on a non-focus MR is not hidden",
			kids,
			[]forge.MR{mr("api", 1, draft), mr("docs", 1, failed), mr("infra", 1, ready)},
			nil, StageDraft, CourtYou, "docs!1 · pipeline failed · +2 more",
		},
		{
			"unresolved threads outrank a draft",
			kids,
			[]forge.MR{mr("api", 1, draft), mr("docs", 1, threads), mr("infra", 1, running)},
			nil, StageDraft, CourtYou, "docs!1 · 2 unresolved threads · +1 more",
		},
		{
			"waiting MRs leave the court to the one you can act on",
			kids,
			[]forge.MR{mr("api", 1, running), mr("docs", 1, nil), mr("infra", 1, failed)},
			nil, StageCI, CourtYou, "infra!1 · pipeline failed",
		},
		{
			"ci outranks reviewers",
			kids,
			[]forge.MR{mr("api", 1, nil), mr("docs", 1, running), mr("infra", 1, nil)},
			nil, StageCI, CourtCI, "docs!1 · pipeline running",
		},
		{
			"someone else's MR needs action before mine wait",
			kids,
			[]forge.MR{mr("api", 1, theirs), mr("docs", 1, running), mr("infra", 1, nil)},
			nil, StageCI, CourtAuthor, "api!1 · 1 unresolved thread",
		},
		{
			"one merged, two open: the slowest open decides the stage",
			kids,
			[]forge.MR{mr("api", 1, merged), mr("docs", 1, draft), mr("infra", 1, ready)},
			nil, StageDraft, CourtYou, "docs!1 · draft MR · mark ready · +1 more",
		},
		{
			"one merged, one open, one child without MR: not merged",
			kids,
			[]forge.MR{mr("api", 1, merged), mr("docs", 1, ready)},
			nil, StageStarted, CourtYou, "docs!1 · ready to merge · +1 more",
		},
		{
			"child without an MR ranks below an MR I can act on",
			kids,
			[]forge.MR{mr("api", 1, draft), mr("docs", 1, ready)},
			nil, StageStarted, CourtYou, "api!1 · draft MR · mark ready · +2 more",
		},
		{
			"child without an MR outranks waiting",
			kids,
			[]forge.MR{mr("api", 1, running), mr("docs", 1, running)},
			[]string{"3-x"},
			StageStarted, CourtYou, "infra#3 · no MR yet",
		},
		{
			"one merged, two children without an MR",
			kids,
			[]forge.MR{mr("api", 1, merged)},
			nil, StageStarted, CourtYou, "docs#2 · no MR yet · +1 more",
		},
		{
			"branches and no MR anywhere: branch",
			kids, nil,
			[]string{"2-x"},
			StageBranch, CourtYou, "branch pushed · open MR",
		},
		{
			"branches once another child has its MR: started",
			kids,
			[]forge.MR{mr("api", 1, merged)},
			[]string{"2-x"},
			StageStarted, CourtYou, "docs#2 · no MR yet · +1 more",
		},
		{
			"no MR on any child",
			kids, nil,
			nil, StageStarted, CourtYou, "api#1 · no MR yet · +2 more",
		},
		{
			"all merged",
			kids,
			[]forge.MR{mr("api", 1, merged), mr("docs", 1, merged), mr("infra", 1, merged)},
			nil, StageMerged, CourtYou, "close tracker",
		},
		{
			"closed child without MR is ignored",
			[]forge.Issue{child("api", 1, 1), withState(child("docs", 2, 1), "closed")},
			[]forge.MR{mr("api", 1, merged)},
			nil, StageMerged, CourtYou, "close tracker",
		},
		{
			"child of someone else: theirs to open, not in my court",
			[]forge.Issue{child("api", 1, 1), withAssignee(child("docs", 2, 1), "sam")},
			[]forge.MR{mr("api", 1, merged)},
			nil, StageStarted, CourtNobody, "docs#2 · no MR yet · sam",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var links []forge.Link
			for _, k := range tt.kids {
				links = append(links, link(k))
			}
			got := Assemble(AssembleInput{
				Manifest: sandboxManifest(), Me: me, Tracker: tracker(1, "x", true, "opened", "type::story"),
				Links: links, Candidates: tt.kids, MRs: tt.mrs, Branches: tt.branches,
			})
			if got.Stage != tt.stage || got.Court != tt.court || got.Reason != tt.reason {
				t.Errorf("stage/court/reason = %v/%v/%q, want %v/%v/%q", got.Stage, got.Court, got.Reason, tt.stage, tt.court, tt.reason)
			}
		})
	}
}

func TestSeveralChildrenAllMergedNamesTheDoneLabel(t *testing.T) {
	m := sandboxManifest()
	m.Labels.Workflow.Done = "done"
	kids := []forge.Issue{child("api", 1, 1), child("docs", 2, 1), child("infra", 3, 1)}
	merged := func(x *forge.MR) { x.State = "merged" }
	got := Assemble(AssembleInput{
		Manifest: m, Me: me, Tracker: tracker(1, "x", true, "opened", "type::story", "workflow::in review"),
		Links:      []forge.Link{link(kids[0]), link(kids[1]), link(kids[2])},
		Candidates: kids,
		MRs:        []forge.MR{mr("api", 1, merged), mr("docs", 1, merged), mr("infra", 1, merged)},
	})
	if got.Stage != StageMerged || got.Court != CourtYou || got.Reason != "set workflow::done" {
		t.Errorf("stage/court/reason = %v/%v/%q", got.Stage, got.Court, got.Reason)
	}
}

func TestUpstreamOnlyWhenTheUpstreamWaitIsChosen(t *testing.T) {
	apiKid, webKid, docsKid := child("api", 1, 1), child("web", 1, 1), child("docs", 1, 1)
	failed := func(m *forge.MR) { m.Pipeline = &forge.Pipeline{Status: "failed"} }
	tests := []struct {
		name   string
		mrs    []forge.MR
		court  Court
		reason string
		up     bool
	}{
		{"waits only", []forge.MR{mr("api", 1, nil), mr("web", 1, nil), mr("docs", 1, func(m *forge.MR) { m.Pipeline = &forge.Pipeline{Status: "running"} })}, CourtUpstream, "waits on " + grp + "/api!1", true},
		{"a failure elsewhere takes the court", []forge.MR{mr("api", 1, nil), mr("web", 1, nil), mr("docs", 1, failed)}, CourtYou, "docs!1 · pipeline failed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Assemble(AssembleInput{
				Manifest: sandboxManifest(), Me: me, Tracker: tracker(1, "x", true, "opened", "type::story"),
				Links:      []forge.Link{link(apiKid), link(webKid), link(docsKid)},
				Candidates: []forge.Issue{apiKid, webKid, docsKid}, MRs: tt.mrs,
			})
			if got.Court != tt.court || got.Reason != tt.reason || (got.Upstream != nil) != tt.up {
				t.Errorf("court/reason/upstream = %v/%q/%v, want %v/%q/%v", got.Court, got.Reason, got.Upstream, tt.court, tt.reason, tt.up)
			}
		})
	}
}

func TestMRsByChild(t *testing.T) {
	a, b := child("api", 1, 1), child("api", 2, 1)
	wi := WorkItem{
		Children: []forge.Issue{a, b, child("web", 3, 1)},
		MRs: []forge.MR{
			mr("api", 10, func(m *forge.MR) { m.SourceBranch = "2-fix" }),
			mr("api", 11, nil), // names no child: the repo's first
			mr("web", 12, nil),
			mr("docs", 13, nil), // no child in docs
		},
	}
	got := wi.MRsByChild()
	if len(got) != 4 {
		t.Fatalf("groups = %d, want 4", len(got))
	}
	want := [][]int{{11}, {10}, {12}, {13}}
	for i, g := range got {
		var iids []int
		for _, m := range g.MRs {
			iids = append(iids, m.Ref.IID)
		}
		if len(iids) != len(want[i]) || iids[0] != want[i][0] {
			t.Errorf("group %d = %v, want %v", i, iids, want[i])
		}
	}
	if got[3].Child != nil {
		t.Errorf("last group has child %v, want none", got[3].Child.Ref)
	}
}

func withState(is forge.Issue, state string) forge.Issue {
	is.State = state
	return is
}

func withAssignee(is forge.Issue, user string) forge.Issue {
	is.Assignees = []forge.User{{Username: user}}
	return is
}
