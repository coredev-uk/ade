// Package billing reads spend and budget from the provider behind each AI
// profile (docs/billing). A Source returns one authoritative Snapshot; the
// displayed figure is that snapshot plus the local ledger since the
// snapshot's AsOf (Merge). Every source is read-only and never sees prompts.
package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coredev-uk/ade/internal/httpdebug"
)

// Source names, as in config AIBilling.Source.
const (
	SourceLiteLLM    = "litellm"
	SourceOMP        = "omp"
	SourceAnthropic  = "anthropic"
	SourceOpenAI     = "openai"
	SourceMoonshot   = "moonshot"
	SourceOpenRouter = "openrouter"
)

// Snapshot is what a provider reported at AsOf. Nil fields are unknown, never
// zero: a missing cap is "no cap known", a missing spend is "not reported".
type Snapshot struct {
	Source string    `json:"source"`
	AsOf   time.Time `json:"as_of"`
	// Scope says which budget the figures describe: key, team, user, org,
	// account, or local (omp: ADE's own ledger).
	Scope     string     `json:"scope,omitempty"`
	Spend     *float64   `json:"spend_usd,omitempty"`      // spend in the current window
	Limit     *float64   `json:"limit_usd,omitempty"`      // hard cap (max_budget)
	SoftLimit *float64   `json:"soft_limit_usd,omitempty"` // alert threshold; never blocks
	Remaining *float64   `json:"remaining_usd,omitempty"`
	Balance   *float64   `json:"balance_usd,omitempty"` // prepaid balance (moonshot)
	Window    string     `json:"window,omitempty"`      // budget_duration, "month", "week"
	ResetAt   *time.Time `json:"reset_at,omitempty"`
	// Note is one line of caveat for the page: "org cost, 4–24 h lag".
	Note string `json:"note,omitempty"`
}

// RemainingUSD is what is left under the cap: the provider's own figure when
// it gave one, else limit − spend.
func (s Snapshot) RemainingUSD() (float64, bool) {
	switch {
	case s.Remaining != nil:
		return *s.Remaining, true
	case s.Limit != nil && s.Spend != nil:
		return *s.Limit - *s.Spend, true
	}
	return 0, false
}

// Source reads one provider's authoritative figures.
type Source interface {
	// Name is the billing.source value.
	Name() string
	// Interval is how often the snapshot is worth refreshing (docs/billing).
	Interval() time.Duration
	// Fetch makes the read-only provider call(s).
	Fetch(ctx context.Context) (Snapshot, error)
}

// Merged is the figure the budget page shows: the snapshot plus what ADE's
// own ledger spent after it.
type Merged struct {
	Snapshot   Snapshot `json:"snapshot"`
	LocalDelta float64  `json:"local_delta_usd"` // ledger spend since Snapshot.AsOf
	// Spend is Snapshot.Spend + LocalDelta; nil when the provider reports no
	// spend (the page then shows the ledger alone).
	Spend     *float64 `json:"spend_usd,omitempty"`
	Remaining *float64 `json:"remaining_usd,omitempty"`
}

// Merge applies the merge rule: displayed spend = last authoritative
// snapshot + local ledger since that snapshot's AsOf.
func Merge(s Snapshot, localSince float64) Merged {
	m := Merged{Snapshot: s, LocalDelta: localSince}
	if s.Spend != nil {
		m.Spend = new(*s.Spend + localSince)
	}
	if left, ok := s.RemainingUSD(); ok {
		m.Remaining = new(left - localSince)
	}
	return m
}

// Error is a failed provider read. Status is the HTTP status, 0 for a
// transport failure; Body is a short excerpt, never a credential.
type Error struct {
	Source string
	Op     string
	Status int
	Body   string
	Err    error
}

func (e *Error) Error() string {
	switch {
	case e.Status != 0:
		return fmt.Sprintf("%s %s: HTTP %d: %s", e.Source, e.Op, e.Status, e.Body)
	case e.Err != nil:
		return fmt.Sprintf("%s %s: %v", e.Source, e.Op, e.Err)
	}
	return fmt.Sprintf("%s %s failed", e.Source, e.Op)
}

func (e *Error) Unwrap() error { return e.Err }

// ErrNoKey marks a source whose credential is missing.
var ErrNoKey = errors.New("no credential for the billing source")

// getJSON performs an authenticated GET and decodes the JSON body into out.
// header adds auth and version headers. Bodies are capped at 4 MiB.
func getJSON(ctx context.Context, hc *http.Client, src, op, url string, header map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return &Error{Source: src, Op: op, Err: err}
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return &Error{Source: src, Op: op, Err: err}
	}
	defer resp.Body.Close()
	body := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for len(body) < 4<<20 {
		n, rerr := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	if resp.StatusCode/100 != 2 {
		ex := string(body)
		if len(ex) > 200 {
			ex = ex[:200] + "…"
		}
		return &Error{Source: src, Op: op, Status: resp.StatusCode, Body: ex}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &Error{Source: src, Op: op, Err: fmt.Errorf("decode response: %w", err)}
	}
	return nil
}

// monthStart is 00:00 UTC on the first of t's month.
func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func httpClient(hc *http.Client) *http.Client {
	if hc != nil {
		return hc
	}
	return &http.Client{Timeout: 20 * time.Second, Transport: httpdebug.Transport(nil)}
}

// WithHeaders returns hc (the default billing client when nil) with headers
// added to every request it makes, a profile's extra_headers. The original
// client is not modified; an empty set returns it as is.
func WithHeaders(hc *http.Client, headers map[string]string) *http.Client {
	hc = httpClient(hc)
	if len(headers) == 0 {
		return hc
	}
	c := *hc
	c.Transport = headerTransport{base: hc.Transport, headers: headers}
	return &c
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
