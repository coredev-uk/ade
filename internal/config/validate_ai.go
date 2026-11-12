package config

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
)

// Valid values for the AI tables, shared with the packages that switch on them.
var (
	AIDrivers       = []string{"omp", "anthropic", "openai-compat"}
	AIBillingSource = []string{"litellm", "omp", "anthropic", "openai", "moonshot", "openrouter", "none"}
	AgentDrivers    = []string{"omp", "claude", "codex"}
	AIRoles         = []string{"explain", "classify", "draft", "plan", "implement", "review"}
)

func validateAI(s *issues, c *Config) {
	if !oneOf(c.AI.Mode, "off", "ask", "auto") {
		s.errorf("ai.mode", "unknown value %q: want off, ask or auto", c.AI.Mode)
	}
	if c.AI.Profile != "" {
		if _, ok := c.AI.Profiles[c.AI.Profile]; !ok {
			s.errorf("ai.profile", "profile %q is not defined under [ai.profiles]", c.AI.Profile)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.AI.Profiles)) {
		validateProfile(s, "ai.profiles."+name, c.AI.Profiles[name])
	}
	b := c.Budget
	for _, f := range []struct {
		key string
		v   float64
	}{
		{"daily_usd", b.DailyUSD},
		{"weekly_usd", b.WeeklyUSD},
		{"sprint_usd", b.SprintUSD},
		{"call_usd", b.CallUSD},
		{"confirm_over_usd", b.ConfirmOverUSD},
		{"agent_run_usd", b.AgentRunUSD},
	} {
		if f.v < 0 {
			s.errorf("budget."+f.key, "must not be negative")
		}
	}
}

func validateProfile(s *issues, path string, p AIProfile) {
	if !oneOf(p.Driver, AIDrivers...) {
		s.errorf(path+".driver", "unknown driver %q: want omp, anthropic or openai-compat", p.Driver)
	}
	if p.Driver == "openai-compat" && p.BaseURL == "" {
		s.errorf(path+".base_url", "required for the openai-compat driver")
	}
	if u, err := url.Parse(p.BaseURL); p.BaseURL != "" && (err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "") {
		s.errorf(path+".base_url", "%q is not an http(s) URL", p.BaseURL)
	}
	if p.Driver != "omp" && p.KeyCmd == "" && p.KeyEnv == "" {
		s.warnf(path, "no key_cmd or key_env: calls will be unauthenticated and fail")
	}
	if p.Agent != "" && !oneOf(p.Agent, AgentDrivers...) {
		s.errorf(path+".agent", "unknown agent %q: want omp, claude or codex", p.Agent)
	}
	for role := range p.Models {
		if !oneOf(role, AIRoles...) {
			s.errorf(path+".models."+role, "unknown role: want one of %v", AIRoles)
		}
	}
	for model, pr := range p.Prices {
		if pr.Input < 0 || pr.Output < 0 || pr.CacheRead < 0 || pr.CacheWrite < 0 {
			s.errorf(fmt.Sprintf("%s.prices.%s", path, model), "prices must not be negative")
		}
	}
	if p.Billing.Source != "" && !oneOf(p.Billing.Source, AIBillingSource...) {
		s.errorf(path+".billing.source", "unknown source %q: want one of %v", p.Billing.Source, AIBillingSource)
	}
	for role, model := range p.Models {
		if strings.TrimSpace(model) == "" {
			s.errorf(path+".models."+role, "model id must not be empty")
		}
	}
	validateGateway(s, path, p)
	if p.Billing.WeeklySoftUSD < 0 {
		s.errorf(path+".billing.weekly_soft_usd", "must not be negative")
	}
}

// validateGateway checks the settings an HTTP driver (a LiteLLM gateway in
// particular) adds to a profile.
func validateGateway(s *issues, path string, p AIProfile) {
	if p.Timeout.Duration < 0 {
		s.errorf(path+".timeout", "must not be negative")
	}
	if p.Driver == "omp" && (len(p.ExtraHeaders) > 0 || p.Timeout.Duration != 0) {
		s.warnf(path, "extra_headers and timeout only apply to the HTTP drivers; the omp driver ignores them")
	}
	for _, name := range slices.Sorted(maps.Keys(p.ExtraHeaders)) {
		hp := path + ".extra_headers." + name
		switch {
		case !validHeaderName(name):
			s.errorf(hp, "%q is not a valid HTTP header name", name)
		case strings.EqualFold(name, "authorization"):
			s.errorf(hp, "Authorization is set from key_env/key_cmd; do not repeat it here")
		case strings.ContainsAny(p.ExtraHeaders[name], "\r\n\x00"):
			s.errorf(hp, "the value is not a valid HTTP header value")
		}
	}
	if p.Billing.Source == "litellm" {
		switch {
		case p.Driver != "openai-compat":
			s.errorf(path+".billing.source", "litellm billing reads a LiteLLM gateway: use driver = \"openai-compat\"")
		case p.KeyEnv == "" && p.KeyCmd == "":
			s.errorf(path, "a litellm gateway needs key_env or key_cmd (the virtual key)")
		}
		if len(p.Models) == 0 {
			s.warnf(path+".models", "no models: set at least explain (and classify for a small classifier) to name the gateway's model aliases")
		}
		if u, err := url.Parse(p.BaseURL); err == nil && u.Host != "" && strings.Trim(u.Path, "/") == "" {
			s.warnf(path+".base_url", "%q has no path: LiteLLM serves the OpenAI API under /v1", p.BaseURL)
		}
	}
}

// validHeaderName reports whether name is an RFC 9110 token.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}
