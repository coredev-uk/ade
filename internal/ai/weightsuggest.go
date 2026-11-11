package ai

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

// WeightSample is a closed, weighted issue: what a new issue can be compared
// with.
type WeightSample struct {
	Ref    forge.Ref `json:"ref"`
	Title  string    `json:"title"`
	Type   string    `json:"type,omitempty"`
	Apps   []string  `json:"apps,omitempty"`
	Weight int       `json:"weight"`
}

// WeightSamples picks the closed issues with a weight out of synced work
// items: done items (closed, or finished by the done label) and their children.
func WeightSamples(items []domain.WorkItem) []WeightSample {
	var out []WeightSample
	for _, it := range items {
		if it.Weight == nil || *it.Weight <= 0 || (it.Issue.State != "closed" && it.Stage != domain.StageDone) {
			continue
		}
		out = append(out, WeightSample{Ref: it.Ref, Title: it.Issue.Title, Type: it.Type, Apps: it.Apps, Weight: *it.Weight})
	}
	return out
}

// SampleWindow is how far back closed issues still say what a piece of work
// weighs.
const SampleWindow = 365 * 24 * time.Hour

// ClosedSamples reads the project's issues closed within SampleWindow of now
// and keeps the weighted ones (weights read under src, facets under labels).
// Synced work items only cover the sprint window; this is the history.
func ClosedSamples(ctx context.Context, f forge.Forge, project string, labels config.Labels, src config.WeightSource, now time.Time) ([]WeightSample, error) {
	issues, err := f.ListIssues(ctx, forge.IssueQuery{Project: project, State: "closed", UpdatedAfter: now.Add(-SampleWindow)})
	if err != nil {
		return nil, err
	}
	var out []WeightSample
	for _, is := range issues {
		w := domain.ResolveWeight(is, src)
		if w == nil {
			continue
		}
		fc := domain.FacetsOf(is.Labels, labels)
		out = append(out, WeightSample{Ref: is.Ref, Title: is.Title, Type: fc.Type, Apps: fc.Apps, Weight: *w})
	}
	return out, nil
}

// MergeSamples joins sample lists, the first occurrence of a ref winning.
func MergeSamples(lists ...[]WeightSample) []WeightSample {
	var out []WeightSample
	seen := map[forge.Ref]bool{}
	for _, l := range lists {
		for _, s := range l {
			if !seen[s.Ref] {
				seen[s.Ref] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// stopWords are title words too common to say two issues are alike.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true, "into": true, "when": true, "that": true,
	"this": true, "not": true, "are": true, "its": true, "all": true, "add": true, "fix": true, "use": true,
}

// titleTokens are the words of a title that can say two issues are alike,
// with plurals folded ("exports" and "export" are the same word).
func titleTokens(s string) map[string]bool {
	out := map[string]bool{}
	for w := range words(s) {
		if len(w) >= 3 && !stopWords[w] {
			out[singular(w)] = true
		}
	}
	return out
}

func singular(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 4 && (strings.HasSuffix(w, "sses") || strings.HasSuffix(w, "xes") || strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "shes")):
		return w[:len(w)-2]
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return w[:len(w)-1]
	}
	return w
}

// SuggestWeight proposes a weight for an unweighted issue from the closed
// issues like it: the same type and app (the dimensions the issue has; the
// suggested facets count when the issue lacks them) and at least one title
// word in common. The suggestion is the lower median of their weights, so it
// is a weight the team really used. It is deterministic and needs no AI. nil
// when the issue has a weight, the project does not track weights, nothing
// compares, or the type and app are both unknown.
func SuggestWeight(is forge.Issue, labels config.Labels, src config.WeightSource, sug Suggestion, samples []WeightSample) (*int, string) {
	if src.Source == "none" || domain.ResolveWeight(is, src) != nil {
		return nil, ""
	}
	f := domain.FacetsOf(is.Labels, labels)
	typ := cmp.Or(f.Type, sug.Type)
	apps := f.Apps
	if len(apps) == 0 {
		apps = sug.Apps
	}
	if typ == "" && len(apps) == 0 {
		return nil, "no weight suggested: the issue has no type or app to compare with"
	}
	mine := titleTokens(is.Title)
	var weights []int
	var refs []string
	for _, s := range samples {
		if s.Ref == is.Ref || s.Weight <= 0 {
			continue
		}
		if typ != "" && !strings.EqualFold(typ, s.Type) {
			continue
		}
		if len(apps) > 0 && !slices.ContainsFunc(apps, func(a string) bool {
			return slices.ContainsFunc(s.Apps, func(b string) bool { return strings.EqualFold(a, b) })
		}) {
			continue
		}
		shared := false
		for w := range titleTokens(s.Title) {
			if mine[w] {
				shared = true
				break
			}
		}
		if !shared {
			continue
		}
		weights = append(weights, s.Weight)
		refs = append(refs, s.Ref.String())
	}
	if len(weights) == 0 {
		return nil, "no weight suggested: no closed issue of the same type and app shares a title word"
	}
	slices.Sort(weights)
	w := weights[(len(weights)-1)/2]
	slices.Sort(refs)
	shown := refs
	if len(shown) > 3 {
		shown = append(slices.Clone(shown[:3]), fmt.Sprintf("+%d more", len(refs)-3))
	}
	strs := make([]string, len(weights))
	for i, v := range weights {
		strs[i] = fmt.Sprint(v)
	}
	return &w, fmt.Sprintf("median of %d similar closed issues (%s): weights %s", len(weights), strings.Join(shown, ", "), strings.Join(strs, ", "))
}
