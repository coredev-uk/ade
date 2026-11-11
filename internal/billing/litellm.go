package billing

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/config"
)

// LiteLLM reads a LiteLLM proxy's budget for the caller's own virtual key
// (docs/billing/litellm.md): GET /key/info, falling back to /team/info or
// /user/info when the key has no max_budget of its own.
type LiteLLM struct {
	base string
	key  string
	hc   *http.Client
}

// NewLiteLLM returns a source for the proxy at baseURL. The OpenAI-style
// "/v1" suffix of a profile's base_url is accepted and dropped.
func NewLiteLLM(baseURL, key string, hc *http.Client) *LiteLLM {
	return &LiteLLM{base: ProxyRoot(baseURL), key: key, hc: httpClient(hc)}
}

// ProxyRoot strips a trailing "/v1" and slashes from a profile base_url.
func ProxyRoot(baseURL string) string {
	b := strings.TrimRight(baseURL, "/")
	b = strings.TrimSuffix(b, "/v1")
	return strings.TrimRight(b, "/")
}

func (l *LiteLLM) Name() string            { return SourceLiteLLM }
func (l *LiteLLM) Interval() time.Duration { return time.Minute }

func (l *LiteLLM) auth() map[string]string {
	return map[string]string{"Authorization": "Bearer " + l.key}
}

// budgetRow is the common shape of a key, team or user row.
type budgetRow struct {
	Spend          *float64 `json:"spend"`
	MaxBudget      *float64 `json:"max_budget"`
	SoftBudget     *float64 `json:"soft_budget"`
	BudgetDuration *string  `json:"budget_duration"`
	BudgetResetAt  *string  `json:"budget_reset_at"`
	TeamID         *string  `json:"team_id"`
	UserID         *string  `json:"user_id"`
}

func (r budgetRow) snapshot(scope string, asOf time.Time) Snapshot {
	s := Snapshot{
		Source: SourceLiteLLM, AsOf: asOf, Scope: scope,
		Spend: r.Spend, Limit: r.MaxBudget, SoftLimit: r.SoftBudget,
	}
	if r.BudgetResetAt != nil {
		s.ResetAt = parseTime(*r.BudgetResetAt)
	}
	if r.BudgetDuration != nil {
		s.Window = *r.BudgetDuration
	}
	if s.Limit != nil && s.Spend != nil {
		s.Remaining = new(*s.Limit - *s.Spend)
	}
	if s.Spend != nil {
		s.Note = "gateway spend; the proxy writes spend in batches, so it can lag a few seconds"
	}
	return s
}

// Fetch reads the key's budget. When the key has no cap of its own and
// belongs to a team (or a user), the team's (user's) budget is what binds.
func (l *LiteLLM) Fetch(ctx context.Context) (Snapshot, error) {
	var ki struct {
		Info budgetRow `json:"info"`
	}
	if err := getJSON(ctx, l.hc, SourceLiteLLM, "/key/info", l.base+"/key/info", l.auth(), &ki); err != nil {
		return Snapshot{}, err
	}
	asOf := time.Now().UTC()
	snap := ki.Info.snapshot("key", asOf)
	if ki.Info.MaxBudget != nil {
		return snap, nil
	}
	// No cap on the key itself: look at the scope that binds, if readable. A
	// 403/404 there is not fatal; the key's own figures stay.
	switch {
	case ki.Info.TeamID != nil && *ki.Info.TeamID != "":
		var ti struct {
			TeamInfo budgetRow `json:"team_info"`
		}
		q := url.Values{"team_id": {*ki.Info.TeamID}}
		if err := getJSON(ctx, l.hc, SourceLiteLLM, "/team/info", l.base+"/team/info?"+q.Encode(), l.auth(), &ti); err == nil && ti.TeamInfo.MaxBudget != nil {
			return ti.TeamInfo.snapshot("team", asOf), nil
		}
	case ki.Info.UserID != nil && *ki.Info.UserID != "":
		var ui struct {
			UserInfo budgetRow `json:"user_info"`
		}
		if err := getJSON(ctx, l.hc, SourceLiteLLM, "/user/info", l.base+"/user/info", l.auth(), &ui); err == nil && ui.UserInfo.MaxBudget != nil {
			return ui.UserInfo.snapshot("user", asOf), nil
		}
	}
	return snap, nil
}

// ModelPrices reads per-token prices from GET /model_group/info, as USD per
// million tokens keyed by model group. Groups without a price are omitted
// (LiteLLM logs those at $0, so a price of 0 would mislead the pre-flight).
func (l *LiteLLM) ModelPrices(ctx context.Context) (map[string]config.Price, error) {
	var mg struct {
		Data []struct {
			Group  string   `json:"model_group"`
			Input  *float64 `json:"input_cost_per_token"`
			Output *float64 `json:"output_cost_per_token"`
		} `json:"data"`
	}
	if err := getJSON(ctx, l.hc, SourceLiteLLM, "/model_group/info", l.base+"/model_group/info", l.auth(), &mg); err != nil {
		return nil, err
	}
	out := map[string]config.Price{}
	for _, m := range mg.Data {
		if m.Input == nil || m.Output == nil {
			continue
		}
		out[m.Group] = config.Price{Input: *m.Input * 1e6, Output: *m.Output * 1e6}
	}
	return out, nil
}

// parseTime reads the ISO timestamps LiteLLM emits, with or without a zone
// (the proxy's own clock is UTC). Unparseable input is "no reset time".
func parseTime(s string) *time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return &t
		}
	}
	return nil
}
