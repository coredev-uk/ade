package bootstrap

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Label facets are the three (plus weight) ways a team spells structure in
// labels: a scope, a separator and a value. The scope names are synonyms of
// the facet; the separator is whatever the team uses.

// facetScopes maps the scope of a scoped label to the facet it plays. Scopes
// are listed in order of preference: when a team has both "type::" and
// "kind/" labels, the canonical name wins a tie.
var facetScopes = map[string][]string{
	"type":     {"type", "kind", "issuetype", "issue-type", "issue_type"},
	"app":      {"app", "area", "component", "service", "repo"},
	"workflow": {"workflow", "status", "state", "stage", "column"},
	"weight":   {"weight", "points", "story-points", "estimate", "sp"},
}

// separators are tried in order at the same position, so "a::b" is scope a
// with separator "::", not scope a with separator ":".
var separators = []string{"::", ":", "/"}

// splitScoped cuts a label at its first separator. Labels without one
// ("good first issue") are not scoped.
func splitScoped(label string) (scope, sep, value string, ok bool) {
	best := -1
	for _, s := range separators {
		i := strings.Index(label, s)
		if i < 1 || (best >= 0 && i >= best) {
			continue
		}
		best, sep = i, s
	}
	if best < 0 {
		return "", "", "", false
	}
	scope, value = strings.TrimSpace(label[:best]), strings.TrimSpace(label[best+len(sep):])
	if scope == "" || value == "" {
		return "", "", "", false
	}
	return scope, sep, value, true
}

// Facet is one detected label facet.
type Facet struct {
	Scope   string   // as spelled on the forge, e.g. "type"
	Sep     string   // "::", ":" or "/"
	Pattern string   // "type::{value}"
	Values  []string // distinct values, busiest first
	Issues  int      // issues carrying any label of the facet
}

// Facets are the facets found among a project's labels. A nil facet was not
// found.
type Facets struct {
	Type, App, Workflow, Weight *Facet
}

type scopeKey struct{ scope, sep string }

type scopeStats struct {
	spelled string
	issues  int
	values  map[string]int // value -> issues carrying it
}

// DetectFacets finds the scoped label patterns in defined (every label the
// project offers) and used (label -> number of issues carrying it). The best
// pattern of a facet is the one on the most issues; on a tie, the one with the
// most values, then the canonical scope name.
func DetectFacets(defined []string, used map[string]int) Facets {
	stats := map[scopeKey]*scopeStats{}
	add := func(label string, n int) {
		scope, sep, value, ok := splitScoped(label)
		if !ok {
			return
		}
		k := scopeKey{strings.ToLower(scope), sep}
		st := stats[k]
		if st == nil {
			st = &scopeStats{spelled: scope, values: map[string]int{}}
			stats[k] = st
		}
		st.issues += n
		st.values[value] += n
	}
	seen := map[string]bool{}
	for _, l := range defined {
		seen[l] = true
		add(l, used[l])
	}
	for l, n := range used {
		if !seen[l] {
			add(l, n)
		}
	}

	var f Facets
	for facet, dst := range map[string]**Facet{"type": &f.Type, "app": &f.App, "workflow": &f.Workflow, "weight": &f.Weight} {
		*dst = pickFacet(facet, stats)
	}
	return f
}

func pickFacet(facet string, stats map[scopeKey]*scopeStats) *Facet {
	scopes := facetScopes[facet]
	type cand struct {
		key  scopeKey
		st   *scopeStats
		rank int
	}
	var cands []cand
	for k, st := range stats {
		rank := slices.Index(scopes, k.scope)
		if rank < 0 {
			continue
		}
		if facet == "weight" && !allNumeric(st.values) {
			continue
		}
		cands = append(cands, cand{k, st, rank})
	}
	if len(cands) == 0 {
		return nil
	}
	slices.SortFunc(cands, func(a, b cand) int {
		return cmp.Or(
			cmp.Compare(b.st.issues, a.st.issues),
			cmp.Compare(len(b.st.values), len(a.st.values)),
			cmp.Compare(a.rank, b.rank),
			cmp.Compare(a.key.sep, b.key.sep),
		)
	})
	best := cands[0]
	values := make([]string, 0, len(best.st.values))
	for v := range best.st.values {
		values = append(values, v)
	}
	slices.SortFunc(values, func(a, b string) int {
		return cmp.Or(cmp.Compare(best.st.values[b], best.st.values[a]), cmp.Compare(a, b))
	})
	return &Facet{
		Scope: best.st.spelled, Sep: best.key.sep, Pattern: best.st.spelled + best.key.sep + "{value}",
		Values: values, Issues: best.st.issues,
	}
}

func allNumeric(values map[string]int) bool {
	for v := range values {
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return false
		}
	}
	return len(values) > 0
}

// IsAppLabel reports whether a label is spelled like an app facet label
// ("app::api", "area/web"): what a tracker's issues carry to route work.
func IsAppLabel(label string) bool {
	scope, _, _, ok := splitScoped(label)
	return ok && slices.Contains(facetScopes["app"], strings.ToLower(scope))
}

// Stage values the manifest's labels.workflow.stage maps.
const (
	stageStarted = "started"
	stageDraft   = "draft"
	stageReview  = "review"
	stageMerged  = "merged"
)

// Workflow is how the workflow values of a team map to the stages ADE derives.
type Workflow struct {
	Stage    map[string]string // stage -> value; started, draft, review, merged
	Done     string            // value marking an open tracker issue finished
	Blocked  string            // a value ADE reads as blocked; never mapped
	Unmapped []string          // values no stage matched (backlog, ready, ...)
	Extra    []string          // values that matched a stage already taken
}

var (
	blockedValue  = regexp.MustCompile(`\b(blocked|on hold|impeded|stuck)\b`)
	doneValue     = regexp.MustCompile(`^(done|closed|complete|completed|finished|shipped|released|resolved)$`)
	reviewValue   = regexp.MustCompile(`\breview\b`)
	progressValue = regexp.MustCompile(`^(in progress|doing|started|wip|in dev|in development|development|developing|active|working|in work|ongoing|implementing)$`)
	nonWord       = regexp.MustCompile(`[^a-z0-9]+`)
)

func normaliseValue(v string) string {
	return strings.TrimSpace(nonWord.ReplaceAllString(strings.ToLower(v), " "))
}

// MapWorkflow maps workflow values to stages by name: in progress is started
// and draft, in review is review, done is merged and also the value that
// marks a tracker issue finished, blocked is kept as the blocked value and
// never mapped. When several values fit one stage the busiest wins; used is
// the number of issues carrying each value.
func MapWorkflow(values []string, used map[string]int) Workflow {
	w := Workflow{Stage: map[string]string{}}
	buckets := map[string][]string{}
	for _, v := range values {
		n := normaliseValue(v)
		switch {
		case blockedValue.MatchString(n):
			if w.Blocked == "" {
				w.Blocked = v
			}
		case doneValue.MatchString(n):
			buckets[stageMerged] = append(buckets[stageMerged], v)
		case reviewValue.MatchString(n):
			buckets[stageReview] = append(buckets[stageReview], v)
		case progressValue.MatchString(n):
			buckets[stageStarted] = append(buckets[stageStarted], v)
		default:
			w.Unmapped = append(w.Unmapped, v)
		}
	}
	pick := func(bucket string) string {
		vs := buckets[bucket]
		if len(vs) == 0 {
			return ""
		}
		slices.SortStableFunc(vs, func(a, b string) int { return cmp.Compare(used[b], used[a]) })
		w.Extra = append(w.Extra, vs[1:]...)
		return vs[0]
	}
	if v := pick(stageStarted); v != "" {
		w.Stage[stageStarted], w.Stage[stageDraft] = v, v
	}
	if v := pick(stageReview); v != "" {
		w.Stage[stageReview] = v
	}
	if v := pick(stageMerged); v != "" {
		w.Stage[stageMerged], w.Done = v, v
	}
	return w
}

// MatchApps routes app-label values to repos. A value equal to a repo's name
// (any case) goes to that repo; a value contained in a repo's name, or
// containing it, goes there when no repo claimed it exactly. A repo no value
// matches gets its own name as the value, which is what an issue labelled
// after the repo would carry. names are repo keys, in the order given;
// matched reports whether a repo found its value among the labels.
func MatchApps(names, values []string) (apps map[string][]string, matched map[string]bool) {
	apps, matched = map[string][]string{}, map[string]bool{}
	claimed := map[string]bool{}
	for _, n := range names {
		for _, v := range values {
			if strings.EqualFold(n, v) {
				apps[n] = append(apps[n], v)
				matched[n], claimed[v] = true, true
			}
		}
	}
	for _, n := range names {
		if matched[n] {
			continue
		}
		ln := strings.ToLower(n)
		for _, v := range values {
			lv := strings.ToLower(v)
			if claimed[v] || len(lv) < 3 || len(ln) < 3 {
				continue
			}
			if strings.Contains(ln, lv) || strings.Contains(lv, ln) {
				apps[n] = append(apps[n], v)
				matched[n] = true
			}
		}
	}
	for _, n := range names {
		if len(apps[n]) == 0 {
			apps[n] = []string{n}
		}
	}
	return apps, matched
}
