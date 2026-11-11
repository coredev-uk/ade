package startwork_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/startwork"
)

func TestParseQuickActions(t *testing.T) {
	body := "## Summary\n\n/assign @a @b\n/assign_reviewer @rae @sam\n/label ~\"type::story\" ~bug\n/usr/bin/env is prose\n" +
		"```\n/assign @in-fence\n```\n/draft\n"
	qas := startwork.ParseQuickActions(body)
	var got []string
	for _, q := range qas {
		got = append(got, q.Summary)
	}
	want := []string{
		"assign @a @b",
		"request review from @rae @sam",
		"add labels ~type::story ~bug",
		"mark draft",
	}
	if !slices.Equal(got, want) {
		t.Errorf("summaries = %q\nwant       %q", got, want)
	}
	if !slices.Equal(qas[1].People, []string{"@rae", "@sam"}) {
		t.Errorf("reviewers = %v", qas[1].People)
	}
}

func TestStripQuickActions(t *testing.T) {
	body := "## Summary\n\n/assign @a\n/assign_reviewer @rae\n/label ~x\n\n```\n/assign @kept\n```\n"
	for _, tc := range []struct {
		names   []string
		want    string
		removed []string
		kept    []string
	}{
		{nil, body, nil, []string{"/assign @a", "/assign_reviewer", "/label"}},
		{[]string{"assign_reviewer"}, "", []string{"/assign_reviewer"}, []string{"/assign @a", "/label ~x", "/assign @kept"}},
		{[]string{"/assign", "label"}, "", []string{"/assign @a", "/label"}, []string{"/assign_reviewer", "/assign @kept"}},
		{[]string{"*"}, "", []string{"/assign @a", "/assign_reviewer", "/label"}, []string{"## Summary", "/assign @kept"}},
	} {
		got := startwork.StripQuickActions(body, tc.names)
		if tc.want != "" && got != tc.want {
			t.Errorf("%v: got %q", tc.names, got)
		}
		for _, r := range tc.removed {
			if strings.Contains(got, r) {
				t.Errorf("%v: %q still present:\n%s", tc.names, r, got)
			}
		}
		for _, k := range tc.kept {
			if !strings.Contains(got, k) {
				t.Errorf("%v: %q was removed:\n%s", tc.names, k, got)
			}
		}
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Rate-limit the export endpoint":    "rate-limit-the-export-endpoint",
		"  Fix: ünïcode & symbols!! (v2)  ": "fix-n-code-symbols-v2",
		"":                                  "",
		"a/b\\c":                            "a-b-c",
		strings.Repeat("word ", 30):         strings.TrimRight(strings.Repeat("word-", 10), "-"),
		strings.Repeat("x", 80):             strings.Repeat("x", 50),
	} {
		if got := startwork.Slug(in, 50); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func manifest(t *testing.T, toml string) *config.Manifest {
	t.Helper()
	m, err := config.ParseManifest([]byte(toml), "test")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBranchNameFollowsTheManifestConvention(t *testing.T) {
	item := domain.WorkItem{Ref: issueRef(tracker, 128), Type: "bug"}
	for _, tc := range []struct {
		conv string
		iid  int
		want string
	}{
		{"", 412, "412-rate-limit-export"},
		{"{type}/{child_iid}-{slug}", 412, "bug/412-rate-limit-export"},
		{"feature-{parent_iid}-{child_iid}", 412, "feature-128-412"},
		{"{child_iid}-{slug}", 0, startwork.ChildIIDPlaceholder + "-rate-limit-export"}, // child not created yet
		{"{nope}-{slug}", 7, "{nope}-rate-limit-export"},                                // typos stay visible
	} {
		m := &config.Manifest{Conventions: config.Conventions{Branch: tc.conv}}
		if got := startwork.BranchName(m, "Rate-limit export", item, tc.iid); got != tc.want {
			t.Errorf("convention %q: %q, want %q", tc.conv, got, tc.want)
		}
	}
}

func TestMRBodyRendering(t *testing.T) {
	m := manifest(t, manifestTOML)
	item := domain.WorkItem{Ref: issueRef(tracker, 3), Issue: forge.Issue{Ref: issueRef(tracker, 3), Title: "T"}}
	found := map[string]startwork.Found{"api": {TemplateName: "Default", TemplateBody: "## What\n/assign @a\n"}}
	plan := func(conv config.MRConv, strip []string, body string) startwork.TargetPlan {
		m.Conventions.MR = conv
		p, err := startwork.BuildPlan(startwork.PlanInput{
			Item: item, Manifest: m, Root: "/r", Found: found,
			Choices: startwork.Choices{Repos: []string{"api"}, Mode: startwork.ModeDraft, StripActions: strip, MRBody: body},
		})
		if err != nil {
			t.Fatal(err)
		}
		return p.Targets[0]
	}
	child := issueRef(apiProj, 9)
	trk := issueRef(tracker, 3)

	tp := plan(config.MRConv{Closes: "child", Mention: "parent"}, nil, "")
	if got := tp.MRBody(child, trk); got != "Closes #9\n\nPart of "+tracker+"#3\n\n## What\n/assign @a\n" {
		t.Errorf("body = %q", got)
	}
	if got := tp.MRBody(forge.Ref{}, trk); !strings.HasPrefix(got, "Closes "+startwork.ChildPlaceholder+"\n") {
		t.Errorf("preview before the child exists = %q", got)
	}
	if got := plan(config.MRConv{Closes: "child", Mention: "parent"}, []string{"*"}, "").MRBody(child, trk); strings.Contains(got, "/assign") || !strings.Contains(got, "## What") {
		t.Errorf("stripped body = %q", got)
	}
	// closes = parent: the MR closes the tracker issue from another project.
	if got := plan(config.MRConv{Closes: "parent", Mention: "none"}, nil, "").MRBody(child, trk); !strings.HasPrefix(got, "Closes "+tracker+"#3\n") || strings.Contains(got, "Part of") {
		t.Errorf("closes=parent body = %q", got)
	}
	// closes = none writes no Closes line.
	if got := plan(config.MRConv{Closes: "none", Mention: "parent"}, nil, "").MRBody(child, trk); strings.Contains(got, "Closes") {
		t.Errorf("closes=none body = %q", got)
	}
	// An edited body wins and gets the child's reference.
	if got := plan(config.MRConv{}, nil, "x "+startwork.ChildPlaceholder).MRBody(child, trk); got != "x #9" {
		t.Errorf("edited body = %q", got)
	}
}

func TestPlanStepsPerMode(t *testing.T) {
	m := manifest(t, manifestTOML)
	item := domain.WorkItem{
		Ref: issueRef(tracker, 3), Type: "story",
		Issue: forge.Issue{Ref: issueRef(tracker, 3), Title: "Retry deliveries", Labels: []string{"type::story", "app::api"}},
	}
	kinds := func(mode startwork.Mode) (kinds []startwork.StepKind, writes int) {
		p, err := startwork.BuildPlan(startwork.PlanInput{
			Item: item, Manifest: m, Root: "/r",
			Choices: startwork.Choices{Repos: []string{"api"}, Mode: mode, Child: startwork.ChildAuto},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range p.Steps {
			kinds = append(kinds, s.Kind)
		}
		return kinds, p.Writes()
	}
	k, w := kinds(startwork.ModeDraft)
	if want := []startwork.StepKind{"child", "workflow", "link", "branch", "mr", "worktree", "workspace"}; !slices.Equal(k, want) || w != 5 {
		t.Errorf("draft steps = %v (%d writes), want %v (5)", k, w, want)
	}
	k, w = kinds(startwork.ModeBranch)
	if want := []startwork.StepKind{"child", "workflow", "link", "branch", "worktree", "workspace"}; !slices.Equal(k, want) || w != 4 {
		t.Errorf("branch steps = %v (%d writes), want %v (4)", k, w, want)
	}
	k, w = kinds(startwork.ModeBrowse)
	if want := []startwork.StepKind{"worktree", "workspace"}; !slices.Equal(k, want) || w != 0 {
		t.Errorf("browse steps = %v (%d writes), want %v (0)", k, w, want)
	}
}

func TestLabelMoves(t *testing.T) {
	m := manifest(t, manifestTOML)
	mk := func(stage domain.Stage, wf string, labels ...string) domain.WorkItem {
		return domain.WorkItem{
			Ref: issueRef(tracker, 3), Stage: stage, Workflow: wf, WantWorkflow: domain.WantWorkflow(stage, m.Labels),
			Issue: forge.Issue{Ref: issueRef(tracker, 3), Labels: labels},
		}
	}
	for _, tc := range []struct {
		name      string
		prev, cur domain.WorkItem
		sync      string
		want      *startwork.Move
	}{
		{
			"stage advanced, label stale",
			mk(domain.StageDraft, "in progress", "workflow::in progress"), mk(domain.StageReview, "in progress", "workflow::in progress"), "",
			&startwork.Move{To: "in review", Add: "workflow::in review", Remove: []string{"workflow::in progress"}},
		},
		{
			"old label removed explicitly even when several coexist",
			mk(domain.StageDraft, "in progress"), mk(domain.StageReview, "in progress", "workflow::in progress", "workflow::backlog"), "",
			&startwork.Move{To: "in review", Add: "workflow::in review", Remove: []string{"workflow::in progress", "workflow::backlog"}},
		},
		{
			"no edge, no move: a hand-set label stays until the next transition",
			mk(domain.StageReview, "in progress"), mk(domain.StageReview, "in progress", "workflow::in progress"), "", nil,
		},
		{"label already right", mk(domain.StageDraft, "in progress"), mk(domain.StageReview, "in review", "workflow::in review"), "", nil},
		{
			"done label is never moved back",
			mk(domain.StageDraft, "done"), mk(domain.StageReview, "done", "workflow::done"), "", nil,
		},
		{
			"deliberate blocked stays",
			mk(domain.StageDraft, "blocked"), mk(domain.StageReview, "blocked", "workflow::blocked"), "", nil,
		},
		{
			"closed tracker is not moved",
			mk(domain.StageMerged, "done"), mk(domain.StageDone, "in review", "workflow::in review"), "", nil,
		},
		{
			"kept-open done item is finished, not drifting",
			mk(domain.StageDraft, "done"), keptOpen(mk(domain.StageDone, "done", "workflow::done")), "", nil,
		},
		{"sync off", mk(domain.StageDraft, "in progress"), mk(domain.StageReview, "in progress", "workflow::in progress"), "off", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.Labels.Workflow.Sync = cmpOr(tc.sync, "auto")
			got := startwork.LabelMoves([]domain.WorkItem{tc.prev}, []domain.WorkItem{tc.cur}, m)
			if tc.want == nil {
				if len(got) != 0 {
					t.Fatalf("moves = %+v, want none", got)
				}
				return
			}
			if len(got) != 1 || got[0].To != tc.want.To || got[0].Add != tc.want.Add || !slices.Equal(got[0].Remove, tc.want.Remove) {
				t.Fatalf("moves = %+v, want %+v", got, *tc.want)
			}
		})
	}
	// A new item (no previous sync) never moves: there is no edge yet.
	cur := mk(domain.StageReview, "in progress", "workflow::in progress")
	if got := startwork.LabelMoves(nil, []domain.WorkItem{cur}, m); len(got) != 0 {
		t.Errorf("first sight moved: %+v", got)
	}
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func keptOpen(it domain.WorkItem) domain.WorkItem {
	it.KeptOpen = true
	return it
}

func TestMRDraftFollowsTheManifestAndDefaultsToDraft(t *testing.T) {
	item := domain.WorkItem{
		Ref: issueRef(tracker, 3), Type: "story",
		Issue: forge.Issue{Ref: issueRef(tracker, 3), Title: "Retry deliveries", Labels: []string{"type::story", "app::api"}},
	}
	for _, tt := range []struct {
		name, mr  string
		wantDraft bool
		wantText  string
	}{
		{"explicit false", `mr = { draft = false, closes = "child" }`, false, "open MR "},
		{"explicit true", `mr = { draft = true, closes = "child" }`, true, "open draft MR "},
		{"unset", `mr = { closes = "child" }`, true, "open draft MR "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			toml := strings.Replace(manifestTOML, `mr     = { draft = true, closes = "child", mention = "parent" }`, tt.mr, 1)
			if toml == manifestTOML && tt.name != "explicit true" {
				t.Fatal("fixture manifest has no mr line to replace")
			}
			p, err := startwork.BuildPlan(startwork.PlanInput{
				Item: item, Manifest: manifest(t, toml), Root: "/r",
				Choices: startwork.Choices{Repos: []string{"api"}, Mode: startwork.ModeDraft, Child: startwork.ChildAuto},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Targets[0].MR.Draft; got != tt.wantDraft {
				t.Errorf("MR.Draft = %v, want %v", got, tt.wantDraft)
			}
			var text string
			for _, s := range p.Steps {
				if s.Kind == startwork.StepMR {
					text = s.Text
				}
			}
			if !strings.HasPrefix(text, tt.wantText) {
				t.Errorf("MR step %q, want prefix %q", text, tt.wantText)
			}
		})
	}
}
