package onboard

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
)

// DefaultGatewayProfile is the profile name `ade init --ai-gateway` writes.
const DefaultGatewayProfile = "gateway"

// AIGateway is the AI half of the answers: one OpenAI-compatible LiteLLM
// gateway profile. The key is never stored, only the variable or command that
// yields it.
type AIGateway struct {
	Profile string // key under [ai.profiles]; DefaultGatewayProfile when empty
	BaseURL string // the OpenAI-style base, ending in /v1
	KeyEnv  string
	KeyCmd  string
	// Models maps a role to a model alias of the gateway. explain is
	// required; classify, draft and review fall back as config.AIProfile.Model says.
	Models map[string]string
}

// ProfileName is the profile key, defaulted.
func (g AIGateway) ProfileName() string {
	if g.Profile == "" {
		return DefaultGatewayProfile
	}
	return g.Profile
}

// NormalizeGatewayURL accepts "litellm.example.internal:4000", a full URL, or
// one with a trailing slash, and returns the base URL ending in /v1 (LiteLLM's
// OpenAI-compatible root). A URL that already has a path is kept as given.
func NormalizeGatewayURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("the gateway URL is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("gateway URL %q: want an http(s) URL such as https://litellm.example.internal/v1", raw)
	}
	if u.User != nil {
		return "", errors.New("the gateway URL must not carry credentials: use --ai-key-env or --ai-key-cmd")
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/v1"
	}
	return u.String(), nil
}

// Validate reports the first problem that would produce a config ADE rejects.
func (g AIGateway) Validate() error {
	if u, err := url.Parse(g.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("gateway URL %q: want an http(s) URL (NormalizeGatewayURL builds one)", g.BaseURL)
	}
	if g.KeyEnv == "" && g.KeyCmd == "" {
		return errors.New("no AI key source: give --ai-key-env (a variable name) or --ai-key-cmd")
	}
	if g.Models["explain"] == "" {
		return errors.New("no explain model: pass --ai-model-explain (classify, draft and review fall back to it)")
	}
	for role, m := range g.Models {
		if !slices.Contains(config.AIRoles, role) {
			return fmt.Errorf("unknown AI role %q: want one of %v", role, config.AIRoles)
		}
		if strings.TrimSpace(m) == "" {
			return fmt.Errorf("the %s model is empty", role)
		}
	}
	return nil
}

// AIProfile is the config entry the answers describe.
func (g AIGateway) AIProfile() config.AIProfile {
	return config.AIProfile{
		Driver: "openai-compat", BaseURL: g.BaseURL, KeyEnv: g.KeyEnv, KeyCmd: g.KeyCmd,
		Models: g.Models, Billing: config.AIBilling{Source: "litellm"},
	}
}

type fileAI struct {
	Mode     string                 `toml:"mode"`
	Profile  string                 `toml:"profile"`
	Profiles map[string]fileProfile `toml:"profiles"`
}

type fileProfile struct {
	Driver  string            `toml:"driver"`
	BaseURL string            `toml:"base_url"`
	KeyEnv  string            `toml:"key_env,omitempty"`
	KeyCmd  string            `toml:"key_cmd,omitempty"`
	Models  map[string]string `toml:"models"`
	Billing fileBilling       `toml:"billing"`
}

type fileBilling struct {
	Source string `toml:"source"`
}

// file is the written form; nil for no gateway.
func (g *AIGateway) file() *fileAI {
	if g == nil {
		return nil
	}
	return &fileAI{
		Mode:    "ask",
		Profile: g.ProfileName(),
		Profiles: map[string]fileProfile{g.ProfileName(): {
			Driver: "openai-compat", BaseURL: g.BaseURL, KeyEnv: g.KeyEnv, KeyCmd: g.KeyCmd,
			Models: g.Models, Billing: fileBilling{Source: "litellm"},
		}},
	}
}
