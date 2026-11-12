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
		return "", AIProfile{}, errors.New("no AI profile selected: set ai.profile in the config")
	}
	p, ok := c.AI.Profiles[name]
	if !ok {
		return "", AIProfile{}, fmt.Errorf("AI profile %q is not defined (have: %v)", name, slices.Sorted(maps.Keys(c.AI.Profiles)))
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

func secret(ctx context.Context, what, env, cmd string) (string, error) {
	if env != "" {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, nil
		}
		if cmd == "" {
			return "", fmt.Errorf("%s_env %s is empty", what, env)
		}
	}
	if cmd == "" {
		return "", fmt.Errorf("no %s_cmd or %s_env configured", what, what)
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
		return "", fmt.Errorf("%s_cmd produced no output", what)
	}
	return v, nil
}
