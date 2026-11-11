// Package ledger is ADE's spend ledger: a query over the `ai` fields of audit
// events, indexed in cache.db. There is no second ledger to drift: the audit
// log is the only source, and the index can be dropped and rebuilt from it at
// any time (`ade reindex`).
package ledger

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/store"
)

func init() {
	store.Register("ledger.entries", `CREATE TABLE ledger_entries (
		id          TEXT PRIMARY KEY,
		ts          TEXT NOT NULL,  -- RFC 3339 UTC
		action      TEXT NOT NULL,
		feature     TEXT NOT NULL,
		profile     TEXT NOT NULL,
		driver      TEXT NOT NULL,
		model       TEXT NOT NULL,
		tokens_in   INTEGER NOT NULL,
		tokens_out  INTEGER NOT NULL,
		tokens_cache INTEGER NOT NULL,
		cost_usd    REAL NOT NULL,
		cost_source TEXT NOT NULL,
		cache_hit   INTEGER NOT NULL,
		item        TEXT NOT NULL,
		target      TEXT NOT NULL,
		artifact    TEXT NOT NULL,
		outcome     TEXT NOT NULL,
		error       TEXT NOT NULL
	);
	CREATE INDEX ledger_ts ON ledger_entries (ts);
	CREATE INDEX ledger_profile_ts ON ledger_entries (profile, ts)`)
}

// StopAction is the audit action of a budget stop, whether a provider refused
// the call or ADE's own caps did.
const StopAction = "budget.stop"

// Entry is one ledger row: an `ai.*` audit event (cache hits included, at $0)
// or a `budget.stop`.
type Entry struct {
	ID          string    `json:"id"`
	TS          time.Time `json:"ts"`
	Action      string    `json:"action"`  // ai.summarize, budget.stop, ...
	Feature     string    `json:"feature"` // action without the "ai." prefix; "stop" for budget.stop
	Profile     string    `json:"profile"`
	Driver      string    `json:"driver,omitempty"`
	Model       string    `json:"model"`
	TokensIn    int       `json:"tokens_in"`
	TokensOut   int       `json:"tokens_out"`
	TokensCache int       `json:"tokens_cache,omitempty"`
	CostUSD     float64   `json:"cost_usd"`
	CostSource  string    `json:"cost_source,omitempty"` // reported | computed | gateway
	CacheHit    bool      `json:"cache_hit"`
	Item        string    `json:"item,omitempty"`
	Target      string    `json:"target,omitempty"`
	Artifact    string    `json:"artifact,omitempty"`
	Outcome     string    `json:"outcome"`
	Error       string    `json:"error,omitempty"`
}

// Stop reports whether the entry is a budget stop rather than a call.
func (e Entry) Stop() bool { return e.Action == StopAction }

// FromAudit converts an audit entry; ok is false for entries the ledger does
// not track (anything but `ai.*` with an ai block, and `budget.stop`).
func FromAudit(e audit.Entry) (Entry, bool) {
	out := Entry{
		ID: e.ID, TS: e.TS.UTC(), Action: e.Action,
		Item: e.Item, Target: e.Target, Outcome: e.Outcome, Error: e.Error,
	}
	switch {
	case e.Action == StopAction:
		out.Feature = "stop"
		if f, ok := e.Params["feature"].(string); ok && f != "" {
			out.Feature = f
		}
		if p, ok := e.Params["profile"].(string); ok {
			out.Profile = p
		}
		if m, ok := e.Params["model"].(string); ok {
			out.Model = m
		}
	case strings.HasPrefix(e.Action, "ai.") && e.AI != nil:
		out.Feature = strings.TrimPrefix(e.Action, "ai.")
	default:
		return Entry{}, false
	}
	if a := e.AI; a != nil {
		out.Profile, out.Driver, out.Model = a.Profile, a.Driver, a.Model
		out.TokensIn, out.TokensOut, out.TokensCache = a.TokensIn, a.TokensOut, a.TokensCache
		out.CostUSD, out.CostSource, out.CacheHit, out.Artifact = a.CostUSD, a.CostSource, a.CacheHit, a.Artifact
	}
	return out, true
}

// Ledger indexes one audit log in cache.db.
type Ledger struct {
	db  *sql.DB
	log *audit.Log
	now func() time.Time
}

// New returns a ledger over log, indexed in st.
func New(st *store.Store, log *audit.Log) *Ledger {
	return &Ledger{db: st.DB, log: log, now: time.Now}
}

// tsFormat is fixed-width so timestamps compare correctly as text.
const tsFormat = "2006-01-02T15:04:05.000000000Z07:00"

const (
	markerKey = "ledger.synced_until"
	// rescan overlaps the previous high-water mark, because concurrent ade
	// processes may append entries slightly out of timestamp order.
	rescan = 2 * time.Minute
)

// Sync indexes audit entries newer than the last sync and returns how many
// were new. It is cheap enough to call before every read.
func (l *Ledger) Sync() (int, error) {
	var since time.Time
	var mark string
	err := l.db.QueryRow(`SELECT v FROM kv WHERE k = ?`, markerKey).Scan(&mark)
	switch {
	case err == nil:
		if t, perr := time.Parse(tsFormat, mark); perr == nil {
			since = t.Add(-rescan)
		}
	case err != sql.ErrNoRows:
		return 0, err
	}
	return l.index(since)
}

// Rebuild drops the index and re-reads the whole audit log. It returns the
// number of entries indexed.
func (l *Ledger) Rebuild() (int, error) {
	if _, err := l.db.Exec(`DELETE FROM ledger_entries`); err != nil {
		return 0, err
	}
	if _, err := l.db.Exec(`DELETE FROM kv WHERE k = ?`, markerKey); err != nil {
		return 0, err
	}
	return l.index(time.Time{})
}

func (l *Ledger) index(since time.Time) (int, error) {
	entries, err := l.log.Query(audit.Filter{Since: since})
	if err != nil {
		return 0, fmt.Errorf("read audit log: %w", err)
	}
	tx, err := l.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO ledger_entries
		(id, ts, action, feature, profile, driver, model, tokens_in, tokens_out, tokens_cache,
		 cost_usd, cost_source, cache_hit, item, target, artifact, outcome, error)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	added := 0
	latest := since
	for _, ae := range entries {
		if ae.TS.After(latest) {
			latest = ae.TS
		}
		e, ok := FromAudit(ae)
		if !ok {
			continue
		}
		res, err := stmt.Exec(e.ID, e.TS.Format(tsFormat), e.Action, e.Feature, e.Profile, e.Driver, e.Model,
			e.TokensIn, e.TokensOut, e.TokensCache, e.CostUSD, e.CostSource, b2i(e.CacheHit),
			e.Item, e.Target, e.Artifact, e.Outcome, e.Error)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	if !latest.IsZero() {
		if _, err := tx.Exec(`INSERT INTO kv (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
			markerKey, latest.UTC().Format(tsFormat)); err != nil {
			return 0, err
		}
	}
	return added, tx.Commit()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
