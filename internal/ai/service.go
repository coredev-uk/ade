package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/billing"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/ledger"
	"github.com/coredev-uk/ade/internal/records"
)

// ErrDisabled is returned when ai.mode is off.
var ErrDisabled = errors.New("AI is off (ai.mode = \"off\" in the config)")

// ErrNeedsConfirm is returned when a call needs the user's yes and the job
// has no Confirm hook (a non-interactive run without --yes).
type ErrNeedsConfirm struct{ Preflight Preflight }

func (e *ErrNeedsConfirm) Error() string {
	return "needs confirmation (" + strings.Join(e.Preflight.Confirm, "; ") + "): pass --yes to go ahead"
}

// ErrCancelled is returned when the user declined the pre-flight prompt.
var ErrCancelled = errors.New("cancelled at the pre-flight prompt; nothing was sent")

// Service runs AI jobs: cache lookup, pre-flight, caps, the driver call,
// the artifact and the audit entry.
type Service struct {
	Cfg     *config.Config
	Audit   *audit.Log
	Records *records.Store
	Ledger  *ledger.Ledger
	Cache   *Cache
	Billing *billing.Cache // optional: provider headroom for the pre-flight
	DB      *sql.DB        // cache.db, for the driver price cache
	Bus     *events.Bus    // optional
	HTTP    *http.Client
	Now     func() time.Time
	Loc     *time.Location
	// SprintStart reports when the current sprint began, for budget.sprint_usd.
	SprintStart func() (time.Time, bool)
	// Open builds the driver; tests swap it. Default DriverFor.
	Open func(ctx context.Context, p config.AIProfile) (Driver, error)
	// Source builds the profile's billing source for headroom; optional.
	Source func(ctx context.Context, name string, p config.AIProfile) (billing.Source, error)
}

// NewService wires a Service from the app. It opens cache.db.
func NewService(a *app.App) (*Service, error) {
	st, err := a.Cache()
	if err != nil {
		return nil, fmt.Errorf("open cache: %w", err)
	}
	rec := records.New(a.RecordsDir)
	s := &Service{
		Cfg: a.Config, Audit: a.Audit, Records: rec,
		Ledger: ledger.New(st, a.Audit), Cache: NewCache(st, rec), Billing: billing.NewCache(st),
		DB: st.DB, Bus: a.Bus, Now: time.Now, Loc: Location(a.Config),
	}
	s.Open = func(ctx context.Context, p config.AIProfile) (Driver, error) { return DriverFor(ctx, p, s.HTTP) }
	s.Source = func(ctx context.Context, name string, p config.AIProfile) (billing.Source, error) {
		return billing.New(ctx, p, s.billingLocal(name))
	}
	return s, nil
}

// Location is the user's time zone (user.timezone), local time when unset or
// unknown.
func Location(c *config.Config) *time.Location {
	if c.User.Timezone != "" {
		if loc, err := time.LoadLocation(c.User.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

// billingLocal gives billing sources the ledger view of one profile.
func (s *Service) billingLocal(profile string) billing.Local {
	return billing.Local{
		Spend:     func(from, to time.Time) (float64, error) { return s.Ledger.Spend(profile, from, to) },
		WeekStart: func(t time.Time) time.Time { return ledger.WeekStart(t, s.Loc) },
		Now:       s.Now,
		HTTP:      s.HTTP,
	}
}

// Job is one AI action.
type Job struct {
	Feature       string // summarize, classify, draft-reply, review: the audit action is ai.<feature>
	Role          Role
	PromptVersion string // bump when the prompt text changes: it keys the cache
	Profile       string // empty: the active profile

	System string
	Prompt string
	// Forge is the config forge name and Ref the item the output belongs to:
	// the artifact lands in that item's record directory.
	Forge string
	Ref   forge.Ref
	// Name is the artifact slug, e.g. "summary-mr233".
	Name string
	// Notes is true when Prompt carries the user's notes (WithNotes): the
	// notes are part of the prompt and so of the cache key, and the
	// pre-flight says so.
	Notes     bool
	MaxTokens int
	// Schema asks for a JSON answer where the gateway can enforce one; the
	// caller still parses defensively (see Call.Schema).
	Schema *Schema
	// Params are extra audit params (no prompt bodies, never secrets).
	Params map[string]any

	// Refresh skips the cache lookup and buys a new answer.
	Refresh bool
	// Override lets the call through the local period caps; it is audited as
	// budget.override. The provider's own limits still apply.
	Override bool
	// Confirm is asked when the pre-flight says so. nil means "cannot ask":
	// a call that needs confirmation then fails with ErrNeedsConfirm.
	Confirm func(Preflight) (bool, error)
	// Assume skips the confirmation (--yes).
	Assume bool
}

// Result is a finished job.
type Result struct {
	Text      string
	CacheHit  bool
	Artifact  string // relative to the records dir
	AuditID   string
	Profile   string
	Model     string
	CostUSD   float64
	Preflight Preflight
	// SavedUSD is what a cache hit did not cost: the original call's price.
	SavedUSD float64
	// Truncated is true when the model hit its output cap, even on the
	// retry with the cap doubled. Text ends in TruncatedNote and the answer
	// is not cached.
	Truncated bool
}

// TruncatedNote ends the text of an answer the model did not finish.
const TruncatedNote = "\n… (cut short: the model reached its output limit)"

// resolve picks the profile, driver and model for a job.
func (s *Service) resolve(ctx context.Context, j Job) (name string, p config.AIProfile, d Driver, model string, err error) {
	if s.Cfg.AI.Mode == "off" {
		return "", p, nil, "", ErrDisabled
	}
	name, p, err = s.Cfg.Profile(j.Profile)
	if err != nil {
		return "", p, nil, "", err
	}
	model, err = p.Model(string(j.Role))
	if err != nil {
		return name, p, nil, "", fmt.Errorf("profile %s: %w", name, err)
	}
	d, err = s.Open(ctx, p)
	if err != nil {
		return name, p, nil, model, fmt.Errorf("profile %s: %w", name, err)
	}
	return name, p, d, model, nil
}

// Run executes j. A cache hit costs nothing and is audited at $0.
func (s *Service) Run(ctx context.Context, j Job) (Result, error) {
	name, p, d, model, err := s.resolve(ctx, j)
	if err != nil {
		return Result{}, err
	}
	hash := InputHash(j.PromptVersion, string(j.Role), model, j.System, j.Prompt)
	item := j.Ref.String()

	if !j.Refresh {
		if a, ok, err := s.Cache.Get(hash); err != nil {
			return Result{}, err
		} else if ok {
			return s.hit(j, name, p, model, item, a)
		}
	}

	pf, err := s.preflight(ctx, j, name, p, d, model)
	if err != nil {
		return Result{}, err
	}
	if pf.Refuse != "" && j.Override {
		s.record(audit.Entry{
			Action: "budget.override", Item: item, Target: item,
			Params: map[string]any{"feature": j.Feature, "profile": name, "reason": pf.Refuse},
		})
		pf.Refuse = ""
	}
	if pf.Refuse != "" {
		stop := &StopError{Source: "local", Message: pf.Refuse}
		s.recordStop(j, name, model, item, stop)
		return Result{Preflight: pf}, stop
	}
	if len(pf.Confirm) > 0 && !j.Assume {
		if j.Confirm == nil {
			return Result{Preflight: pf}, &ErrNeedsConfirm{Preflight: pf}
		}
		ok, err := j.Confirm(pf)
		if err != nil {
			return Result{Preflight: pf}, err
		}
		if !ok {
			s.record(audit.Entry{
				Action: "ai." + j.Feature, Item: item, Target: item, Outcome: "cancelled",
				AI: &audit.AIInfo{Profile: name, Driver: p.Driver, Model: model, CostSource: CostReported},
			})
			return Result{Preflight: pf}, ErrCancelled
		}
	}

	call := Call{Model: model, System: j.System, Prompt: j.Prompt, MaxTokens: j.MaxTokens, Schema: j.Schema, Thinking: thinkingFor(p.Driver, j.Role)}
	if s.Cfg.Budget.CallUSD > 0 {
		call.MaxCostUSD = s.Cfg.Budget.CallUSD
	}
	resp, err := d.Complete(ctx, call)
	var res Result
	if err != nil {
		res, err = s.failed(j, name, p, model, item, resp, err, pf)
	} else {
		res, err = s.finish(j, name, p, model, item, hash, resp, pf)
	}
	// The call happened (and may have cost something) whether or not it
	// succeeded: spend can cross a threshold either way.
	s.warnBudget(item)
	return res, err
}

// Preview reports what Run would do without sending anything or writing an
// audit entry: the pre-flight estimate, and whether the answer is cached.
func (s *Service) Preview(ctx context.Context, j Job) (pf Preflight, cached bool, err error) {
	name, p, d, model, err := s.resolve(ctx, j)
	if err != nil {
		return Preflight{}, false, err
	}
	if !j.Refresh {
		hash := InputHash(j.PromptVersion, string(j.Role), model, j.System, j.Prompt)
		if _, ok, err := s.Cache.Get(hash); err != nil {
			return Preflight{}, false, err
		} else if ok {
			cached = true
		}
	}
	pf, err = s.preflight(ctx, j, name, p, d, model)
	return pf, cached, err
}

// thinkingFor chooses omp's reasoning level: none for explain/classify/draft work,
// light for review.
func thinkingFor(driver string, role Role) string {
	if driver != "omp" {
		return ""
	}
	if role == RoleReview || role == RolePlan {
		return "low"
	}
	return "off"
}

func (s *Service) hit(j Job, profile string, p config.AIProfile, model, item string, a records.Artifact) (Result, error) {
	id := ulid.Make().String()
	if err := s.Audit.Append(audit.Entry{
		ID: id, Action: "ai." + j.Feature, Target: item, Item: item, Params: j.Params,
		AI: &audit.AIInfo{
			Profile: profile, Driver: p.Driver, Model: model,
			CostSource: CostReported, CacheHit: true, Artifact: a.Path,
		},
	}); err != nil {
		return Result{}, fmt.Errorf("audit: %w", err)
	}
	return Result{Text: a.Body, CacheHit: true, Artifact: a.Path, AuditID: id, Profile: profile, Model: model, SavedUSD: a.CostUSD}, nil
}

func (s *Service) finish(j Job, name string, p config.AIProfile, model, item, hash string, resp Response, pf Preflight) (Result, error) {
	now := s.Now()
	id := ulid.Make().String()
	if resp.Truncated {
		// Kept in the record like any paid output, but with no input hash:
		// neither Cache.Put nor Reindex will serve it for the same prompt.
		hash = ""
		resp.Text = strings.TrimRight(resp.Text, "\n ") + TruncatedNote
	}
	art := records.Artifact{
		Feature: j.Feature, Profile: name, Model: firstNonEmpty(resp.Model, model), PromptVersion: j.PromptVersion,
		InputHash: hash, TokensIn: resp.Usage.In, TokensOut: resp.Usage.Out, CostUSD: resp.CostUSD,
		Created: now, AuditID: id, Body: resp.Text,
	}
	// The artifact is written first: paid output must survive an audit failure.
	path, werr := s.Records.WriteArtifact(s.Records.ItemDir(j.Forge, j.Ref), j.Name, art)
	params := maps.Clone(j.Params)
	if params == nil {
		params = map[string]any{}
	}
	if j.Schema != nil {
		params["structured"] = resp.Structured
	}
	if resp.Truncated {
		params["truncated"] = true
	}
	if resp.Gateway != nil {
		if resp.Gateway.KeySpend != nil {
			params["key_spend"] = *resp.Gateway.KeySpend
		}
		if resp.Gateway.KeyMaxBudget != nil {
			params["key_max_budget"] = *resp.Gateway.KeyMaxBudget
		}
	}
	e := audit.Entry{
		ID: id, Action: "ai." + j.Feature, Target: item, Item: item, Params: params,
		AI: &audit.AIInfo{
			Profile: name, Driver: p.Driver, Model: firstNonEmpty(resp.Model, model),
			TokensIn: resp.Usage.In, TokensOut: resp.Usage.Out, TokensCache: resp.Usage.Cache,
			CostUSD: resp.CostUSD, CostSource: resp.CostSource, Artifact: path,
			DestHost: resp.DestHost, Bytes: resp.Bytes,
		},
	}
	if werr != nil {
		e.Outcome, e.Error = "error", "write artifact: "+werr.Error()
	}
	aerr := s.Audit.Append(e)
	if werr != nil {
		return Result{Text: resp.Text, CostUSD: resp.CostUSD}, werr
	}
	if !resp.Truncated {
		if err := s.Cache.Put(hash, path, j.Feature, art.Model, now); err != nil {
			return Result{Text: resp.Text, CostUSD: resp.CostUSD, Artifact: path}, err
		}
	}
	if aerr != nil {
		return Result{Text: resp.Text, CostUSD: resp.CostUSD, Artifact: path, Truncated: resp.Truncated}, fmt.Errorf("audit: %w", aerr)
	}
	return Result{
		Text: resp.Text, Artifact: path, AuditID: id, Profile: name, Model: art.Model,
		CostUSD: resp.CostUSD, Preflight: pf, Truncated: resp.Truncated,
	}, nil
}

// failed audits a driver error. A budget stop becomes a budget.stop entry
// (and a notification); anything else an ai.* entry with outcome error, with
// whatever cost the driver did report.
func (s *Service) failed(j Job, name string, p config.AIProfile, model, item string, resp Response, err error, pf Preflight) (Result, error) {
	if stop, ok := AsStop(err); ok {
		s.recordStop(j, name, model, item, stop)
		return Result{Preflight: pf, CostUSD: resp.CostUSD}, err
	}
	s.record(audit.Entry{
		Action: "ai." + j.Feature, Target: item, Item: item, Params: j.Params, Outcome: "error", Error: err.Error(),
		AI: &audit.AIInfo{
			Profile: name, Driver: p.Driver, Model: model, TokensIn: resp.Usage.In, TokensOut: resp.Usage.Out,
			CostUSD: resp.CostUSD, CostSource: firstNonEmpty(resp.CostSource, CostReported),
		},
	})
	return Result{Preflight: pf, CostUSD: resp.CostUSD}, err
}

// recordStop writes the budget.stop audit entry and raises the event.
func (s *Service) recordStop(j Job, profile, model, item string, stop *StopError) {
	s.record(audit.Entry{
		Action: ledger.StopAction, Target: item, Item: item, Outcome: "error", Error: stop.Message,
		Params: map[string]any{"feature": j.Feature, "profile": profile, "model": model, "source": stop.Source},
	})
	if s.Bus != nil {
		e := events.New(ledger.StopAction, events.Error, "AI budget reached: "+stop.Message)
		e.Subject = item
		s.Bus.Publish(e)
	}
}

// record appends an audit entry; the call it describes already happened, so
// a failing audit write must not mask its result (it is reported on stderr by
// the CLI's own audit line).
func (s *Service) record(e audit.Entry) {
	_ = s.Audit.Append(e)
}

// preflight estimates the call and checks it against the caps.
func (s *Service) preflight(ctx context.Context, j Job, name string, p config.AIProfile, d Driver, model string) (Preflight, error) {
	out := j.MaxTokens
	if out <= 0 {
		out = defaultOut(j.Role)
	}
	pf := Preflight{
		Profile: name, Driver: p.Driver, Model: model, Role: string(j.Role),
		InTokens: estimateTokens(j.System) + estimateTokens(j.Prompt), OutTokens: out,
		Dest: destOf(p), Notes: j.Notes,
	}
	if price, ok := s.priceFor(ctx, name, p, d, model); ok {
		pf.Priced = true
		pf.EstimateUSD = priceUSD(price, pf.InTokens, pf.OutTokens)
	}
	spent, err := s.Ledger.Periods(s.Now(), s.Loc, s.SprintStart)
	if err != nil {
		return pf, err
	}
	pf.Spent = spent
	pf.Headroom = s.headroom(ctx, name, p)
	check(&pf, s.Cfg.Budget, p.Billing.WeeklySoftUSD, s.Cfg.AI.Mode == "ask")
	return pf, nil
}

func destOf(p config.AIProfile) string {
	switch p.Driver {
	case "omp":
		return "omp's provider"
	case "anthropic":
		if p.BaseURL == "" {
			return "api.anthropic.com"
		}
	}
	return hostOf(p.BaseURL)
}

// headroom is the provider's own remaining budget from the last snapshot,
// merged with local spend since it. It is read from cache.db only: the
// pre-flight never waits on a billing endpoint.
func (s *Service) headroom(_ context.Context, name string, p config.AIProfile) *float64 {
	if s.Billing == nil || p.Billing.Source != billing.SourceLiteLLM {
		return nil
	}
	snap, _, ok, err := s.Billing.Get(name)
	if err != nil || !ok {
		return nil
	}
	local, err := s.Ledger.Spend(name, snap.AsOf, time.Time{})
	if err != nil {
		return nil
	}
	return billing.Merge(snap, local).Remaining
}

// priceFor finds the model's list price: the profile's own table first, then
// what the driver publishes (cached for a day).
func (s *Service) priceFor(ctx context.Context, name string, p config.AIProfile, d Driver, model string) (config.Price, bool) {
	if pr, ok := lookupPrice(p.Prices, model); ok {
		return pr, true
	}
	prices := s.driverPrices(ctx, name, d)
	return lookupPrice(prices, model)
}

func lookupPrice(m map[string]config.Price, model string) (config.Price, bool) {
	if pr, ok := m[model]; ok {
		return pr, true
	}
	if _, bare, found := strings.Cut(model, "/"); found {
		if pr, ok := m[bare]; ok {
			return pr, true
		}
	}
	for k, pr := range m { // price keyed by "provider/model", model configured bare
		if strings.HasSuffix(k, "/"+model) {
			return pr, true
		}
	}
	return config.Price{}, false
}

const priceTTL = 24 * time.Hour

func (s *Service) driverPrices(ctx context.Context, name string, d Driver) map[string]config.Price {
	key := "ai.prices." + name
	if s.DB != nil {
		var raw string
		if s.DB.QueryRow(`SELECT v FROM kv WHERE k = ?`, key).Scan(&raw) == nil {
			var c struct {
				At     time.Time               `json:"at"`
				Prices map[string]config.Price `json:"prices"`
			}
			if json.Unmarshal([]byte(raw), &c) == nil && s.Now().Sub(c.At) < priceTTL {
				return c.Prices
			}
		}
	}
	prices, err := d.Prices(ctx)
	if err != nil || len(prices) == 0 {
		return nil
	}
	if s.DB != nil {
		if raw, err := json.Marshal(map[string]any{"at": s.Now().UTC(), "prices": prices}); err == nil {
			_, _ = s.DB.Exec(`INSERT INTO kv (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, key, string(raw))
		}
	}
	return prices
}
