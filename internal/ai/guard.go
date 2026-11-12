package ai

import (
	"fmt"
	"math"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/ledger"
)

// Spent is what the ledger says has been spent in each budget period.
type Spent = ledger.Periods

// Preflight is the estimate shown before a call, and the verdict on it.
type Preflight struct {
	Profile   string `json:"profile"`
	Driver    string `json:"driver"`
	Model     string `json:"model"`
	Role      string `json:"role"`
	InTokens  int    `json:"in_tokens"`
	OutTokens int    `json:"out_tokens"`
	// EstimateUSD is the worst case: with Retry, the call sent twice, the
	// second time with double the output cap.
	EstimateUSD float64 `json:"estimate_usd"`
	// Retry is true when an answer cut off at the output cap is asked for
	// again once, with the cap doubled (HTTP drivers; omp does not).
	Retry bool `json:"may_retry,omitempty"`
	// Priced is false when no price for the model is known: the estimate is
	// then $0 and the call's cost will be unknown as well.
	Priced bool   `json:"priced"`
	Dest   string `json:"dest,omitempty"`
	// Notes is true when the prompt carries the user's own notes on the item.
	Notes bool `json:"includes_notes,omitempty"`

	Spent Spent `json:"spent"`
	// Headroom is what the provider says is left under its own cap
	// (LiteLLM max_budget − spend); nil when unknown.
	Headroom *float64 `json:"headroom_usd,omitempty"`

	// Refuse is why the call must not go out; empty when it may.
	Refuse string `json:"refuse,omitempty"`
	// Confirm lists why the user has to say yes first.
	Confirm []string `json:"confirm,omitempty"`
}

// estimateTokens approximates the tokens of s at ~3.5 bytes per token, which
// errs high for English and code, so the estimate is a ceiling.
func estimateTokens(s string) int { return int(math.Ceil(float64(len(s)) / 3.5)) }

// defaultOut is the output length assumed when a call sets no MaxTokens.
func defaultOut(role Role) int {
	switch role {
	case RoleReview, RolePlan, RoleImplement:
		return 2000
	case RoleDraft:
		return 600
	}
	return 500
}

// priceUSD prices in/out tokens (list prices, USD per million).
func priceUSD(p config.Price, in, out int) float64 {
	return (float64(in)*p.Input + float64(out)*p.Output) / 1e6
}

// check decides on est against the caps. softWeekly is the profile's
// weekly_soft_usd: crossing it asks for confirmation, never refuses. In mode
// "ask" an estimate over budget.confirm_over_usd asks too; in "auto" only caps act.
func check(pf *Preflight, b config.Budget, softWeekly float64, ask bool) {
	est := pf.EstimateUSD
	s := pf.Spent
	money := usd
	switch {
	case b.CallUSD > 0 && est > b.CallUSD:
		pf.Refuse = fmt.Sprintf("this call is estimated at %s, over budget.call_usd %s", money(est), money(b.CallUSD))
	case b.DailyUSD > 0 && s.Day+est > b.DailyUSD:
		pf.Refuse = capMessage("daily", "today", s.Day, est, b.DailyUSD, "daily_usd")
	case b.WeeklyUSD > 0 && s.Week+est > b.WeeklyUSD:
		pf.Refuse = capMessage("weekly", "this week", s.Week, est, b.WeeklyUSD, "weekly_usd")
	case b.SprintUSD > 0 && s.HaveSprint && s.Sprint+est > b.SprintUSD:
		pf.Refuse = capMessage("sprint", "this sprint", s.Sprint, est, b.SprintUSD, "sprint_usd")
	case pf.Headroom != nil && est > *pf.Headroom:
		pf.Refuse = fmt.Sprintf("the provider's own budget has %s left and this call is estimated at %s", money(*pf.Headroom), money(est))
	}
	if ask && b.ConfirmOverUSD > 0 && est > b.ConfirmOverUSD {
		pf.Confirm = append(pf.Confirm, fmt.Sprintf("estimated %s is over the %s you set to be asked first", money(est), money(b.ConfirmOverUSD)))
	}
	if ask && softWeekly > 0 && s.Week+est > softWeekly {
		pf.Confirm = append(pf.Confirm, fmt.Sprintf("this week's usage (%s + ~%s) passes the soft cap %s", money(s.Week), money(est), money(softWeekly)))
	}
}

// usd formats a dollar amount, with more digits for the sub-cent figures AI
// calls usually cost.
func usd(v float64) string {
	if v != 0 && v < 0.01 && v > -0.01 {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

func capMessage(name, period string, spent, est, cap float64, key string) string {
	return fmt.Sprintf("%s cap reached: %s spent %s + ~%s for this call is over budget.%s %s",
		name, usd(spent), period, usd(est), key, usd(cap))
}

// Describe renders a preflight for a confirm prompt.
func (pf Preflight) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s · %s · %s", pf.Profile, pf.Model, pf.Role)
	if pf.Priced {
		fmt.Fprintf(&b, " · ~%d in / ≤%d out tokens · est $%.4f", pf.InTokens, pf.OutTokens, pf.EstimateUSD)
	} else {
		fmt.Fprintf(&b, " · ~%d in / ≤%d out tokens · no price known", pf.InTokens, pf.OutTokens)
	}
	if pf.Retry {
		fmt.Fprintf(&b, " · asks again with double the cap if cut short (≤%d out in all)", 3*pf.OutTokens)
	}
	if pf.Dest != "" {
		fmt.Fprintf(&b, " · to %s", pf.Dest)
	}
	if pf.Notes {
		b.WriteString(" · includes your notes")
	}
	return b.String()
}
