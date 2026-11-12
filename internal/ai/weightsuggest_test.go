package ai

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

var weightLabels = config.Labels{
	Type: config.LabelFacet{Pattern: "type::{value}"},
	App:  config.LabelFacet{Pattern: "app::{value}"},
}

func sample(iid int, title, typ string, w int, apps ...string) WeightSample {
	return WeightSample{Ref: forge.Ref{Project: "g/tracker", IID: iid, Kind: forge.KindIssue}, Title: title, Type: typ, Apps: apps, Weight: w}
}

func newIssue(title string, labels ...string) forge.Issue {
	return forge.Issue{Ref: forge.Ref{Project: "g/tracker", IID: 50, Kind: forge.KindIssue}, Title: title, Labels: labels}
}

func TestSuggestWeightIsTheMedianOfSimilarClosedIssues(t *testing.T) {
	samples := []WeightSample{
		sample(1, "Export timestamps ignore tenant timezone", "bug", 3, "api"),
		sample(2, "Export timezone offsets drift", "bug", 2, "api"),
		sample(3, "Tenant timezone export header", "bug", 5, "api"),
		sample(4, "Export preview shows stale tenant timezone", "bug", 1, "web"), // other app
		sample(5, "Export tenant timezone dark mode", "story", 8, "api"),         // other type
		sample(6, "Rotate credentials quarterly", "bug", 13, "api"),              // no shared word
		sample(7, "Export tenant timezone offsets again", "bug", 0, "api"),       // unweighted never counts
	}
	issue := newIssue("Exports break for tenants in a non-UTC timezone", "type::bug", "app::api")
	w, why := SuggestWeight(issue, weightLabels, config.WeightSource{Source: "label"}, Suggestion{}, samples)
	if w == nil || *w != 3 {
		t.Fatalf("weight = %v (%s), want 3 from weights 3, 2 and 5", w, why)
	}
	if !strings.HasPrefix(why, "weighted median of 3 similar closed issues: g/tracker#1 (3)") || strings.Contains(why, "#5") {
		t.Errorf("reason = %q", why)
	}
	// Deterministic: same input, same answer, whatever the order.
	slicesReverse(samples)
	if w2, why2 := SuggestWeight(issue, weightLabels, config.WeightSource{Source: "label"}, Suggestion{}, samples); *w2 != *w || why2 != why {
		t.Errorf("not order independent: %d %q vs %d %q", *w2, why2, *w, why)
	}
}

// "Adds" and "fixes" are the stop words "add" and "fix" in another form.
func TestTitleTokensDropInflectedStopWords(t *testing.T) {
	got := titleTokens("Adds retries; fixes the Pages for updates to Exports, new bugs")
	want := map[string]bool{"retry": true, "export": true}
	if len(got) != len(want) || !got["retry"] || !got["export"] {
		t.Errorf("tokens = %v, want %v", got, want)
	}
}

// One word every sample shares says little; two rare ones say a lot.
func TestSuggestWeightRanksBySharedRareWords(t *testing.T) {
	src := config.WeightSource{Source: "label"}
	samples := []WeightSample{
		sample(1, "Export invoice pdf generation", "bug", 8, "api"),
		sample(2, "Export users csv", "bug", 1, "api"),
		sample(3, "Export audit log csv", "bug", 1, "api"),
		sample(4, "Export orders csv", "bug", 1, "api"),
		sample(5, "Export refunds csv", "bug", 1, "api"),
	}
	issue := newIssue("Invoice pdf generation fails on export", "type::bug", "app::api")
	w, why := SuggestWeight(issue, weightLabels, src, Suggestion{}, samples)
	if w == nil || *w != 8 {
		t.Fatalf("weight = %v (%s), want 8 from the one near-duplicate, not the generic export matches", w, why)
	}
	if !strings.HasPrefix(why, "only one similar issue: g/tracker#1 (8)") {
		t.Errorf("reason = %q", why)
	}
}

func TestSuggestWeightPicksTheSideNearerTheWeightedMeanOnEvenSplits(t *testing.T) {
	src := config.WeightSource{Source: "label"}
	issue := newIssue("Tenant timezone export", "type::bug", "app::api")
	same := "Tenant timezone export"
	tests := []struct {
		name    string
		samples []WeightSample
		want    int
	}{
		{"equal scores, tie goes to the best match's side", []WeightSample{sample(1, same, "bug", 2, "api"), sample(2, same, "bug", 8, "api")}, 2},
		{"a closer match outweighs a looser one", []WeightSample{sample(1, "Tenant export", "bug", 2, "api"), sample(2, same, "bug", 8, "api")}, 8},
		{"four samples, mean nearer the upper middle", []WeightSample{
			sample(1, same, "bug", 1, "api"), sample(2, same, "bug", 5, "api"), sample(3, same, "bug", 8, "api"), sample(4, same, "bug", 13, "api"),
		}, 8},
		{"four samples, mean nearer the lower middle", []WeightSample{
			sample(1, same, "bug", 1, "api"), sample(2, same, "bug", 2, "api"), sample(3, same, "bug", 5, "api"), sample(4, same, "bug", 5, "api"),
		}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, why := SuggestWeight(issue, weightLabels, src, Suggestion{}, tt.samples)
			if w == nil || *w != tt.want {
				t.Errorf("weight = %v (%s), want %d", w, why, tt.want)
			}
		})
	}
}

func TestSuggestWeightKeepsTheNearestFiveAndListsThree(t *testing.T) {
	src := config.WeightSource{Source: "label"}
	var samples []WeightSample
	for i := 1; i <= 7; i++ {
		samples = append(samples, sample(i, "Tenant timezone export", "bug", 3, "api"))
	}
	w, why := SuggestWeight(newIssue("Tenant timezone export", "type::bug", "app::api"), weightLabels, src, Suggestion{}, samples)
	want := "weighted median of the 5 nearest of 7 similar closed issues: g/tracker#1 (3), g/tracker#2 (3), g/tracker#3 (3)"
	if w == nil || *w != 3 || why != want {
		t.Errorf("got %v %q, want 3 %q", w, why, want)
	}
}

func slicesReverse(s []WeightSample) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func TestSuggestWeightEdgeCases(t *testing.T) {
	src := config.WeightSource{Source: "label"}
	samples := []WeightSample{sample(1, "Export timezone fix", "bug", 3, "api")}

	// An already weighted issue, or a project without weights, gets nothing.
	if w, why := SuggestWeight(newIssue("Export timezone", "type::bug", "app::api", "weight::5"), weightLabels, src, Suggestion{}, samples); w != nil || why != "" {
		t.Errorf("weighted issue suggested %v %q", w, why)
	}
	if w, _ := SuggestWeight(newIssue("Export timezone", "type::bug", "app::api"), weightLabels, config.WeightSource{Source: "none"}, Suggestion{}, samples); w != nil {
		t.Error("source none suggested a weight")
	}
	// Neither type nor app known: nothing to compare, and it says so.
	if w, why := SuggestWeight(newIssue("Export timezone"), weightLabels, src, Suggestion{}, samples); w != nil || !strings.Contains(why, "no type or app") {
		t.Errorf("unclassified: %v %q", w, why)
	}
	// The suggested facets stand in for missing labels.
	if w, _ := SuggestWeight(newIssue("Export timezone"), weightLabels, src, Suggestion{Type: "bug", Apps: []string{"api"}}, samples); w == nil || *w != 3 {
		t.Errorf("suggested facets ignored: %v", w)
	}
	// A closed issue never compares with itself.
	self := sample(50, "Export timezone", "bug", 8, "api")
	if w, _ := SuggestWeight(newIssue("Export timezone", "type::bug", "app::api"), weightLabels, src, Suggestion{}, []WeightSample{self}); w != nil {
		t.Error("compared the issue with itself")
	}
	// A single shared generic word is not similar enough.
	if w, _ := SuggestWeight(newIssue("Export billing", "type::bug", "app::api"), weightLabels, src, Suggestion{}, []WeightSample{sample(1, "Export timezone", "bug", 3, "api"), sample(2, "Export users", "bug", 2, "api")}); w != nil {
		t.Errorf("suggested %d from a common word alone", *w)
	}
	// No comparable issue: no weight, with a reason.
	if w, why := SuggestWeight(newIssue("Billing rounding", "type::bug", "app::api"), weightLabels, src, Suggestion{}, samples); w != nil || why == "" {
		t.Errorf("no match: %v %q", w, why)
	}
}

func TestWeightSamplesTakeClosedWeightedItemsOnly(t *testing.T) {
	three, five := 3, 5
	closed := forge.Issue{State: "closed", Title: "Done one"}
	items := []domain.WorkItem{
		{Ref: forge.Ref{Project: "g/t", IID: 1}, Issue: closed, Weight: &three, Type: "bug", Apps: []string{"api"}},
		{Ref: forge.Ref{Project: "g/t", IID: 2}, Issue: forge.Issue{State: "opened"}, Weight: &five},                          // open
		{Ref: forge.Ref{Project: "g/t", IID: 3}, Issue: forge.Issue{State: "opened"}, Weight: &five, Stage: domain.StageDone}, // kept open, finished by label
		{Ref: forge.Ref{Project: "g/t", IID: 4}, Issue: closed},                                                               // unweighted
	}
	got := WeightSamples(items)
	if len(got) != 2 || got[0].Weight != 3 || got[0].Type != "bug" || got[1].Ref.IID != 3 {
		t.Errorf("samples = %+v", got)
	}
}

func TestClassifySuggestsAWeightEvenWithEveryFacetPresent(t *testing.T) {
	e := newEnv(t)
	in := ClassifyInput{
		Issue: newIssue("Export timezone offsets", "type::bug", "app::api"), Labels: weightLabels,
		Weight: config.WeightSource{Source: "label"}, Samples: []WeightSample{sample(1, "Export timezone fix", "bug", 3, "api")},
	}
	// Keywords path (no AI call, no cost) and the AI path agree on the weight.
	for _, noAI := range []bool{true, false} {
		cr, err := e.svc.Classify(context.Background(), "sandbox", in, ClassifyOpts{NoAI: noAI, Assume: true})
		if err != nil {
			t.Fatalf("noAI=%v: %v", noAI, err)
		}
		if cr.Suggestion.Weight == nil || *cr.Suggestion.Weight != 3 || len(cr.Suggestion.Labels) != 0 || cr.Run != nil {
			t.Errorf("noAI=%v: %+v (run %v): want weight 3, no labels, no model call", noAI, cr.Suggestion, cr.Run)
		}
	}
	if e.drv.calls != 0 {
		t.Errorf("the model was called %d times with nothing to classify", e.drv.calls)
	}
	// Weighted and complete: nothing to do at all.
	in.Issue.Labels = append(in.Issue.Labels, "weight::2")
	if _, err := e.svc.Classify(context.Background(), "sandbox", in, ClassifyOpts{NoAI: true}); err != ErrNothingToClassify {
		t.Errorf("err = %v, want ErrNothingToClassify", err)
	}
}

func TestClassifyAIPathAddsTheDeterministicWeight(t *testing.T) {
	e := newEnv(t)
	e.drv.resp.Text = `{"type":"bug","apps":["api"],"reason":"crash"}`
	in := ClassifyInput{
		Issue: newIssue("Export timezone offsets"), Labels: weightLabels, KnownTypes: []string{"bug", "story"}, KnownApps: []string{"api", "web"},
		Weight: config.WeightSource{Source: "label"}, Samples: []WeightSample{sample(1, "Export timezone fix", "bug", 2, "api")},
	}
	cr, err := e.svc.Classify(context.Background(), "sandbox", in, ClassifyOpts{Assume: true})
	if err != nil || cr.Run == nil {
		t.Fatalf("%+v %v", cr, err)
	}
	if cr.Suggestion.Source != SourceAI || cr.Suggestion.Weight == nil || *cr.Suggestion.Weight != 2 {
		t.Errorf("suggestion = %+v: want the AI labels and weight 2 from the suggested type/app", cr.Suggestion)
	}
}

func TestClosedSamplesReadTheForgeHistoryAndKeepWeightedOnly(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithClock(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)))
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, fake.Token)
	ctx := context.Background()
	tracker := fake.GroupPath + "/tracker"
	// tracker#9 and #10 are closed in the seed; weight them with labels as a Free team does.
	if err := c.SetLabels(ctx, forge.Ref{Project: tracker, IID: 10, Kind: forge.KindIssue}, []string{"weight::2"}, nil); err != nil {
		t.Fatal(err)
	}
	labels := config.Labels{Type: config.LabelFacet{Pattern: "type::{value}"}, App: config.LabelFacet{Pattern: "app::{value}"}}
	src := config.WeightSource{Source: "label", Pattern: "weight::{value}"}
	got, err := ClosedSamples(ctx, c, tracker, labels, src, time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Ref.IID != 10 || got[0].Weight != 2 || got[0].Type != "story" || !slices.Equal(got[0].Apps, []string{"api"}) {
		t.Fatalf("samples = %+v: want the one closed, weighted issue with its facets", got)
	}
	// Merging keeps the first of a repeated ref.
	dup := WeightSample{Ref: got[0].Ref, Weight: 99}
	if m := MergeSamples([]WeightSample{dup}, got); len(m) != 1 || m[0].Weight != 99 {
		t.Errorf("merge = %+v", m)
	}
}

type issueLister struct {
	forge.Forge
	issues []forge.Issue
}

func (l issueLister) ListIssues(context.Context, forge.IssueQuery) ([]forge.Issue, error) {
	return l.issues, nil
}

// The window runs on the closed date: an issue touched recently but closed
// long ago is history too old to size work by.
func TestClosedSamplesUseTheClosedDateForTheWindow(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	old, recent := now.Add(-SampleWindow-24*time.Hour), now.Add(-30*24*time.Hour)
	issue := func(iid int, closed *time.Time) forge.Issue {
		return forge.Issue{Ref: forge.Ref{Project: "g/tracker", IID: iid}, Labels: []string{"weight::3"}, ClosedAt: closed}
	}
	f := issueLister{issues: []forge.Issue{issue(1, &old), issue(2, &recent), issue(3, nil)}}
	got, err := ClosedSamples(context.Background(), f, "g/tracker", weightLabels, config.WeightSource{Source: "label", Pattern: "weight::{value}"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Ref.IID != 2 || got[1].Ref.IID != 3 {
		t.Errorf("samples = %+v, want #2 (closed recently) and #3 (no closed date: the forge's update filter stands)", got)
	}
}

// Float addition depends on order, so equal word sets must be summed in a
// fixed order or the Ref tie-break never gets to decide.
func TestSuggestWeightScoreDoesNotDependOnMapOrder(t *testing.T) {
	src := config.WeightSource{Source: "label"}
	samples := []WeightSample{
		sample(1, "Rework billing export pipeline retries logging", "bug", 3, "api"),
		sample(2, "Logging retries pipeline export billing rework", "bug", 8, "api"),
		sample(3, "Billing page", "bug", 2, "api"),
		sample(4, "Export csv", "bug", 2, "api"),
		sample(5, "Retries on timeout", "bug", 5, "api"),
		sample(6, "Pipeline stage names", "bug", 1, "api"),
		sample(7, "Logging format", "bug", 1, "api"),
	}
	issue := newIssue("Billing export pipeline retries logging rework", "type::bug", "app::api")
	_, first := SuggestWeight(issue, weightLabels, src, Suggestion{}, samples)
	if !strings.Contains(first, "g/tracker#1 (3), g/tracker#2 (8)") {
		t.Fatalf("equal scores should order by ref: %q", first)
	}
	for range 200 {
		if _, why := SuggestWeight(issue, weightLabels, src, Suggestion{}, samples); why != first {
			t.Fatalf("reason changed between runs:\n%q\n%q", first, why)
		}
	}
}

// With every word as common as the next, none is rarer than the median, so a
// single shared word does not qualify a sample; two do.
func TestSuggestWeightNeedsTwoSharedWordsWhenNoWordIsRarer(t *testing.T) {
	src := config.WeightSource{Source: "label"}
	samples := []WeightSample{
		sample(1, "Alpha beta", "bug", 3, "api"),
		sample(2, "Gamma delta", "bug", 5, "api"),
	}
	one := newIssue("Alpha epsilon", "type::bug", "app::api")
	if w, why := SuggestWeight(one, weightLabels, src, Suggestion{}, samples); w != nil {
		t.Errorf("one shared word in a pool of equally common words suggested %d (%s)", *w, why)
	}
	two := newIssue("Alpha beta epsilon", "type::bug", "app::api")
	if w, why := SuggestWeight(two, weightLabels, src, Suggestion{}, samples); w == nil || *w != 3 {
		t.Errorf("two shared words: %v (%s), want 3", w, why)
	}
}
