package domain

import (
	"slices"
	"testing"
)

func TestSprintLabels(t *testing.T) {
	got := SprintLabels([]string{"type::story", "Sprint::41", "Sprint::042", "Sprint::0", "Sprint::next", "Sprint::7"}, "Sprint::{n}")
	if want := []string{"Sprint::41", "Sprint::7"}; !slices.Equal(got, want) {
		t.Errorf("SprintLabels = %v, want %v", got, want)
	}
}
