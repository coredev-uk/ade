package ai

import (
	"cmp"
	"context"
	"fmt"
	"math"
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
// Synced work items only cover the sprint window; this is the history. The
// window runs on the closed date when the forge gives one; the forge is asked
// by update time, which closing always moves, so no closed issue is missed.
func ClosedSamples(ctx context.Context, f forge.Forge, project string, labels config.Labels, src config.WeightSource, now time.Time) ([]WeightSample, error) {
	issues, err := f.ListIssues(ctx, forge.IssueQuery{Project: project, State: "closed", UpdatedAfter: now.Add(-SampleWindow)})
	if err != nil {
		return nil, err
	}
	var out []WeightSample
	for _, is := range issues {
		w := domain.ResolveWeight(is, src)
		if w == nil || (is.ClosedAt != nil && is.ClosedAt.Before(now.Add(-SampleWindow))) {
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

// stopWords are title words too common to say two issues are alike: function
// words, plus verbs and nouns that every tracker title carries whatever the
// size of the work. They are checked on the singular form, so "adds" and
// "fixes" go with "add" and "fix".
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true, "into": true, "when": true, "that": true,
	"this": true, "not": true, "are": true, "its": true, "all": true,
	"add": true, "fix": true, "use": true, "update": true, "change": true, "remove": true, "support": true, "make": true,
	"new": true, "page": true, "issue": true, "bug": true, "feature": true,
}

// titleTokens are the words of a title that can say two issues are alike,
// with plurals folded ("exports" and "export" are the same word).
func titleTokens(s string) map[string]bool {
	out := map[string]bool{}
	for w := range words(s) {
		if len(w) < 3 {
			continue
		}
		if t := singular(w); !stopWords[w] && !stopWords[t] {
			out[t] = true
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

// nearestSamples is how many of the most similar closed issues the suggestion
// is drawn from.
const nearestSamples = 5

// facetBonus is what each matching facet (type, app) adds to a similarity
// score: less than a rare shared word, so facets order near ties only.
const facetBonus = 0.5

// match is a sample that is similar enough to the issue, with how similar.
type match struct {
	sample WeightSample
	score  float64
}

// SuggestWeight proposes a weight for an unweighted issue from the closed
// issues like it: the same type and app (the dimensions the issue has; the
// suggested facets count when the issue lacks them) that share title words.
// Samples rank by the summed rarity (IDF over all samples) of the shared
// words plus a small bonus per matching facet. A sample qualifies with two
// shared words, or one that is rarer than the median word. The suggestion is
// the score-weighted median weight of the nearest few, so it is a weight the
// team really used; on an even split it takes the one nearer the weighted
// mean. It is deterministic and needs no AI. nil when the issue has a weight,
// the project does not track weights, nothing compares, or the type and app
// are both unknown.
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
	var pool []WeightSample
	for _, s := range samples {
		if s.Ref != is.Ref && s.Weight > 0 {
			pool = append(pool, s)
		}
	}
	idf, floor := tokenRarity(pool)
	mine := titleTokens(is.Title)
	var found []match
	for _, s := range pool {
		if typ != "" && !strings.EqualFold(typ, s.Type) {
			continue
		}
		sharedApps := 0
		for _, a := range apps {
			if slices.ContainsFunc(s.Apps, func(b string) bool { return strings.EqualFold(a, b) }) {
				sharedApps++
			}
		}
		if len(apps) > 0 && sharedApps == 0 {
			continue
		}
		shared, rare, score := 0, false, 0.0
		for w := range titleTokens(s.Title) {
			if mine[w] {
				shared++
				rare = rare || idf[w] >= floor
				score += idf[w]
			}
		}
		if shared == 0 || (shared < 2 && !rare) {
			continue
		}
		if typ != "" {
			score += facetBonus
		}
		if len(apps) > 0 {
			score += facetBonus * float64(sharedApps) / float64(len(apps))
		}
		found = append(found, match{s, score})
	}
	if len(found) == 0 {
		return nil, "no weight suggested: no closed issue of the same type and app is similar enough"
	}
	slices.SortFunc(found, func(a, b match) int {
		return cmp.Or(cmp.Compare(b.score, a.score), strings.Compare(a.sample.Ref.Project, b.sample.Ref.Project), cmp.Compare(a.sample.Ref.IID, b.sample.Ref.IID))
	})
	nearest := found[:min(len(found), nearestSamples)]
	w := weightedMedian(nearest)
	pairs := make([]string, min(len(nearest), 3))
	for i := range pairs {
		pairs[i] = fmt.Sprintf("%s (%d)", nearest[i].sample.Ref, nearest[i].sample.Weight)
	}
	list := strings.Join(pairs, ", ")
	switch {
	case len(found) == 1:
		return &w, "only one similar issue: " + list
	case len(found) > len(nearest):
		return &w, fmt.Sprintf("weighted median of the %d nearest of %d similar closed issues: %s", len(nearest), len(found), list)
	}
	return &w, fmt.Sprintf("weighted median of %d similar closed issues: %s", len(found), list)
}

// tokenRarity gives each title word of the samples its IDF (a word in every
// sample still scores above zero) and the median of those values: a word at
// or above it is rarer than a typical one.
func tokenRarity(pool []WeightSample) (idf map[string]float64, median float64) {
	df := map[string]int{}
	for _, s := range pool {
		for w := range titleTokens(s.Title) {
			df[w]++
		}
	}
	idf = make(map[string]float64, len(df))
	vals := make([]float64, 0, len(df))
	for w, n := range df {
		idf[w] = math.Log(float64(len(pool)+1)/float64(n)) + 1
		vals = append(vals, idf[w])
	}
	slices.Sort(vals)
	if len(vals) > 0 {
		median = vals[len(vals)/2]
	}
	return idf, median
}

// weightedMedian is the weight at which half of the total score lies on
// either side, for matches in score order. On an exact even split it picks the
// neighbouring weight nearer the score-weighted mean; when that is a tie too,
// the one on the best match's side.
func weightedMedian(ms []match) int {
	byWeight := slices.Clone(ms)
	slices.SortStableFunc(byWeight, func(a, b match) int { return cmp.Compare(a.sample.Weight, b.sample.Weight) })
	total, mean := 0.0, 0.0
	for _, m := range ms {
		total += m.score
		mean += m.score * float64(m.sample.Weight)
	}
	mean /= total
	const eps = 1e-9
	cum := 0.0
	for i, m := range byWeight {
		cum += m.score
		switch {
		case cum < total/2-eps:
			continue
		case cum > total/2+eps || i == len(byWeight)-1:
			return m.sample.Weight
		}
		lo, hi := m.sample.Weight, byWeight[i+1].sample.Weight
		dLo, dHi := mean-float64(lo), float64(hi)-mean
		if dLo < dHi-eps || (math.Abs(dLo-dHi) < eps && ms[0].sample.Weight <= lo) {
			return lo
		}
		return hi
	}
	return byWeight[len(byWeight)-1].sample.Weight
}
