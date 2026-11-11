package domain

import (
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
)

// valuePlaceholders are the tokens a label pattern may use for the varying
// part: "{value}" for facets, "{n}" for numbered sprints.
var valuePlaceholders = []string{"{value}", "{n}"}

// Facets are the label-derived dimensions of an issue.
type Facets struct {
	Type     string   // single value; lexically first when several exist
	Apps     []string // sorted, unique
	Workflow string   // see pickWorkflow
}

// FacetsOf extracts the facets from an issue's labels.
func FacetsOf(labels []string, l config.Labels) Facets {
	var f Facets
	if types := patternValues(labels, l.Type.Pattern); len(types) > 0 {
		f.Type = types[0]
	}
	f.Apps = patternValues(labels, l.App.Pattern)
	f.Workflow = pickWorkflow(patternValues(labels, l.Workflow.Pattern), l.Workflow)
	return f
}

// ParseLabels returns the type, app and workflow values carried by labels.
//
// Patterns put the varying part in "{value}" with any prefix/suffix
// ("type::{value}"); matching ignores case. GitLab Free keeps every
// scoped-looking label, so several workflow labels can coexist: the done value
// (labels.workflow.done) wins, then the one mapped to the most advanced stage
// in labels.workflow.stage; values mapped to no stage rank lowest, and ties go
// to the lexically last value. Several type labels resolve to the lexically
// first.
func ParseLabels(labels []string, l config.Labels) (typ string, apps []string, workflow string) {
	f := FacetsOf(labels, l)
	return f.Type, f.Apps, f.Workflow
}

// splitPattern cuts a pattern around its placeholder.
func splitPattern(pattern string) (prefix, suffix string, ok bool) {
	for _, ph := range valuePlaceholders {
		if before, after, found := strings.Cut(pattern, ph); found {
			return before, after, true
		}
	}
	return "", "", false
}

// matchPattern returns the value a label carries under pattern.
func matchPattern(pattern, label string) (string, bool) {
	prefix, suffix, ok := splitPattern(pattern)
	if !ok || len(label) < len(prefix)+len(suffix)+1 {
		return "", false
	}
	if !strings.EqualFold(label[:len(prefix)], prefix) || !strings.EqualFold(label[len(label)-len(suffix):], suffix) {
		return "", false
	}
	return label[len(prefix) : len(label)-len(suffix)], true
}

// patternValues returns the sorted, unique values of labels matching pattern.
func patternValues(labels []string, pattern string) []string {
	var out []string
	for _, label := range labels {
		if v, ok := matchPattern(pattern, label); ok {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// pickWorkflow chooses among simultaneous workflow values deterministically.
func pickWorkflow(values []string, wf config.WorkflowFacet) string {
	best, bestRank := "", -2
	for _, v := range values {
		// values are sorted, so >= makes ties resolve to the lexically last.
		if r := workflowRank(v, wf); r >= bestRank {
			best, bestRank = v, r
		}
	}
	return best
}

// workflowRank is the most advanced stage whose mapped workflow value is v
// (StageDone for the done value), or -1 when no stage maps to it.
func workflowRank(v string, wf config.WorkflowFacet) int {
	if wf.Done != "" && strings.EqualFold(v, wf.Done) {
		return int(StageDone)
	}
	rank := -1
	for key, val := range wf.Stage {
		if !strings.EqualFold(val, v) {
			continue
		}
		if s, ok := parseStage(key); ok && int(s) > rank {
			rank = int(s)
		}
	}
	return rank
}

// WorkflowLabel renders a workflow value as its label ("workflow::done").
func WorkflowLabel(value string, l config.Labels) string {
	prefix, suffix, ok := splitPattern(l.Workflow.Pattern)
	if !ok {
		return value
	}
	return prefix + value + suffix
}

// parseStage maps a stage name ("review") to its Stage.
func parseStage(name string) (Stage, bool) {
	for s := StageAssigned; s <= StageDone; s++ {
		if strings.EqualFold(s.String(), name) {
			return s, true
		}
	}
	return 0, false
}

// WantWorkflow is the workflow value stage maps to in labels.workflow.stage,
// or "" when the manifest maps nothing for it.
func WantWorkflow(stage Stage, l config.Labels) string {
	for key, val := range l.Workflow.Stage {
		if s, ok := parseStage(key); ok && s == stage {
			return val
		}
	}
	return ""
}
