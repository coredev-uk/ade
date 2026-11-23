package ai

import (
	"strings"
	"testing"
)

func TestPickableLabelsListsOnlyTheMissingFacetsKnownValues(t *testing.T) {
	in := classifyIn("Hmm", "?")
	if got := strings.Join(PickableLabels(in), ","); got != "type::bug,type::chore,type::story,app::api,app::web" {
		t.Errorf("both missing: %s", got)
	}
	// An app label already there: only types are offered.
	in = classifyIn("Hmm", "?", "app::api")
	if got := strings.Join(PickableLabels(in), ","); got != "type::bug,type::chore,type::story" {
		t.Errorf("type missing: %s", got)
	}
	// No known types: the three conventional ones, as the keyword path uses.
	in = classifyIn("Hmm", "?", "app::api")
	in.KnownTypes = nil
	if got := strings.Join(PickableLabels(in), ","); got != "type::bug,type::chore,type::story" {
		t.Errorf("no known types: %s", got)
	}
	// Nothing missing: nothing to pick.
	if got := PickableLabels(classifyIn("Hmm", "?", "app::api", "type::bug")); len(got) != 0 {
		t.Errorf("complete issue: %v", got)
	}
}
