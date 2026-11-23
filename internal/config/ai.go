package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// Why AI cannot be used right now, as errors the AI layer and the screens
// word for a person (ai.Human). ErrNoSecret wraps a missing or empty key.
var (
	ErrNoProfile        = errors.New("no AI profile selected: set ai.profile in the config")
	ErrProfileUndefined = errors.New("AI profile is not defined")
	ErrNoSecret         = errors.New("no key configured")
)

// AIState is where the AI settings stand, from "off" to "usable".
type AIState int

const (
	AIReady     AIState = iota // mode is not off and ai.profile names a defined profile
	AIOff                      // ai.mode = off
	AINone                     // no profile is configured at all: a machine without AI
	AINoProfile                // profiles exist but ai.profile selects none
	AIUnknown                  // ai.profile names a profile that is not defined
)

// State classifies the AI settings. It reads the config only: no key, binary
// or network is touched, so it is cheap enough to ask on every redraw.
func (a AI) State() AIState {
	switch {
	case a.Mode == "off":
		return AIOff
	case len(a.Profiles) == 0:
		return AINone
	case a.Profile == "":
		return AINoProfile
	}
	if _, ok := a.Profiles[a.Profile]; !ok {
		return AIUnknown
	}
	return AIReady
}

// Available is the one answer to "can ADE offer AI here": every screen, key
// hint and automatic call asks it instead of looking at mode and profiles
// itself. reason says which setting is in the way when ok is false.
func (a AI) Available() (ok bool, reason string) {
	switch a.State() {
	case AIReady:
		return true, ""
	case AIOff:
		return false, "AI is off (ai.mode = off)"
	case AINone:
		return false, "no AI profile is configured (see [ai.profiles] in the config)"
	case AINoProfile:
		return false, "no AI profile is selected: set ai.profile in the config"
	}
	return false, fmt.Sprintf("the AI profile %q is not defined (have: %v)", a.Profile, slices.Sorted(maps.Keys(a.Profiles)))
}

// ActiveProfile returns the profile ai.profile names. It errors when AI is off
// or no profile is selected, with a message saying which key to set.
func (c *Config) ActiveProfile() (string, AIProfile, error) {
	if c.AI.Mode == "off" {
		return "", AIProfile{}, errors.New("ai.mode is off")
	}
	return c.Profile(c.AI.Profile)
}

// Profile returns the named profile; an empty name means the active one.
func (c *Config) Profile(name string) (string, AIProfile, error) {
	if name == "" {
		name = c.AI.Profile
	}
	if name == "" {
		return "", AIProfile{}, ErrNoProfile
	}
	p, ok := c.AI.Profiles[name]
	if !ok {
		return "", AIProfile{}, fmt.Errorf("AI profile %q is not defined (have: %v): %w", name, slices.Sorted(maps.Keys(c.AI.Profiles)), ErrProfileUndefined)
	}
	return name, p, nil
}

// Model returns the model id for role. A review role without its own model
// uses draft's; a role without one (classify included) falls back to explain.
func (p AIProfile) Model(role string) (string, error) {
	m, _, err := p.ModelFrom(role)
	return m, err
}

// ModelFrom is Model that also names the role whose entry supplied the id:
// role itself, or the one it fell back to.
func (p AIProfile) ModelFrom(role string) (model, from string, err error) {
	chain := []string{role}
	switch role {
	case "review":
		chain = append(chain, "draft", "explain")
	case "explain":
	default:
		chain = append(chain, "explain")
	}
	for _, r := range chain {
		if m := p.Models[r]; m != "" {
			return m, r, nil
		}
	}
	return "", "", fmt.Errorf("no model configured for role %q: set models.%s on the profile", role, role)
}

// Key resolves the profile's API key from KeyEnv or KeyCmd. The omp driver
// needs none (omp holds its own login).
func (p AIProfile) Key(ctx context.Context) (string, error) {
	return secret(ctx, "key", p.KeyEnv, p.KeyCmd)
}

// AdminKey resolves the organisation key some billing sources need.
func (b AIBilling) AdminKey(ctx context.Context) (string, error) {
	return secret(ctx, "admin_key", b.AdminKeyEnv, b.AdminKeyCmd)
}

// noSecret is an unset or empty secret: its text is the specific cause, and
// it matches ErrNoSecret.
type noSecret string

func (e noSecret) Error() string      { return string(e) }
func (noSecret) Is(target error) bool { return target == ErrNoSecret }

func secret(ctx context.Context, what, env, cmd string) (string, error) {
	if env != "" {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, nil
		}
		if cmd == "" {
			return "", noSecret(fmt.Sprintf("%s_env %s is empty", what, env))
		}
	}
	if cmd == "" {
		return "", noSecret(fmt.Sprintf("no %s_cmd or %s_env configured", what, what))
	}
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("%s_cmd failed: %w: %s", what, err, strings.TrimSpace(stderr.String()))
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", noSecret(fmt.Sprintf("%s_cmd produced no output", what))
	}
	return v, nil
}
