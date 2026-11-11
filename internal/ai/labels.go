package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

// ErrNothingToClassify means the issue already carries every facet; no call
// is made and nothing is spent.
var ErrNothingToClassify = errors.New("the issue already has a type and an app label")

// ClassifyInput is what classification needs besides the issue.
type ClassifyInput struct {
	Issue  forge.Issue
	Labels config.Labels
	// KnownTypes and KnownApps are the values the team uses. Apps come from
	// the manifest's repos; types from the work items already synced. An empty
	// KnownTypes accepts any plain value.
	KnownTypes []string
	KnownApps  []string
}

// Missing lists the facets the issue lacks: "type", "app".
func (in ClassifyInput) Missing() []string {
	f := domain.FacetsOf(in.Issue.Labels, in.Labels)
	var miss []string
	if f.Type == "" && in.Labels.Type.Pattern != "" {
		miss = append(miss, "type")
	}
	if len(f.Apps) == 0 && in.Labels.App.Pattern != "" {
		miss = append(miss, "app")
	}
	return miss
}

// ClassifyJob builds the job, or ErrNothingToClassify when no facet is
// missing (deterministic: no AI needed).
func ClassifyJob(forgeName string, in ClassifyInput) (Job, error) {
	miss := in.Missing()
	if len(miss) == 0 {
		return Job{}, ErrNothingToClassify
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Issue %s: %s\n", in.Issue.Ref, in.Issue.Title)
	fmt.Fprintf(&b, "Current labels: %s\n", strings.Join(in.Issue.Labels, ", "))
	fmt.Fprintf(&b, "\nDescription:\n%s\n", clip(in.Issue.Description, maxDescription))
	fmt.Fprintf(&b, "\nMISSING facets: %s\n", strings.Join(miss, ", "))
	if slices.Contains(miss, "type") {
		if len(in.KnownTypes) > 0 {
			fmt.Fprintf(&b, "Allowed type values: %s\n", strings.Join(sortedCopy(in.KnownTypes), ", "))
		} else {
			b.WriteString("Allowed type values: a single lowercase word such as story, bug or chore\n")
		}
	}
	if slices.Contains(miss, "app") {
		fmt.Fprintf(&b, "Allowed app values: %s\n", strings.Join(sortedCopy(in.KnownApps), ", "))
	}
	return Job{
		Feature: "classify", Role: RoleExplain, PromptVersion: classifyVersion,
		System: classifySystem, Prompt: b.String(),
		Forge: forgeName, Ref: in.Issue.Ref, Name: "classify",
		MaxTokens: 200,
	}, nil
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// Suggestion is the labels classification proposes. Nothing is applied here.
type Suggestion struct {
	Type   string   `json:"type,omitempty"`
	Apps   []string `json:"apps,omitempty"`
	Reason string   `json:"reason,omitempty"`
	// Labels are the full label names to add, from the manifest's patterns.
	Labels []string `json:"labels"`
	// Dropped lists model suggestions that were ignored and why.
	Dropped []string `json:"dropped,omitempty"`
	// Source is how it was made: "ai" or "keywords".
	Source string `json:"source,omitempty"`
}

var plainValue = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ParseSuggestion reads the model's answer and keeps only suggestions for
// missing facets that use allowed values.
func ParseSuggestion(text string, in ClassifyInput) (Suggestion, error) {
	var raw struct {
		Type   string   `json:"type"`
		Apps   []string `json:"apps"`
		Reason string   `json:"reason"`
	}
	if err := decodeJSON(text, &raw); err != nil {
		return Suggestion{}, &badAnswer{err: err}
	}
	miss := in.Missing()
	s := Suggestion{Reason: raw.Reason, Labels: []string{}}
	if t := strings.ToLower(strings.TrimSpace(raw.Type)); t != "" {
		switch {
		case !slices.Contains(miss, "type"):
			s.Dropped = append(s.Dropped, fmt.Sprintf("type %q: the issue already has a type", t))
		case len(in.KnownTypes) > 0 && !slices.ContainsFunc(in.KnownTypes, func(k string) bool { return strings.EqualFold(k, t) }):
			s.Dropped = append(s.Dropped, fmt.Sprintf("type %q: not one of %s", t, strings.Join(sortedCopy(in.KnownTypes), ", ")))
		case !plainValue.MatchString(t):
			s.Dropped = append(s.Dropped, fmt.Sprintf("type %q: not a plain value", t))
		default:
			s.Type = t
			s.Labels = append(s.Labels, applyPattern(in.Labels.Type.Pattern, t))
		}
	}
	seen := map[string]bool{}
	for _, a := range raw.Apps {
		a = strings.ToLower(strings.TrimSpace(a))
		switch {
		case a == "" || seen[a]:
		case !slices.Contains(miss, "app"):
			s.Dropped = append(s.Dropped, fmt.Sprintf("app %q: the issue already has an app", a))
		case !slices.ContainsFunc(in.KnownApps, func(k string) bool { return strings.EqualFold(k, a) }):
			s.Dropped = append(s.Dropped, fmt.Sprintf("app %q: no repo routes that app", a))
		default:
			seen[a] = true
			s.Apps = append(s.Apps, a)
			s.Labels = append(s.Labels, applyPattern(in.Labels.App.Pattern, a))
		}
	}
	return s, nil
}

// applyPattern fills a label pattern ("type::{value}") with value.
func applyPattern(pattern, value string) string {
	for _, ph := range []string{"{value}", "{n}"} {
		if strings.Contains(pattern, ph) {
			return strings.Replace(pattern, ph, value, 1)
		}
	}
	return pattern
}

// decodeJSON decodes the first JSON value in text, tolerating a code fence
// or a sentence around it.
func decodeJSON(text string, v any) error {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		if i := strings.LastIndex(text, "```"); i >= 0 {
			text = text[:i]
		}
		text = strings.TrimSpace(text)
	}
	start := strings.IndexAny(text, "{[")
	if start < 0 {
		return errors.New("no JSON in the answer")
	}
	dec := json.NewDecoder(strings.NewReader(text[start:]))
	return dec.Decode(v)
}
