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
	"github.com/coredev-uk/ade/internal/store"
)

// ErrDisabled is returned when ai.mode is off.
var ErrDisabled = errors.New("AI is off (ai.mode = \"off\" in the config)")

// ErrNeedsConfirm is returned when a call needs the user's yes and the job
// has no Confirm hook (a non-interactive run without --yes).
type ErrNeedsConfirm struct{ Preflight Preflight }

func (e *ErrNeedsConfirm) Error() string {
	return "needs confirmation (" + strings.Join(e.Preflight.Confirm, "; ") + "): pass --yes to go ahead"
}

// ErrCutShortRecently is returned instead of asking again for a prompt whose
// answer the model cut short a moment ago: the same prompt would most likely
// be cut short again, and each try is paid for. Refresh asks anyway.
var ErrCutShortRecently = errors.New("the last answer to this prompt was cut short; refresh to ask again")

// cutShortTTL is how long a cut-short answer is remembered per input hash.
const cutShortTTL = 30 * time.Minute

// ErrCancelled is returned when the user declined the pre-flight prompt.
var ErrCancelled = errors.New("cancelled at the pre-flight prompt; nothing was sent")

// unusableAnswer is a finished call whose text the job's Validate (or the
// basic "there is some text" check) refused. It matches ErrBadAnswer.
type unusableAnswer struct{ err error }

func (e *unusableAnswer) Error() string        { return e.err.Error() }
func (e *unusableAnswer) Unwrap() error        { return e.err }
func (e *unusableAnswer) Is(target error) bool { return target == ErrBadAnswer }

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
	// Validate reads the answer the way the caller will, and says why it is
	// unusable. An answer it refuses is still paid for, audited and kept in
	// the record, but is never cached (and one found in the cache is dropped
	// and bought again): asking again must be able to recover, not serve the
	// same unusable text for ever. Run then returns the error, which matches
	// ErrBadAnswer. nil accepts any non-empty text.
	Validate func(text string) error

	// Refresh skips the cache lookup and buys a new answer.
	Refresh bool
	// Override lets the call through the local period caps; it is audited as
	// budget.override. The provider's own limits still apply.
	Override bool
	// Auto marks a call nobody asked for (the MR page's summary that starts
	// by itself). On a day its profile hit a budget stop it is not made:
	// Run answers from the cache or returns ErrAutoPaused (daystop.go).
	Auto bool
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
	// retry with the cap doubled. Text is the fragment as the model wrote it:
	// it never carries TruncatedNote, so nothing parsed or posted from it
	// does. The caller shows the note. The answer is not cached.
	Truncated bool
}

// CutShort is the words to show beside the answer when the model did not
// finish it, "" otherwise.
func (r Result) CutShort() string {
	if r.Truncated {
		return strings.TrimSpace(TruncatedNote)
	}
	return ""
}

// TruncatedNote is shown beside an answer the model did not finish. It is
// written to the artifact only, never to Result.Text.
const TruncatedNote = "\n… (cut short: the model reached its output limit)"

// profileFor picks the profile and model for a job. It opens nothing: a
// cached answer needs neither the driver nor its key.
func (s *Service) profileFor(j Job) (name string, p config.AIProfile, model string, err error) {
	if s.Cfg.AI.Mode == "off" {
		return "", p, "", ErrDisabled
	}
	name, p, err = s.Cfg.Profile(j.Profile)
	if err != nil {
		return "", p, "", err
	}
	model, err = p.Model(string(j.Role))
	if err != nil {
		return name, p, "", fmt.Errorf("profile %s: %w", name, err)
	}
	return name, p, model, nil
}

// driverFor opens the profile's driver (running its key command or reading its
// key variable): only a call that goes out needs it.
func (s *Service) driverFor(ctx context.Context, name string, p config.AIProfile) (Driver, error) {
	d, err := s.Open(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", name, err)
	}
	return d, nil
}

// cached looks hash up in the result cache. An answer the job's Validate
// refuses (one cached by an older version, or before the answer could be
// read) is dropped from the index and counts as a miss.
func (s *Service) cached(j Job, hash string) (records.Artifact, bool, error) {
	a, ok, err := s.Cache.Get(hash)
	if err != nil || !ok {
		return a, false, err
	}
	if s.unusable(j, a.Body) != nil {
		return records.Artifact{}, false, s.Cache.Evict(hash)
	}
	return a, true, nil
}

// unusable reports why an answer cannot be used: no text at all, or the job's
// own reader refuses it.
func (s *Service) unusable(j Job, text string) error {
	if strings.TrimSpace(text) == "" {
		return &unusableAnswer{err: errors.New("the model's answer was empty")}
	}
	if j.Validate != nil {
		if err := j.Validate(text); err != nil {
			return &unusableAnswer{err: err}
		}
	}
	return nil
}

// Run executes j. A cache hit costs nothing and is audited at $0; it needs
// neither the driver nor the API key.
func (s *Service) Run(ctx context.Context, j Job) (Result, error) {
	name, p, model, err := s.profileFor(j)
	if err != nil {
		return Result{}, err
	}
	hash := InputHash(j.PromptVersion, string(j.Role), model, j.System, j.Prompt)
	item := j.Ref.String()

	if !j.Refresh {
		if a, ok, err := s.cached(j, hash); err != nil {
			return Result{}, err
		} else if ok {
			return s.hit(j, name, p, model, item, a)
		}
		if s.cutShortRecently(hash) {
			return Result{}, ErrCutShortRecently
		}
	}
	if j.Auto && s.stoppedToday(name) {
		return Result{}, ErrAutoPaused
	}
	d, err := s.driverFor(ctx, name, p)
	if err != nil {
		return Result{}, err
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

	// The price the pre-flight used goes with the call: the driver books the
	// cost from it, so what was estimated is what the ledger and the caps see.
	call := Call{Model: model, System: j.System, Prompt: j.Prompt, MaxTokens: j.MaxTokens, Schema: j.Schema, Thinking: thinkingFor(p.Driver, j.Role), Price: pf.price}
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
// audit entry: the pre-flight estimate, and whether the answer is cached. A
// cached answer needs no driver: when the driver cannot be opened the
// estimate is left empty.
func (s *Service) Preview(ctx context.Context, j Job) (pf Preflight, cached bool, err error) {
	name, p, model, err := s.profileFor(j)
	if err != nil {
		return Preflight{}, false, err
	}
	if !j.Refresh {
		hash := InputHash(j.PromptVersion, string(j.Role), model, j.System, j.Prompt)
		if _, ok, err := s.cached(j, hash); err != nil {
			return Preflight{}, false, err
		} else if ok {
			cached = true
		}
	}
	d, err := s.driverFor(ctx, name, p)
	if err != nil {
		if cached {
			return Preflight{Profile: name, Driver: p.Driver, Model: model, Role: string(j.Role), Dest: destOf(p), Notes: j.Notes}, true, nil
		}
		return Preflight{}, false, err
	}
	pf, err = s.preflight(ctx, j, name, p, d, model)
	return pf, cached, err
}

func cutShortKey(hash string) string { return "ai:cut-short:" + hash }

func (s *Service) cutShortRecently(hash string) bool {
	if s.DB == nil {
		return false
	}
	cut, ok := store.Peek[bool](&store.Store{DB: s.DB}, cutShortKey(hash), cutShortTTL, s.Now())
	return ok && cut
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
	// A model that echoes a token from the input (a CI log, a description)
	// must not store or show it: the answer is masked like the audit log is.
	redacted := false
	if r := audit.Redact(resp.Text); r != resp.Text {
		resp.Text, redacted = r, true
	}
	var bad error
	if resp.Truncated {
		// Kept in the record like any paid output, but with no input hash:
		// neither Cache.Put nor Reindex will serve it for the same prompt.
		if s.DB != nil {
			store.Put(&store.Store{DB: s.DB}, cutShortKey(hash), now, true)
		}
		hash = ""
		resp.Text = strings.TrimRight(resp.Text, "\n ")
	} else if bad = s.unusable(j, resp.Text); bad != nil {
		hash = "" // paid for and kept, but never served again for this prompt
	}
	art := records.Artifact{
		Feature: j.Feature, Profile: name, Model: firstNonEmpty(resp.Model, model), PromptVersion: j.PromptVersion,
		InputHash: hash, TokensIn: resp.Usage.In, TokensOut: resp.Usage.Out, CostUSD: resp.CostUSD,
		Created: now, AuditID: id, Body: resp.Text,
	}
	if resp.Truncated {
		art.Body += TruncatedNote
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
	if redacted {
		params["redacted"] = true
	}
	if bad != nil {
		params["unusable"] = oneLine(bad.Error())
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
	switch {
	case werr != nil:
		e.Outcome, e.Error = "error", "write artifact: "+werr.Error()
	case bad != nil:
		e.Outcome, e.Error = "error", "unusable answer: "+oneLine(bad.Error())
	}
	aerr := s.Audit.Append(e)
	// The provider's own figure for the key, now that the call is in the
	// ledger: the snapshot restarts from it (after the entry, so the call is
	// not counted both in the figure and in the ledger since it).
	s.noteGateway(name, p, resp.Gateway)
	if werr != nil {
		return Result{Text: resp.Text, CostUSD: resp.CostUSD}, werr
	}
	if bad == nil && !resp.Truncated {
		if err := s.Cache.Put(hash, path, j.Feature, art.Model, now); err != nil {
			return Result{Text: resp.Text, CostUSD: resp.CostUSD, Artifact: path}, err
		}
	}
	if aerr != nil {
		return Result{Text: resp.Text, CostUSD: resp.CostUSD, Artifact: path, Truncated: resp.Truncated}, fmt.Errorf("audit: %w", aerr)
	}
	res := Result{
		Text: resp.Text, Artifact: path, AuditID: id, Profile: name, Model: art.Model,
		CostUSD: resp.CostUSD, Preflight: pf, Truncated: resp.Truncated,
	}
	if bad != nil {
		return res, bad
	}
	return res, nil
}

// noteGateway keeps the provider-side snapshot of a LiteLLM key current from
// the key's own spend, which every response carries (x-litellm-key-spend). A
// snapshot that describes another scope (a team's or user's budget binds when
// the key has none) is left alone: the key's spend says nothing about it.
func (s *Service) noteGateway(name string, p config.AIProfile, g *GatewayInfo) {
	if s.Billing == nil || g == nil || g.KeySpend == nil || p.Billing.Source != billing.SourceLiteLLM {
		return
	}
	snap, _, ok, err := s.Billing.Get(name)
	if err != nil {
		return
	}
	switch {
	case ok && snap.Scope != "key":
		return
	case !ok && g.KeyMaxBudget == nil:
		return // no cap known: nothing to refuse on
	}
	snap.Source, snap.Scope, snap.AsOf = billing.SourceLiteLLM, "key", s.Now().UTC()
	snap.Spend = g.KeySpend
	if g.KeyMaxBudget != nil {
		snap.Limit = g.KeyMaxBudget
	}
	snap.Remaining = nil
	if snap.Limit != nil {
		snap.Remaining = new(*snap.Limit - *snap.Spend)
	}
	_ = s.Billing.Put(name, snap)
}

// failed audits a driver error. A budget stop becomes a budget.stop entry
// (and, once a day, a notification); anything else an ai.* entry with outcome
// error, with whatever cost the driver did report and where the request went
// and how many bytes of it were sent: a failed call may have left the machine
// all the same.
func (s *Service) failed(j Job, name string, p config.AIProfile, model, item string, resp Response, err error, pf Preflight) (Result, error) {
	info := &audit.AIInfo{
		Profile: name, Driver: p.Driver, Model: model, TokensIn: resp.Usage.In, TokensOut: resp.Usage.Out,
		CostUSD: resp.CostUSD, CostSource: firstNonEmpty(resp.CostSource, CostReported),
		DestHost: resp.DestHost, Bytes: resp.Bytes,
	}
	if stop, ok := AsStop(err); ok {
		// A stop on the retry follows a first call that was paid for: the
		// ledger counts cost from ai.* entries only, so book it.
		if resp.CostUSD > 0 {
			s.record(audit.Entry{
				Action: "ai." + j.Feature, Target: item, Item: item, Params: j.Params, Outcome: "error", Error: err.Error(),
				AI: info,
			})
		}
		s.recordStop(j, name, model, item, stop)
		return Result{Preflight: pf, CostUSD: resp.CostUSD}, err
	}
	s.record(audit.Entry{
		Action: "ai." + j.Feature, Target: item, Item: item, Params: j.Params, Outcome: "error", Error: err.Error(),
		AI: info,
	})
	return Result{Preflight: pf, CostUSD: resp.CostUSD}, err
}

// recordStop writes the budget.stop audit entry; the day's first for the
// profile also raises the event (daystop.go).
func (s *Service) recordStop(j Job, profile, model, item string, stop *StopError) {
	RecordStop(s.Audit, s.Bus, s.Now(), s.Loc, Stopped{Item: item, Feature: j.Feature, Profile: profile, Model: model, Stop: stop})
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
		Retry: p.Driver != "omp" && j.MaxTokens > 0, // see retryTruncated
		// omp takes no output cap, and thinks at --thinking low for review and
		// plan: the estimate assumes MaxTokens but nothing enforces it.
		EstimateOnly: p.Driver == "omp",
	}
	if price, ok := s.priceFor(ctx, name, p, d, model); ok {
		pf.Priced, pf.price = true, &price
		in, outTok := pf.InTokens, pf.OutTokens
		if pf.Retry {
			in, outTok = 2*in, 3*outTok
		}
		pf.EstimateUSD = priceUSD(price, in, outTok)
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

// headroomMaxAge is how old a provider snapshot may be and still refuse a
// call. The pre-flight never waits on a billing endpoint, so a snapshot that
// nothing has refreshed (the budget page polls every minute while it is open;
// every LiteLLM response refreshes it) must not keep deciding for long: the
// provider's budget may have been raised or reset since.
const headroomMaxAge = time.Hour

// headroom is the provider's own remaining budget from the last snapshot,
// merged with local spend since it. It is read from cache.db only: the
// pre-flight never waits on a billing endpoint. A snapshot whose budget
// window has reset since, or that is older than headroomMaxAge, says nothing
// about now and is ignored (the provider still refuses a call it must).
func (s *Service) headroom(_ context.Context, name string, p config.AIProfile) *float64 {
	if s.Billing == nil || p.Billing.Source != billing.SourceLiteLLM {
		return nil
	}
	snap, _, ok, err := s.Billing.Get(name)
	if err != nil || !ok {
		return nil
	}
	now := s.Now()
	if snap.ResetAt != nil && !now.Before(*snap.ResetAt) {
		return nil
	}
	if now.Sub(snap.AsOf) > headroomMaxAge {
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
