package domain

import (
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
)

const defaultWeightPattern = "weight::{value}"

// ResolveWeight returns the issue's weight under src, or nil when unweighted.
// It never returns a pointer to a non-positive value: 0 means "unset" on
// GitLab, so it is normalised to nil.
//
//   - gitlab, github-field: the forge's weight field (drivers fill Issue.Weight).
//   - label: the largest integer carried by labels matching src.Pattern
//     (default "weight::{value}"); the largest wins when several exist.
//   - none: always nil.
//   - auto (or empty): the field when set, else the label.
func ResolveWeight(issue forge.Issue, src config.WeightSource) *int {
	switch src.Source {
	case "none":
		return nil
	case "gitlab", "github-field":
		return positive(issue.Weight)
	case "label":
		return labelWeight(issue.Labels, src.Pattern)
	default:
		if w := positive(issue.Weight); w != nil {
			return w
		}
		return labelWeight(issue.Labels, src.Pattern)
	}
}

func positive(w *int) *int {
	if w == nil || *w <= 0 {
		return nil
	}
	v := *w
	return &v
}

func labelWeight(labels []string, pattern string) *int {
	if pattern == "" {
		pattern = defaultWeightPattern
	}
	var best *int
	for _, v := range patternValues(labels, pattern) {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			continue
		}
		if best == nil || n > *best {
			best = &n
		}
	}
	return best
}

// WeightLabel is the label that carries weight n under pattern (default
// "weight::{value}").
func WeightLabel(pattern string, n int) string {
	if pattern == "" {
		pattern = defaultWeightPattern
	}
	for _, ph := range valuePlaceholders {
		if strings.Contains(pattern, ph) {
			return strings.Replace(pattern, ph, strconv.Itoa(n), 1)
		}
	}
	return pattern
}

// WeightLabels returns the labels that match the weight pattern, whatever
// value they carry: the ones a re-weight must remove explicitly, since "::"
// labels are not exclusive on every tier. Labels whose value is not a number
// are left alone.
func WeightLabels(labels []string, pattern string) []string {
	if pattern == "" {
		pattern = defaultWeightPattern
	}
	var out []string
	for _, l := range labels {
		if v, ok := matchPattern(pattern, l); ok {
			if _, err := strconv.Atoi(v); err == nil {
				out = append(out, l)
			}
		}
	}
	return out
}
