package config

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// One predicate answers "can ADE offer AI here": off, no profile configured,
// none selected and a selected one that is not defined are told apart.
func TestAIAvailableTellsTheStatesApart(t *testing.T) {
	prof := map[string]AIProfile{"sub": {Driver: "omp"}}
	for _, tc := range []struct {
		name   string
		ai     AI
		state  AIState
		ok     bool
		reason string
	}{
		{"usable", AI{Mode: "ask", Profile: "sub", Profiles: prof}, AIReady, true, ""},
		{"auto is usable", AI{Mode: "auto", Profile: "sub", Profiles: prof}, AIReady, true, ""},
		{"off", AI{Mode: "off", Profile: "sub", Profiles: prof}, AIOff, false, "ai.mode = off"},
		{"off without profiles", AI{Mode: "off"}, AIOff, false, "ai.mode = off"},
		{"none configured", AI{Mode: "ask"}, AINone, false, "no AI profile is configured"},
		{"none selected", AI{Mode: "ask", Profiles: prof}, AINoProfile, false, "set ai.profile"},
		{"selected is missing", AI{Mode: "ask", Profile: "gone", Profiles: prof}, AIUnknown, false, `"gone" is not defined`},
	} {
		if got := tc.ai.State(); got != tc.state {
			t.Errorf("%s: state %v, want %v", tc.name, got, tc.state)
		}
		ok, why := tc.ai.Available()
		if ok != tc.ok || (tc.reason == "") != (why == "") || !strings.Contains(why, tc.reason) {
			t.Errorf("%s: Available() = %v, %q", tc.name, ok, why)
		}
	}
}

func TestProfileErrorsAreSentinels(t *testing.T) {
	c := Defaults()
	if _, _, err := c.Profile(""); !errors.Is(err, ErrNoProfile) {
		t.Errorf("no selection: %v", err)
	}
	c.AI.Profile = "gone"
	if _, _, err := c.Profile(""); !errors.Is(err, ErrProfileUndefined) {
		t.Errorf("undefined: %v", err)
	}
	t.Setenv("ADE_TEST_EMPTY_KEY", "")
	for name, p := range map[string]AIProfile{
		"nothing configured": {},
		"empty env":          {KeyEnv: "ADE_TEST_EMPTY_KEY"},
		"silent command":     {KeyCmd: "true"},
	} {
		if _, err := p.Key(context.Background()); !errors.Is(err, ErrNoSecret) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := (AIProfile{KeyCmd: "exit 3"}).Key(context.Background()); err == nil || errors.Is(err, ErrNoSecret) {
		t.Errorf("a failing key command is not a missing key: %v", err)
	}
}

func TestValidateWarnsWhenProfilesExistButNoneIsSelected(t *testing.T) {
	c := Defaults()
	c.AI.Profiles = map[string]AIProfile{"sub": {Driver: "omp"}}
	found := false
	for _, is := range c.Validate() {
		found = found || is.Path == "ai.profile" && strings.Contains(is.Msg, "none is selected")
	}
	if !found {
		t.Error("no warning for profiles without a selection")
	}
}
