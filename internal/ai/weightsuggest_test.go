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

func TestSuggestWeightIsTheLowerMedianOfSimilarClosedIssues(t *testing.T) {
	samples := []WeightSample{
		sample(1, "Export timestamps ignore tenant timezone", "bug", 3, "api"),
		sample(2, "Export worker crashes on empty file", "bug", 2, "api"),
		sample(3, "Export preview truncates headers", "bug", 5, "api"),
		sample(4, "Export preview shows stale totals", "bug", 1, "web"), // other app
		sample(5, "Export dashboard dark mode", "story", 8, "api"),      // other type
		sample(6, "Rotate credentials quarterly", "bug", 13, "api"),     // no shared word
		sample(7, "Export timezone offsets again", "bug", 0, "api"),     // unweighted never counts
	}
	issue := newIssue("Exports break for tenants in a non-UTC timezone", "type::bug", "app::api")
	w, why := SuggestWeight(issue, weightLabels, config.WeightSource{Source: "label"}, Suggestion{}, samples)
	if w == nil || *w != 3 {
		t.Fatalf("weight = %v (%s), want the lower median 3 of weights 2, 3 and 5", w, why)
	}
	if !strings.Contains(why, "median of 3") || !strings.Contains(why, "g/tracker#1") || strings.Contains(why, "#5") {
		t.Errorf("reason = %q", why)
	}
	// Deterministic: same input, same answer, whatever the order.
	slicesReverse(samples)
	if w2, why2 := SuggestWeight(issue, weightLabels, config.WeightSource{Source: "label"}, Suggestion{}, samples); *w2 != *w || why2 != why {
		t.Errorf("not order independent: %d %q vs %d %q", *w2, why2, *w, why)
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
