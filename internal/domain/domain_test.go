package domain

import (
	"reflect"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
)

var sandboxLabels = config.Labels{
	Type: config.LabelFacet{Pattern: "type::{value}"},
	App:  config.LabelFacet{Pattern: "app::{value}"},
	Workflow: config.WorkflowFacet{
		Pattern: "workflow::{value}",
		Stage:   map[string]string{"started": "in progress", "draft": "in progress", "review": "in review", "merged": "done"},
	},
}

func sandboxManifest() *config.Manifest {
	return &config.Manifest{
		Name:    "demo-gl",
		Tracker: config.Tracker{Project: grp + "/tracker", Iterations: config.IterationsSource{Source: "milestones"}},
		Labels:  sandboxLabels,
		Repos: map[string]config.Repo{
			"api": {Path: grp + "/api", App: []string{"api"}},
			"web": {Path: grp + "/web", App: []string{"web"}, Requires: []string{"api"}},
		},
	}
}

func TestParseLabels(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		typ      string
		apps     []string
		workflow string
	}{
		{"sandbox", []string{"type::story", "app::api", "workflow::in review"}, "story", []string{"api"}, "in review"},
		{"multi app sorted", []string{"app::web", "app::api", "type::bug"}, "bug", []string{"api", "web"}, ""},
		{"none", []string{"unrelated"}, "", nil, ""},
		{"case insensitive prefix", []string{"Type::Story"}, "Story", nil, ""},
		{"empty value ignored", []string{"type::"}, "", nil, ""},
		{"most advanced workflow wins", []string{"workflow::in progress", "workflow::in review"}, "", nil, "in review"},
		{"done beats blocked (unmapped)", []string{"workflow::blocked", "workflow::done"}, "", nil, "done"},
		{"unmapped only: lexical last", []string{"workflow::a", "workflow::b"}, "", nil, "b"},
		{"mapped beats unmapped", []string{"workflow::zzz", "workflow::in progress"}, "", nil, "in progress"},
		{"several types: lexical first", []string{"type::story", "type::bug"}, "bug", nil, ""},
		{"duplicate apps collapse", []string{"app::api", "APP::api"}, "", []string{"api"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typ, apps, wf := ParseLabels(tt.labels, sandboxLabels)
			if typ != tt.typ || !reflect.DeepEqual(apps, tt.apps) || wf != tt.workflow {
				t.Errorf("got (%q, %v, %q), want (%q, %v, %q)", typ, apps, wf, tt.typ, tt.apps, tt.workflow)
			}
		})
	}
}

func TestParseLabelsAffixPatterns(t *testing.T) {
	l := config.Labels{Type: config.LabelFacet{Pattern: "[{value}]-kind"}}
	typ, _, _ := ParseLabels([]string{"[bug]-kind", "bug"}, l)
	if typ != "bug" {
		t.Errorf("type = %q, want bug", typ)
	}
	if typ, _, _ = ParseLabels([]string{"[bug]"}, l); typ != "" {
		t.Errorf("suffix mismatch matched: %q", typ)
	}
	if typ, _, _ = ParseLabels([]string{"type::x"}, config.Labels{}); typ != "" {
		t.Errorf("empty pattern matched: %q", typ)
	}
}

func intp(n int) *int { return &n }

func TestResolveWeight(t *testing.T) {
	tests := []struct {
		name  string
		issue forge.Issue
		src   config.WeightSource
		want  *int
	}{
		{"gitlab field", forge.Issue{Weight: intp(3)}, config.WeightSource{Source: "gitlab"}, intp(3)},
		{"gitlab ignores labels", forge.Issue{Labels: []string{"weight::5"}}, config.WeightSource{Source: "gitlab"}, nil},
		{"gitlab zero is unweighted", forge.Issue{Weight: intp(0)}, config.WeightSource{Source: "gitlab"}, nil},
		{"label", forge.Issue{Labels: []string{"weight::5"}}, config.WeightSource{Source: "label", Pattern: "weight::{value}"}, intp(5)},
		{"label default pattern", forge.Issue{Labels: []string{"weight::8"}}, config.WeightSource{Source: "label"}, intp(8)},
		{"label max of many", forge.Issue{Labels: []string{"weight::2", "weight::13", "weight::5"}}, config.WeightSource{Source: "label"}, intp(13)},
		{"label custom pattern", forge.Issue{Labels: []string{"sp-3"}}, config.WeightSource{Source: "label", Pattern: "sp-{value}"}, intp(3)},
		{"label non-numeric", forge.Issue{Labels: []string{"weight::big"}}, config.WeightSource{Source: "label"}, nil},
		{"label zero never pointer", forge.Issue{Labels: []string{"weight::0"}}, config.WeightSource{Source: "label"}, nil},
		{"label source ignores field", forge.Issue{Weight: intp(2), Labels: []string{"weight::9"}}, config.WeightSource{Source: "label"}, intp(9)},
		{"none", forge.Issue{Weight: intp(3), Labels: []string{"weight::5"}}, config.WeightSource{Source: "none"}, nil},
		{"auto prefers field", forge.Issue{Weight: intp(3), Labels: []string{"weight::5"}}, config.WeightSource{Source: "auto"}, intp(3)},
		{"auto falls back to label", forge.Issue{Labels: []string{"weight::5"}}, config.WeightSource{Source: "auto"}, intp(5)},
		{"empty source is auto", forge.Issue{Labels: []string{"weight::5"}}, config.WeightSource{}, intp(5)},
		{"auto unweighted", forge.Issue{}, config.WeightSource{Source: "auto"}, nil},
		{"github field", forge.Issue{Weight: intp(4)}, config.WeightSource{Source: "github-field", Field: "Estimate"}, intp(4)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveWeight(tt.issue, tt.src)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", deref(got), deref(tt.want))
			}
		})
	}
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestResolveWeightDoesNotAlias(t *testing.T) {
	w := 3
	got := ResolveWeight(forge.Issue{Weight: &w}, config.WeightSource{Source: "gitlab"})
	*got = 99
	if w != 3 {
		t.Error("result aliases the issue's weight")
	}
}

func ref(s string) forge.Ref {
	r, err := forge.ParseRef(s)
	if err != nil {
		panic(err)
	}
	return r
}

func TestParentRefs(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want []string
	}{
		{"parent line", "Parent: ade-demo-env/tracker#3\n\nImplementation issue.", []string{"ade-demo-env/tracker#3"}},
		{"part of with period", "Closes #1\n\nPart of ade-demo-env/tracker#1.", []string{"ade-demo-env/tracker#1"}},
		{"lowercase and bold", "**parent:** g/sub/tracker#12", []string{"g/sub/tracker#12"}},
		{"full url", "Parent: https://gitlab.com/ade-demo-env/tracker/-/issues/7", []string{"ade-demo-env/tracker#7"}},
		{"url without dash", "Part of https://git.example.com/a/b/issues/4", []string{"a/b#4"}},
		{"several, deduped", "Parent: a/b#1\nPart of a/b#1\nParent: a/c#2", []string{"a/b#1", "a/c#2"}},
		{"bare ref is not a parent", "See a/b#1 for context", nil},
		{"parent without ref", "Parent: none yet", nil},
		{"unrelated text", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var want []forge.Ref
			for _, s := range tt.want {
				want = append(want, ref(s))
			}
			got := ParentRefs(forge.Issue{Description: tt.desc})
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func TestIsChildLink(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{"relates_to", true}, {"sub_issue", true}, {"blocks", false}, {"is_blocked_by", false}, {"parent", false},
	}
	for _, tt := range tests {
		if got := IsChildLink(forge.Link{Ref: ref("a/b#1"), Type: tt.typ}); got != tt.want {
			t.Errorf("%s: got %v", tt.typ, got)
		}
	}
	if IsChildLink(forge.Link{Ref: ref("a/b!1"), Type: "relates_to"}) {
		t.Error("an MR ref is not a child issue")
	}
}

func TestChildren(t *testing.T) {
	tracker := forge.Issue{Ref: ref("g/tracker#1")}
	linked := forge.Issue{Ref: ref("g/api#1")}
	byDesc := forge.Issue{Ref: ref("g/web#1"), Description: "Parent: g/tracker#1"}
	both := forge.Issue{Ref: ref("g/api#2"), Description: "Parent: g/tracker#1"}
	other := forge.Issue{Ref: ref("g/api#3"), Description: "Parent: g/tracker#9"}
	links := []forge.Link{
		{Ref: linked.Ref, Type: "relates_to"},
		{Ref: both.Ref, Type: "relates_to"},
		{Ref: other.Ref, Type: "blocks"},
	}
	got := children(tracker, links, []forge.Issue{linked, byDesc, both, both, other, tracker})
	want := []forge.Issue{linked, byDesc, both}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func d(s string) forge.Date {
	v, err := forge.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

func TestWantWorkflow(t *testing.T) {
	tests := []struct {
		stage Stage
		want  string
	}{
		{StageStarted, "in progress"},
		{StageDraft, "in progress"},
		{StageReview, "in review"},
		{StageMerged, "done"},
		{StageAssigned, ""},
		{StageCI, ""},
		{StageDone, ""},
	}
	for _, tt := range tests {
		if got := WantWorkflow(tt.stage, sandboxLabels); got != tt.want {
			t.Errorf("%v: got %q, want %q", tt.stage, got, tt.want)
		}
	}
}
