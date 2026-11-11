package billing

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/coredev-uk/ade/internal/config"
)

// Local is what a profile's sources need from this machine.
type Local struct {
	// Spend sums the profile's ledger cost in [from, to) (the omp source).
	Spend func(from, to time.Time) (float64, error)
	// WeekStart is the start of the budget week containing t.
	WeekStart func(t time.Time) time.Time
	Now       func() time.Time
	HTTP      *http.Client
}

// New builds the source a profile's billing.source names. It returns nil, nil
// for "none" or when the profile names no source and its driver has no
// natural one. Credentials are resolved here, so an unset key fails loudly
// before any request is made.
func New(ctx context.Context, p config.AIProfile, l Local) (Source, error) {
	src := p.Billing.Source
	if src == "" {
		if p.Driver == "omp" {
			src = SourceOMP
		} else {
			src = "none"
		}
	}
	if l.Now == nil {
		l.Now = time.Now
	}
	switch src {
	case "none":
		return nil, nil
	case SourceOMP:
		if l.Spend == nil || l.WeekStart == nil {
			return nil, fmt.Errorf("billing source omp needs the local ledger")
		}
		return OMP{SoftWeeklyUSD: p.Billing.WeeklySoftUSD, WeekSpend: l.Spend, WeekStart: l.WeekStart, Now: l.Now}, nil
	case SourceLiteLLM:
		base := p.Billing.BaseURL
		if base == "" {
			base = p.BaseURL
		}
		if base == "" {
			return nil, fmt.Errorf("billing source litellm needs base_url")
		}
		key, err := p.Key(ctx)
		if err != nil {
			return nil, fmt.Errorf("billing source litellm: %w", err)
		}
		return NewLiteLLM(base, key, l.HTTP), nil
	case SourceMoonshot, SourceOpenRouter:
		key, err := p.Key(ctx)
		if err != nil {
			return nil, fmt.Errorf("billing source %s: %w", src, err)
		}
		if src == SourceMoonshot {
			return NewMoonshot(p.Billing.BaseURL, key, l.HTTP), nil
		}
		return NewOpenRouter(p.Billing.BaseURL, key, l.HTTP), nil
	case SourceAnthropic, SourceOpenAI:
		key, err := p.Billing.AdminKey(ctx)
		if err != nil {
			return nil, fmt.Errorf("billing source %s needs an admin key (billing.admin_key_env or admin_key_cmd): %w", src, err)
		}
		if src == SourceAnthropic {
			return NewAnthropic(p.Billing.BaseURL, key, l.HTTP, l.Now), nil
		}
		return NewOpenAI(p.Billing.BaseURL, key, l.HTTP, l.Now), nil
	}
	return nil, fmt.Errorf("unknown billing source %q", src)
}
