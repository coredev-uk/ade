// Package ai is ADE's AI layer: drivers (omp, anthropic, openai-compatible),
// pre-flight estimates and caps, the content-hash result cache backed by item
// records, and the features built on them (summarize, classify, draft-reply,
// review). Every call, cache hits included, leaves an `ai.*` audit entry; the
// spend ledger is a query over those entries.
package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coredev-uk/ade/internal/billing"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/httpdebug"
)

// Role is what a call is for; a profile maps each role to a model.
type Role string

const (
	RoleExplain   Role = "explain"   // summaries, thread summaries, standup, sprint text
	RoleClassify  Role = "classify"  // short structured answers (label suggestions); falls back to explain
	RoleDraft     Role = "draft"     // replies, MR text
	RolePlan      Role = "plan"      // agent plans
	RoleImplement Role = "implement" // agent runs
	RoleReview    Role = "review"    // agent review passes
)

// Cost sources recorded on audit entries.
const (
	CostReported = "reported" // the driver said (omp usage.cost.total)
	CostComputed = "computed" // tokens × the profile's price table
	CostGateway  = "gateway"  // x-litellm-response-cost
	CostUnknown  = "unknown"  // no price known: counted as $0, flagged
)

// Call is one model request.
type Call struct {
	Model  string
	System string
	Prompt string
	// Thinking is the reasoning level for drivers that take one (omp
	// --thinking); empty leaves the model's default.
	Thinking  string
	MaxTokens int // output cap; 0 = driver default
	// MaxCostUSD hard-stops the call where the driver can (omp --max-cost).
	MaxCostUSD float64
	// Schema asks for a JSON answer of this shape where the driver can
	// enforce it (OpenAI-style response_format). Drivers or gateways that
	// cannot simply answer in text, which the caller parses either way.
	Schema *Schema
}

// Schema is the JSON Schema of a structured answer.
type Schema struct {
	Name string         // identifier sent with the schema, e.g. "classify"
	JSON map[string]any // the schema document
}

// Usage is the token accounting of a response.
type Usage struct {
	In    int
	Out   int
	Cache int // cache-read tokens
}

// Response is a finished call.
type Response struct {
	Text       string
	Model      string // as the provider reports it
	Usage      Usage
	CostUSD    float64
	CostSource string
	DestHost   string // where the prompt went, for the audit trail (4.3)
	Bytes      int    // request body size
	// Structured is true when the call carried a Schema and the endpoint
	// accepted it; false when none was asked for or the endpoint refused it
	// and the answer is free text.
	Structured bool
	// Gateway carries LiteLLM's per-request key figures when present.
	Gateway *GatewayInfo
}

// GatewayInfo is what LiteLLM's response headers say about the key after the
// call (x-litellm-key-spend, x-litellm-key-max-budget).
type GatewayInfo struct {
	KeySpend     *float64
	KeyMaxBudget *float64
}

// Driver sends one call to a model provider.
type Driver interface {
	// Name is the config driver name.
	Name() string
	Complete(ctx context.Context, c Call) (Response, error)
	// Prices returns the provider's own per-model prices (USD per million
	// tokens), if it publishes any. Models without a published price are
	// omitted.
	Prices(ctx context.Context) (map[string]config.Price, error)
	// Models lists the model ids the provider offers.
	Models(ctx context.Context) ([]string, error)
}

// StopError is a budget stop: a provider refusing a call because a budget is
// used up, or ADE's own caps refusing it before it was sent.
type StopError struct {
	// Source is who stopped it: gateway, provider, omp or local.
	Source  string
	Message string
}

func (e *StopError) Error() string { return fmt.Sprintf("budget stop (%s): %s", e.Source, e.Message) }

// AsStop returns err's *StopError, if any.
func AsStop(err error) (*StopError, bool) {
	var s *StopError
	ok := errors.As(err, &s)
	return s, ok
}

// DriverFor builds the driver for a profile. Tests replace the Service's
// Open hook with their own; this is the production one.
func DriverFor(ctx context.Context, p config.AIProfile, hc *http.Client) (Driver, error) {
	switch p.Driver {
	case "omp":
		return newOMP(p), nil
	case "anthropic", "openai-compat":
		key, err := p.Key(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s driver: %w", p.Driver, err)
		}
		if hc == nil {
			hc = &http.Client{Timeout: 5 * time.Minute, Transport: httpdebug.Transport(nil)}
		}
		if p.Timeout.Duration > 0 {
			c := *hc
			c.Timeout = p.Timeout.Duration
			hc = &c
		}
		hc = billing.WithHeaders(hc, p.ExtraHeaders)
		if p.Driver == "anthropic" {
			return newAnthropic(p, key, hc), nil
		}
		return newOpenAI(p, key, hc), nil
	}
	return nil, fmt.Errorf("unknown driver %q", p.Driver)
}
