package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
)

func classifyIn(title, desc string, labels ...string) ClassifyInput {
	return ClassifyInput{
		Issue:      forge.Issue{Ref: forge.Ref{Project: "g/tracker", IID: 1}, Title: title, Description: desc, Labels: labels},
		Labels:     config.Labels{Type: config.LabelFacet{Pattern: "type::{value}"}, App: config.LabelFacet{Pattern: "app::{value}"}},
		KnownTypes: []string{"story", "bug", "chore"}, KnownApps: []string{"api", "web"},
	}
}

func TestKeywordSuggestionsAreDeterministicAndRespectAllowedValues(t *testing.T) {
	s := SuggestKeywords(classifyIn("Export endpoint crashes with a timeout", "The api returns 500 when the export is large."))
	if strings.Join(s.Labels, ",") != "type::bug,app::api" || s.Source != SourceKeywords || s.Reason == "" {
		t.Fatalf("%+v", s)
	}
	// Title words outweigh description words.
	s = SuggestKeywords(classifyIn("Add CSV export", "this was broken before"))
	if s.Type != "story" {
		t.Fatalf("%+v", s)
	}
	// Only missing facets: the app is already there, the type is suggested.
	s = SuggestKeywords(classifyIn("Fix crash in web client", "", "app::web"))
	if strings.Join(s.Labels, ",") != "type::bug" {
		t.Fatalf("%+v", s)
	}
	// A type the team does not use is never suggested.
	in := classifyIn("Fix crash", "")
	in.KnownTypes = []string{"story"}
	if s := SuggestKeywords(in); s.Type != "" || len(s.Labels) != 0 {
		t.Fatalf("%+v", s)
	}
	// Nothing recognisable: an empty, non-nil label list (stable JSON).
	s = SuggestKeywords(classifyIn("Hmm", "?"))
	if s.Labels == nil || len(s.Labels) != 0 {
		t.Fatalf("%+v", s)
	}
}

type labelWriter struct {
	forge.IssueWriter
	add, remove []string
	err         error
}

func (w *labelWriter) SetLabels(_ context.Context, _ forge.Ref, add, remove []string) error {
	w.add, w.remove = add, remove
	return w.err
}

func TestApplyLabelsOnlyAddsAndIsAudited(t *testing.T) {
	e := newEnv(t)
	w := &labelWriter{}
	ref := forge.Ref{Project: "g/tracker", IID: 11}
	if err := ApplyLabels(context.Background(), w, e.log, ref, []string{"type::bug"}, SourceKeywords); err != nil {
		t.Fatal(err)
	}
	if len(w.add) != 1 || w.remove != nil {
		t.Fatalf("add %v remove %v", w.add, w.remove)
	}
	es, _ := e.log.Query(audit.Filter{Action: "forge.issue.labels"})
	if len(es) != 1 || es[0].Outcome != "ok" || es[0].Params["reason"] != "classify (keywords)" {
		t.Fatalf("%+v", es)
	}
	w.err = errors.New("403")
	if err := ApplyLabels(context.Background(), w, e.log, ref, []string{"app::api"}, SourceAI); err == nil {
		t.Fatal("a forge failure must surface")
	}
	if es, _ := e.log.Query(audit.Filter{Action: "forge.issue.labels"}); len(es) != 2 || es[1].Outcome != "error" {
		t.Fatalf("failures are audited too: %+v", es)
	}
	if err := ApplyLabels(context.Background(), w, e.log, ref, nil, SourceAI); err == nil {
		t.Fatal("nothing to apply is an error, not a silent no-op write")
	}
}

func TestClassifyFallsBackToKeywordsWhenAIIsOffOrUnconfigured(t *testing.T) {
	e := newEnv(t)
	in := classifyIn("Search crashes on empty query", "")
	e.conf.AI.Mode = "off"
	cr, err := e.svc.Classify(context.Background(), "f", in, ClassifyOpts{})
	if err != nil || cr.Run != nil || cr.Suggestion.Source != SourceKeywords || e.drv.calls != 0 {
		t.Fatalf("%+v %v calls=%d", cr, err, e.drv.calls)
	}
	e.conf.AI.Mode = "auto"
	if cr, _ := e.svc.Classify(context.Background(), "f", in, ClassifyOpts{NoAI: true}); cr.Run != nil {
		t.Fatal("--no-ai must not call the model")
	}
	e.drv.resp.Text = `{"type":"bug","apps":["web"],"reason":"r"}`
	cr, err = e.svc.Classify(context.Background(), "f", in, ClassifyOpts{})
	if err != nil || cr.Run == nil || cr.Suggestion.Source != SourceAI || strings.Join(cr.Suggestion.Labels, ",") != "type::bug,app::web" {
		t.Fatalf("%+v %v", cr, err)
	}
	if _, err := e.svc.Classify(context.Background(), "f", classifyIn("x", "", "type::bug", "app::api"), ClassifyOpts{}); !errors.Is(err, ErrNothingToClassify) {
		t.Fatalf("%v", err)
	}
}
