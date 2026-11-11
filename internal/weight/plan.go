// Package weight is the explicit, audited re-weight of an issue (PLAN 3,
// "Weights"): `ade issue weight` and the TUI's `=` both plan with BuildPlan,
// show the plan, and write it with Apply. Where the forge licenses a weight
// field (GitLab Premium) the field is written; elsewhere the weight rides on
// a label, and the old weight labels are removed explicitly because "::"
// labels are not exclusive on every tier.
package weight

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

// Modes of a plan: how the weight is stored.
const (
	ModeField = "field"
	ModeLabel = "label"
)

// Plan is one issue's weight change.
type Plan struct {
	Ref  forge.Ref `json:"ref"`
	Mode string    `json:"mode"`
	// From and To are the weight before and after; nil is unweighted.
	From *int `json:"from"`
	To   *int `json:"to"`
	// Add and Remove are the label changes of a label plan.
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
	// Unchanged is true when the issue already is as asked: nothing is written.
	Unchanged bool `json:"unchanged,omitempty"`
	// Child marks the child issue of the tracker item that started work.
	Child bool     `json:"child,omitempty"`
	Notes []string `json:"notes,omitempty"`
}

// Value renders a weight: "3", or "none".
func Value(w *int) string {
	if w == nil {
		return "none"
	}
	return fmt.Sprint(*w)
}

// BuildPlan plans setting the issue's weight to to (nil clears it). The
// storage follows src: "gitlab" is the field, "label" the label pattern, and
// "auto" (or empty) the field where caps say the forge licenses it, else the
// label.
func BuildPlan(is forge.Issue, caps forge.Caps, src config.WeightSource, to *int) (Plan, error) {
	if to != nil && *to < 1 {
		return Plan{}, errors.New("a weight is 1 or more; use \"none\" to clear it (0 would read as unweighted)")
	}
	p := Plan{Ref: is.Ref, From: domain.ResolveWeight(is, src), To: to}
	var field bool
	switch src.Source {
	case "none":
		return Plan{}, errors.New("tracker.weight.source is \"none\": this project does not track weights")
	case "github-field":
		return Plan{}, errors.New("tracker.weight.source \"github-field\" is read-only here: ADE writes weights as labels or the GitLab field")
	case "gitlab":
		if !caps.Weight {
			return Plan{}, fmt.Errorf("tracker.weight.source is \"gitlab\" but the forge does not license issue weight for %s: use source \"label\" or \"auto\"", is.Ref.Project)
		}
		field = true
	case "label":
	default: // auto
		field = caps.Weight
	}
	if field {
		p.Mode = ModeField
		p.Unchanged = equalWeight(positive(is.Weight), to)
		if labels := domain.WeightLabels(is.Labels, src.Pattern); len(labels) > 0 && !p.Unchanged {
			p.Notes = append(p.Notes, "weight labels stay as they are ("+strings.Join(labels, ", ")+"); the field wins while it is set")
		}
		return p, nil
	}
	p.Mode = ModeLabel
	old := domain.WeightLabels(is.Labels, src.Pattern)
	var want string
	if to != nil {
		want = domain.WeightLabel(src.Pattern, *to)
	}
	for _, l := range old {
		if !strings.EqualFold(l, want) {
			p.Remove = append(p.Remove, l)
		}
	}
	if want != "" && !slices.ContainsFunc(old, func(l string) bool { return strings.EqualFold(l, want) }) {
		p.Add = []string{want}
	}
	p.Unchanged = len(p.Add) == 0 && len(p.Remove) == 0
	return p, nil
}

func positive(w *int) *int {
	if w == nil || *w <= 0 {
		return nil
	}
	return w
}

func equalWeight(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Describe is the plan in one line, for dry runs and confirmations.
func (p Plan) Describe() string {
	who := p.Ref.String()
	if p.Child {
		who += " (child)"
	}
	if p.Unchanged {
		return fmt.Sprintf("%s: weight already %s; nothing to write", who, Value(p.To))
	}
	switch p.Mode {
	case ModeField:
		return fmt.Sprintf("%s: weight %s → %s (field)", who, Value(p.From), Value(p.To))
	default:
		var parts []string
		if len(p.Add) > 0 {
			parts = append(parts, "add "+strings.Join(p.Add, ", "))
		}
		if len(p.Remove) > 0 {
			parts = append(parts, "remove "+strings.Join(p.Remove, ", "))
		}
		return fmt.Sprintf("%s: weight %s → %s (label: %s)", who, Value(p.From), Value(p.To), strings.Join(parts, "; "))
	}
}
