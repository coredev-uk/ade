package ai

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestHumanWordsEachFailureAsACauseAndARecovery(t *testing.T) {
	_, execErr := exec.LookPath("omp-not-installed-for-test")
	if execErr == nil {
		t.Fatal("the probe command exists")
	}
	_, badErr := ParseSuggestion("not json at all", classifyIn("x", ""))
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"missing command", &NotInstalledError{Driver: "omp", Err: execErr}, "omp is not installed · install it or set ai.mode = off"},
		{"missing command, unwrapped", fmt.Errorf("run: %w", execErr), "is not installed"},
		{"local cap", &StopError{Source: "local", Message: "daily cap reached"}, "spending cap in ADE"},
		{"provider budget", &StopError{Source: "gateway", Message: "Budget has been exceeded"}, "provider's own budget"},
		{"unusable answer", badErr, "answer was not usable · try again"},
		{"timeout", fmt.Errorf("omp: %w", context.DeadlineExceeded), "took too long"},
		{"declined", ErrCancelled, "nothing was sent"},
		{"anything else", errors.New("boom: HTTP 500"), "the AI call failed"},
	}
	for _, c := range cases {
		got := Human(c.err)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %q lacks %q", c.name, got, c.want)
		}
		if strings.Contains(got, "exec:") || strings.Contains(got, "HTTP 500") || strings.Contains(got, "$PATH") {
			t.Errorf("%s: the raw error leaked: %q", c.name, got)
		}
	}
}

func TestNotInstalledErrorKeepsTheRawTextForTheLog(t *testing.T) {
	_, execErr := exec.LookPath("omp-not-installed-for-test")
	err := error(&NotInstalledError{Driver: "omp", Err: execErr})
	if !strings.Contains(err.Error(), "executable file not found") || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestKeywordSuggestionSaysWhatItCouldNotFind(t *testing.T) {
	// A type match and no app: the reason names the app miss.
	s := SuggestKeywords(classifyIn("Export crashes", "totals are wrong"))
	if s.Type != "bug" || len(s.Apps) != 0 || !strings.Contains(s.Reason, "no app keyword matched") {
		t.Fatalf("%+v", s)
	}
	// Nothing at all: both are named, and the reason is never empty.
	s = SuggestKeywords(classifyIn("Hmm", "?"))
	if s.Reason != "no type or app keyword matched" {
		t.Fatalf("%q", s.Reason)
	}
	// Everything found: no miss is claimed.
	s = SuggestKeywords(classifyIn("Export endpoint crashes", "The api returns 500."))
	if strings.Contains(s.Reason, "no ") {
		t.Fatalf("%q", s.Reason)
	}
}
