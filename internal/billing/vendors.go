package billing

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func trimBase(base, def string) string {
	if base == "" {
		base = def
	}
	return strings.TrimRight(base, "/")
}

func nextMonth(t time.Time) time.Time { return monthStart(t).AddDate(0, 1, 0) }

// maxPages bounds pagination so a misbehaving server cannot loop forever.
const maxPages = 100

// --- Anthropic ---------------------------------------------------------

type anthropicSource struct {
	base, key string
	hc        *http.Client
	now       func() time.Time
}

// NewAnthropic reads month-to-date org cost from the Admin analytics API.
func NewAnthropic(baseURL, adminKey string, hc *http.Client, now func() time.Time) Source {
	if now == nil {
		now = time.Now
	}
	return &anthropicSource{trimBase(baseURL, "https://api.anthropic.com"), adminKey, httpClient(hc), now}
}

func (a *anthropicSource) Name() string            { return SourceAnthropic }
func (a *anthropicSource) Interval() time.Duration { return 6 * time.Hour }

type anthropicCostPage struct {
	Data []struct {
		Cost struct {
			Amount string `json:"amount"`
		} `json:"cost"`
	} `json:"data"`
	HasMore         bool       `json:"has_more"`
	NextPage        string     `json:"next_page"`
	DataRefreshedAt *time.Time `json:"data_refreshed_at"`
}

func (a *anthropicSource) Fetch(ctx context.Context) (Snapshot, error) {
	now := a.now().UTC()
	start := monthStart(now)
	end := now.Truncate(time.Hour)
	snap := Snapshot{
		Source: SourceAnthropic, Scope: "org", Window: "month",
		ResetAt: new(nextMonth(now)), AsOf: end,
		Note: "org cost, 4–24 h lag; needs an Admin key",
	}
	if !end.After(start) {
		snap.Spend = new(0.0)
		return snap, nil
	}
	hdr := map[string]string{
		"x-api-key":         a.key,
		"anthropic-version": "2026-10-21",
		"anthropic-beta":    "analytics-usage-cost-2026-08-24",
		"User-Agent":        "ade",
	}
	var cents big.Rat
	refreshed := (*time.Time)(nil)
	page := ""
	for range maxPages {
		q := url.Values{}
		q.Set("starting_at", start.Format(time.RFC3339))
		q.Set("ending_at", end.Format(time.RFC3339))
		q.Set("limit", "1000")
		if page != "" {
			q.Set("page", page)
		}
		var out anthropicCostPage
		if err := getJSON(ctx, a.hc, SourceAnthropic, "cost", a.base+"/v1/organizations/analytics/cost?"+q.Encode(), hdr, &out); err != nil {
			return Snapshot{}, err
		}
		for _, row := range out.Data {
			var r big.Rat
			if _, ok := r.SetString(strings.TrimSpace(row.Cost.Amount)); !ok {
				return Snapshot{}, &Error{Source: SourceAnthropic, Op: "cost", Err: fmt.Errorf("bad cost amount %q", row.Cost.Amount)}
			}
			cents.Add(&cents, &r)
		}
		if out.DataRefreshedAt != nil {
			refreshed = out.DataRefreshedAt
		}
		if !out.HasMore || out.NextPage == "" {
			break
		}
		page = out.NextPage
	}
	dollars, _ := new(big.Rat).Quo(&cents, big.NewRat(100, 1)).Float64()
	snap.Spend = &dollars
	if refreshed != nil {
		snap.AsOf = refreshed.UTC()
	}
	return snap, nil
}

// --- OpenAI ------------------------------------------------------------

type openAISource struct {
	base, key string
	hc        *http.Client
	now       func() time.Time
}

// NewOpenAI reads month-to-date org cost (complete UTC days) from the Admin
// costs API.
func NewOpenAI(baseURL, adminKey string, hc *http.Client, now func() time.Time) Source {
	if now == nil {
		now = time.Now
	}
	return &openAISource{trimBase(baseURL, "https://api.openai.com"), adminKey, httpClient(hc), now}
}

func (o *openAISource) Name() string            { return SourceOpenAI }
func (o *openAISource) Interval() time.Duration { return 6 * time.Hour }

type openAICostPage struct {
	Data []struct {
		Results []struct {
			Amount struct {
				Value float64 `json:"value"`
			} `json:"amount"`
		} `json:"results"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page"`
}

func (o *openAISource) Fetch(ctx context.Context) (Snapshot, error) {
	now := o.now().UTC()
	start := monthStart(now)
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	snap := Snapshot{
		Source: SourceOpenAI, Scope: "org", Window: "month",
		ResetAt: new(nextMonth(now)), AsOf: end,
		Note: "org cost, complete UTC days; needs an Admin key",
	}
	if !end.After(start) {
		snap.Spend = new(0.0)
		return snap, nil
	}
	hdr := map[string]string{"Authorization": "Bearer " + o.key}
	var total float64
	page := ""
	for range maxPages {
		q := url.Values{}
		q.Set("start_time", strconv.FormatInt(start.Unix(), 10))
		q.Set("end_time", strconv.FormatInt(end.Unix(), 10))
		q.Set("bucket_width", "1d")
		q.Set("limit", "31")
		if page != "" {
			q.Set("page", page)
		}
		var out openAICostPage
		if err := getJSON(ctx, o.hc, SourceOpenAI, "costs", o.base+"/v1/organization/costs?"+q.Encode(), hdr, &out); err != nil {
			return Snapshot{}, err
		}
		for _, b := range out.Data {
			for _, r := range b.Results {
				total += r.Amount.Value
			}
		}
		if !out.HasMore || out.NextPage == "" {
			break
		}
		page = out.NextPage
	}
	snap.Spend = &total
	return snap, nil
}

// --- Moonshot ----------------------------------------------------------

type moonshotSource struct {
	base, key string
	hc        *http.Client
}

// NewMoonshot reads the prepaid balance of a Moonshot account.
func NewMoonshot(baseURL, key string, hc *http.Client) Source {
	return &moonshotSource{trimBase(baseURL, "https://api.moonshot.ai"), key, httpClient(hc)}
}

func (m *moonshotSource) Name() string            { return SourceMoonshot }
func (m *moonshotSource) Interval() time.Duration { return 5 * time.Minute }

type moonshotBalance struct {
	Code int `json:"code"`
	Data struct {
		Available float64 `json:"available_balance"`
		Voucher   float64 `json:"voucher_balance"`
		Cash      float64 `json:"cash_balance"`
	} `json:"data"`
	SCode  string `json:"scode"`
	Status bool   `json:"status"`
}

func (m *moonshotSource) Fetch(ctx context.Context) (Snapshot, error) {
	var out moonshotBalance
	err := getJSON(ctx, m.hc, SourceMoonshot, "balance", m.base+"/v1/users/me/balance",
		map[string]string{"Authorization": "Bearer " + m.key}, &out)
	if err != nil {
		return Snapshot{}, err
	}
	if !out.Status || out.Code != 0 {
		return Snapshot{}, &Error{
			Source: SourceMoonshot, Op: "balance",
			Err: fmt.Errorf("provider rejected the read: code %d scode %q", out.Code, out.SCode),
		}
	}
	avail := out.Data.Available
	return Snapshot{
		Source: SourceMoonshot, Scope: "account", AsOf: time.Now(),
		Balance: &avail, Remaining: new(avail),
		Note: fmt.Sprintf("balance incl. vouchers: $%.2f (voucher $%.2f, cash $%.2f); no usage endpoint",
			avail, out.Data.Voucher, out.Data.Cash),
	}, nil
}

// --- OpenRouter --------------------------------------------------------

type openRouterSource struct {
	base, key string
	hc        *http.Client
}

// NewOpenRouter reads the limit and usage of one OpenRouter key.
func NewOpenRouter(baseURL, key string, hc *http.Client) Source {
	return &openRouterSource{trimBase(baseURL, "https://openrouter.ai"), key, httpClient(hc)}
}

func (r *openRouterSource) Name() string            { return SourceOpenRouter }
func (r *openRouterSource) Interval() time.Duration { return 60 * time.Second }

type openRouterKey struct {
	Data struct {
		Limit          *float64 `json:"limit"`
		LimitRemaining *float64 `json:"limit_remaining"`
		LimitReset     *string  `json:"limit_reset"`
		Usage          *float64 `json:"usage"`
		Effective      *struct {
			LimitUSD      *float64 `json:"limit_usd"`
			SpendUSD      *float64 `json:"spend_usd"`
			RemainingUSD  *float64 `json:"remaining_usd"`
			ResetInterval *string  `json:"reset_interval"`
			ResetsAt      *string  `json:"resets_at"`
		} `json:"effective_budget"`
	} `json:"data"`
}

func (r *openRouterSource) Fetch(ctx context.Context) (Snapshot, error) {
	var out openRouterKey
	err := getJSON(ctx, r.hc, SourceOpenRouter, "key", r.base+"/api/v1/key",
		map[string]string{"Authorization": "Bearer " + r.key}, &out)
	if err != nil {
		return Snapshot{}, err
	}
	d := out.Data
	snap := Snapshot{Source: SourceOpenRouter, Scope: "key", AsOf: time.Now()}
	if e := d.Effective; e != nil {
		snap.Limit, snap.Spend, snap.Remaining = e.LimitUSD, e.SpendUSD, e.RemainingUSD
		if e.ResetInterval != nil {
			snap.Window = *e.ResetInterval
		}
		if e.ResetsAt != nil {
			if t, err := time.Parse(time.RFC3339, *e.ResetsAt); err == nil {
				snap.ResetAt = &t
			}
		}
		return snap, nil
	}
	snap.Limit, snap.Remaining, snap.Spend = d.Limit, d.LimitRemaining, d.Usage
	if d.LimitReset != nil {
		snap.Window = *d.LimitReset
	}
	return snap, nil
}
