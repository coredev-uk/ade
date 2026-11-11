package ai

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forge"
)

// Suggestion sources.
const (
	SourceAI       = "ai"
	SourceKeywords = "keywords"
)

// typeKeywords maps a type value to the words in a title or description that
// point at it. Only values the team already uses (KnownTypes) are ever
// suggested; with none known, the three conventional ones are.
var typeKeywords = []struct {
	value string
	words []string
}{
	{"bug", []string{"bug", "crash", "crashes", "error", "errors", "fail", "fails", "failing", "failure", "broken", "regression", "exception", "timeout", "timeouts", "wrong", "incorrect", "leak", "500", "stale", "lag", "lags", "slow", "mismatch", "missing"}},
	{"chore", []string{"refactor", "upgrade", "bump", "cleanup", "clean", "remove", "rename", "migrate", "dependency", "dependencies", "ci", "docs", "documentation", "lint", "deprecate"}},
	{"story", []string{"add", "support", "implement", "allow", "enable", "introduce", "new", "create", "provide", "export", "feature"}},
}

var wordSplit = regexp.MustCompile(`[^a-z0-9]+`)

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordSplit.Split(strings.ToLower(s), -1) {
		if w != "" {
			out[w] = true
		}
	}
	return out
}

// SuggestKeywords is the deterministic classifier: no AI, no cost. The type
// comes from keywords in the title (weighted double) and description, the app
// from known app names mentioned in either. Missing facets only, allowed
// values only, like the AI path.
func SuggestKeywords(in ClassifyInput) Suggestion {
	miss := in.Missing()
	s := Suggestion{Source: SourceKeywords, Labels: []string{}}
	title, body := words(in.Issue.Title), words(in.Issue.Description)
	// reasons says what matched; misses what looked at but found nothing, so
	// a facet left out is never silently out.
	var reasons, misses []string
	if slices.Contains(miss, "type") {
		best, bestScore := "", 0
		for _, tk := range typeKeywords {
			if len(in.KnownTypes) > 0 && !slices.ContainsFunc(in.KnownTypes, func(k string) bool { return strings.EqualFold(k, tk.value) }) {
				continue
			}
			score := 0
			for _, w := range tk.words {
				if title[w] {
					score += 2
				}
				if body[w] {
					score++
				}
			}
			if score > bestScore {
				best, bestScore = tk.value, score
			}
		}
		if best != "" {
			s.Type = best
			s.Labels = append(s.Labels, applyPattern(in.Labels.Type.Pattern, best))
			reasons = append(reasons, "keywords in the title and description point at "+best)
		} else {
			misses = append(misses, "type")
		}
	}
	if slices.Contains(miss, "app") {
		for _, a := range sortedCopy(in.KnownApps) {
			la := strings.ToLower(a)
			if (title[la] || body[la]) && !slices.Contains(s.Apps, la) {
				s.Apps = append(s.Apps, la)
				s.Labels = append(s.Labels, applyPattern(in.Labels.App.Pattern, la))
			}
		}
		if len(s.Apps) > 0 {
			reasons = append(reasons, "the issue names "+strings.Join(s.Apps, ", "))
		} else {
			misses = append(misses, "app")
		}
	}
	if len(misses) > 0 {
		reasons = append(reasons, "no "+strings.Join(misses, " or ")+" keyword matched")
	}
	s.Reason = strings.Join(reasons, "; ")
	return s
}

// ClassifyOpts are the per-call options of Classify.
type ClassifyOpts struct {
	Profile  string
	Refresh  bool
	Override bool
	Assume   bool
	Confirm  func(Preflight) (bool, error)
	// NoAI forces the deterministic path.
	NoAI bool
}

// ClassifyResult is a suggestion with how it was made.
type ClassifyResult struct {
	Suggestion Suggestion
	// Run is the AI call behind the suggestion; nil for keywords.
	Run *Result
}

// UsesAI reports whether Classify would call the model: ai.mode is not off,
// a profile is selected, and the caller did not ask for keywords.
func (s *Service) UsesAI(o ClassifyOpts) bool {
	if o.NoAI || s.Cfg.AI.Mode == "off" {
		return false
	}
	name := o.Profile
	if name == "" {
		name = s.Cfg.AI.Profile
	}
	_, ok := s.Cfg.AI.Profiles[name]
	return ok
}

// Classify suggests the missing type/app labels: with the model when AI is on,
// by keywords when it is off or unconfigured. Either way an unweighted issue
// also gets a weight suggested from similar closed issues (SuggestWeight: no
// model, no cost). ErrNothingToClassify when the issue has every facet and
// no weight to suggest. Nothing is applied.
func (s *Service) Classify(ctx context.Context, forgeName string, in ClassifyInput, o ClassifyOpts) (ClassifyResult, error) {
	job, err := ClassifyJob(forgeName, in)
	nothing := errors.Is(err, ErrNothingToClassify)
	if err != nil && !nothing {
		return ClassifyResult{}, err
	}
	if nothing || !s.UsesAI(o) {
		sug := Suggestion{Labels: []string{}}
		if !nothing {
			sug = SuggestKeywords(in)
		}
		sug.Weight, sug.WeightReason = SuggestWeight(in.Issue, in.Labels, in.Weight, sug, in.Samples)
		if nothing && sug.Weight == nil {
			return ClassifyResult{}, ErrNothingToClassify
		}
		if nothing {
			sug.Source = SourceKeywords
		}
		return ClassifyResult{Suggestion: sug}, nil
	}
	job.Profile, job.Refresh, job.Override, job.Assume, job.Confirm = o.Profile, o.Refresh, o.Override, o.Assume, o.Confirm
	res, err := s.Run(ctx, job)
	if err != nil {
		return ClassifyResult{Run: &res}, err
	}
	sug, err := ParseSuggestion(res.Text, in)
	if err != nil {
		return ClassifyResult{Run: &res}, err
	}
	sug.Source = SourceAI
	sug.Weight, sug.WeightReason = SuggestWeight(in.Issue, in.Labels, in.Weight, sug, in.Samples)
	return ClassifyResult{Suggestion: sug, Run: &res}, nil
}

// ApplyLabels adds labels to the issue and audits it as forge.issue.labels.
// It never removes anything. The caller asks the user first.
func ApplyLabels(ctx context.Context, w forge.IssueWriter, log *audit.Log, ref forge.Ref, labels []string, source string) error {
	if len(labels) == 0 {
		return errors.New("no labels to apply")
	}
	err := w.SetLabels(ctx, ref, labels, nil)
	if log != nil {
		e := audit.Entry{
			Action: "forge.issue.labels", Target: ref.String(), Item: ref.String(),
			Params: map[string]any{"add": labels, "reason": "classify (" + source + ")"},
		}
		if err != nil {
			e.Outcome, e.Error = "error", err.Error()
		}
		if aerr := log.Append(e); aerr != nil && err == nil {
			return fmt.Errorf("labels applied, but the audit entry failed: %w", aerr)
		}
	}
	return err
}
