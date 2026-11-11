package config

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
)

// Valid values for the AI tables, shared with the packages that switch on them.
var (
	AIDrivers       = []string{"omp", "anthropic", "openai-compat"}
	AIBillingSource = []string{"litellm", "omp", "anthropic", "openai", "moonshot", "openrouter", "none"}
	AgentDrivers    = []string{"omp", "claude", "codex"}
	AIRoles         = []string{"explain", "draft", "plan", "implement", "review"}
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
	if p.Billing.WeeklySoftUSD < 0 {
		s.errorf(path+".billing.weekly_soft_usd", "must not be negative")
	}
}
