# Vendor billing / usage endpoints: reference for ADE AI budget page

Research date: 2026-11-11. Every claim cites a primary doc URL; `[INFERENCE]` = not stated in docs; "not documented" = could not find. Items from GitHub issues / third-party posts are labelled **UNDOCUMENTED**: they can change without notice.

Short summary:

| Vendor | Authoritative spend | Remaining balance | Key type needed |
|---|---|---|---|
| Anthropic API (Console org) | Yes, org-level, hours of lag | No (no balance/credit endpoint found) | Admin key (`sk-ant-admin...`), NOT a normal key |
| Claude Pro/Max (OAuth) | No dollars; only % of 5h/7d windows | n/a (percent used + reset time) | OAuth token; documented only via Claude Code statusline JSON |
| OpenAI | Yes, org-level daily buckets | No documented balance endpoint | Admin key |
| Moonshot/Kimi API | No usage/cost endpoint found | Yes (`available_balance`, USD) | Normal API key |
| Kimi Code (subscription) | n/a | Quota % only via UNDOCUMENTED endpoint | OAuth token |
| OpenRouter | Per-key usage, per-generation cost | Per-key `limit_remaining`; account credits need management key | Normal key for `/key` and `/generation`; management key for `/credits` |

---

## 1. Anthropic (Claude API / Console)

### 1.1 Key-type gate (applies to everything in 1.1-1.3)

- "The Admin API is unavailable for individual accounts." The org must be set up on the Claude Console Organization page. Source: https://platform.claude.com/docs/en/manage-claude/usage-cost-analytics-api
- Credentials accepted by the Admin endpoints: an Admin API key, or an OAuth token with scope `org:admin` (sent as `authorization: Bearer`), or (Claude Code analytics endpoint) a personal/service-account key not scoped to a workspace. **Workspace API keys do not work** on the Claude Code analytics endpoint. Sources: same page; https://platform.claude.com/docs/en/manage-claude/claude-code-analytics-api
- Admin key prefix `sk-ant-admin...`: this prefix is **[INFERENCE]/not shown in the pages fetched** (the user brief states it; the fetched docs use `$ANTHROPIC_ADMIN_KEY`).
- Header used in examples: `x-api-key: $ANTHROPIC_ADMIN_KEY` plus `anthropic-version: 2026-10-21` (docs examples use this version string).
- **Can a normal (inference) API key see anything?** Not through these endpoints: docs state Admin credentials are required. The only usage info a normal key gets is the per-response `usage` object and the `anthropic-ratelimit-*` headers (section 1.4). [Absence of any normal-key spend endpoint: not documented.]

### 1.2 Current endpoints: Usage and Cost Analytics API (beta) , recommended for new integrations

Source: https://platform.claude.com/docs/en/manage-claude/usage-cost-analytics-api (legacy endpoint pages say "For new integrations, use GET /v1/organizations/analytics/usage", e.g. https://platform.claude.com/docs/en/api/beta/organization/usage_report/retrieve_messages.md)

- **GET** `https://api.anthropic.com/v1/organizations/analytics/usage` (token + request counts)
- **GET** `https://api.anthropic.com/v1/organizations/analytics/cost` (USD cost)
- Required headers: `anthropic-version: 2026-10-21`, `anthropic-beta: analytics-usage-cost-2026-08-24`, `x-api-key: <admin key>`. Docs recommend setting `User-Agent: YourApp/1.0.0 (https://yourapp.com)`.
- Params (both endpoints):
  - `starting_at` (RFC 3339, inclusive), `ending_at` (exclusive). `starting_at` must be on/after 2026-01-01 and within last 365 days; max span 31 days per request; `ending_at` required whenever `time_bucket_width` is set; with `1m` buckets max span 24 h. Ends are rounded down to the bucket boundary (UTC).
  - `time_bucket_width`: `1m` | `1h` | `1d`; no default. Omit for a single whole-range total per group combination. (Contrast: legacy `bucket_width` defaulted to `1d`.)
  - `group_by[]`: up to 5 dims, e.g. `model`, `workspace_id`, `api_key_id`, `actor`, `actor_type`, `service_tier`, `context_window`, `inference_geo`, `speed`, `product`. Cost endpoint additionally accepts `cost_type` and `token_type` (don't count toward the 5 limit).
  - Filters in bracket notation, e.g. `models[]=...`, `api_key_ids[]=...`, `actor_ids[]=...`, up to 100 values each; same filter = OR, different filters = AND.
  - `order_by`: usage: `total_tokens` (default) | `output_tokens` | `uncached_input_tokens` | `requests` | `time`; cost: `amount` (default) | `list_amount` | `time`. `order=asc` reverses. For time series use `time_bucket_width` + `order_by=time`.
  - `limit`: rows per page, 1-1000, default 20. `page`: opaque cursor from `next_page`; loop while `has_more`.
- Response (rows, not buckets):
  - Row: `starting_at`, `ending_at`, `group{...dims, null unless grouped}`, `usage{...}` or `cost{...}`.
  - `usage`: `uncached_input_tokens`, `cache_creation{ephemeral_1h_input_tokens, ephemeral_5m_input_tokens}`, `cache_read_input_tokens`, `output_tokens`, `total_tokens`, `server_tool_use{web_search_requests}`, `requests` (all numbers).
  - `cost`: `amount` (decimal **string**, cost after discounts, before credits applied), `list_amount` (string, list price), `currency` (always `"USD"`), `requests`.
  - Top level: `data[]`, `has_more`, `next_page`, `data_refreshed_at` (RFC 3339 or null).
- **Units**: cost amounts are **cents as decimal strings with six decimal places**: `"41280.000000"` = $412.80. Tokens are raw counts.
- **Freshness**: "Data typically refreshes within 4 hours of the underlying usage and can take up to 24 hours." Usage after `data_refreshed_at` is missing/partial. Values can be revised until ~7 days after end of calendar month. (Same page, "Data freshness".)
- Rate limit: docs reference an "organization-level rate limit" shared with the Enterprise analytics API (https://platform.claude.com/docs/en/manage-claude/analytics-api#working-with-the-api); numeric value **not read/documented here**.

Trimmed usage example (copied from the page above):

```json
{
  "data": [
    {
      "starting_at": "2026-09-03T00:00:00Z",
      "ending_at": "2026-09-04T00:00:00Z",
      "group": { "model": "claude-sonnet-5", "workspace_id": null, "api_key_id": null },
      "usage": {
        "uncached_input_tokens": 1284500,
        "cache_creation": { "ephemeral_1h_input_tokens": 0, "ephemeral_5m_input_tokens": 215300 },
        "cache_read_input_tokens": 3200000,
        "output_tokens": 891000,
        "total_tokens": 5590800,
        "server_tool_use": { "web_search_requests": 12 },
        "requests": 4210
      }
    }
  ],
  "has_more": true,
  "next_page": "page_V2mQ4dkt9HcbX3pLr...",
  "data_refreshed_at": "2026-09-18T16:02:11Z"
}
```

(The page abbreviates `group` to the full 19-dim object; trimmed here.)

### 1.3 Legacy endpoints: Usage & Cost API (still live, marked "[Legacy]")

Sources: https://platform.claude.com/docs/en/api/beta/organization/usage_report/retrieve_messages.md ; https://platform.claude.com/docs/en/api/beta/organization/cost_report/retrieve.md

- **GET** `https://api.anthropic.com/v1/organizations/usage_report/messages`: params `starting_at` (req), `ending_at`, `bucket_width` `1d|1h|1m` (default 1d), `group_by[]` (`account_id, api_key_id, context_window, inference_geo, model, service_account_id, service_tier, speed, workspace_id`), filters (`models[]`, `api_key_ids[]`, `workspace_ids[]`, ...), `limit` (buckets: 1d default 7/max 31; 1h default 24/max 168; 1m default 60/max 1440), `page`. Response: `data[]` buckets `{starting_at, ending_at, results[]}` oldest first (empty buckets included), result fields: `uncached_input_tokens`, `cache_creation{ephemeral_1h_input_tokens, ephemeral_5m_input_tokens}`, `cache_read_input_tokens`, `output_tokens`, `server_tool_use.web_search_requests`, plus dims; `has_more`, `next_page`.
- **GET** `https://api.anthropic.com/v1/organizations/cost_report`: params `starting_at` (req), `ending_at`, `bucket_width` (only `1d`), `group_by[]` (`description`, `workspace_id`), `limit` (default 7, max 31), `page`. Result: `amount` (decimal string, **lowest currency units i.e. cents**: `"123.45"` USD = $1.23), `currency` (`"USD"`), `cost_type` (`tokens|web_search|code_execution|session_usage`), `description`, `model`, `token_type`, `service_tier` (`standard|batch`), `workspace_id`, etc.
- Freshness: legacy docs claim "Data typically appears within 5 minutes" per the migration table at https://platform.claude.com/docs/en/manage-claude/usage-cost-analytics-api#migrate-from-usage-cost-api (vs 4h/24h for new API). Useful for ADE if the legacy endpoint stays up, but it is labelled legacy so deprecation risk is real.
- Same Admin-key requirement.

Trimmed cost example (legacy; copied from cost_report page):

```json
{
  "data": [{
    "starting_at": "2025-08-01T00:00:00Z",
    "ending_at": "2025-08-02T00:00:00Z",
    "results": [{
      "amount": "123.78912", "currency": "USD", "cost_type": "tokens",
      "description": "Claude Opus 5.5 Usage - Input Tokens",
      "model": "claude-opus-5-5", "service_tier": "standard",
      "token_type": "cache_creation.ephemeral_1h_input_tokens",
      "workspace_id": "wrkspc_01JwQvzr7rXLA5AGx3HKfFUJ"
    }]
  }],
  "has_more": true,
  "next_page": "page_MjAyNS0wNS0xNFQwMDowMDowMFo="
}
```

### 1.4 Claude Code Analytics API (Console orgs)

Source: https://platform.claude.com/docs/en/manage-claude/claude-code-analytics-api

- **GET** `https://api.anthropic.com/v1/organizations/usage_report/claude_code?starting_at=YYYY-MM-DD&limit=&page=`. Auth: Admin key / `org:admin` OAuth / non-workspace-scoped personal or service key.
- One UTC day per call (`starting_at` = date). `limit` default 20, max 1000. Cursor pagination.
- Per-actor records: `actor` (`user_actor.email_address` or `api_actor.api_key_name`), `customer_type` (`api|subscription`), `terminal_type`, `core_metrics{num_sessions, lines_of_code, commits, PRs}`, `tool_actions`, `model_breakdown[]{model, tokens{input,output,cache_read,cache_creation}, estimated_cost{amount (cents USD), currency}}`.
- Freshness: up to 1 hour delay; daily aggregates only (docs point to OpenTelemetry for real time).
- Does **not** include Pro/Max/Team/Enterprise subscription usage (attributed to claude.ai org). Records cover only usage authenticated against the Console org. Estimated cost is "estimated". Free to use.
- Relevance to ADE: low. It is per-user Claude Code activity, redundant with `/analytics/cost?products[]=claude_code&group_by[]=actor` which the new docs recommend instead.

### 1.5 Rate-limit response headers on normal Messages API calls

Source: https://platform.claude.com/docs/en/api/rate-limits (Response headers table)

Present on API-key requests: `anthropic-ratelimit-requests-{limit,remaining,reset}`, `anthropic-ratelimit-tokens-{limit,remaining,reset}`, `anthropic-ratelimit-input-tokens-{limit,remaining,reset}`, `anthropic-ratelimit-output-tokens-{limit,remaining,reset}`, plus Priority-Tier `anthropic-priority-*`, `retry-after` on 429, `anthropic-workspace-id`. `*-reset` values are RFC 3339 timestamps; token `remaining` is rounded to nearest thousand; `tokens-*` shows the most restrictive limit in effect.
These are **rate limits (RPM/ITPM/OTPM), not spend**. They say nothing about dollars.

Spend-cap behaviour (relevant to ADE enforcement): each tier has a monthly org spend cap (Start $500, Build $1,000, Scale $200,000; Custom none). At the platform cap, requests get **HTTP 429** `rate_limit_error` with `error.details.error_code = "enforced_spend_limit_reached"` and **no `retry-after`**. At a user-set spend limit, requests get **HTTP 400** `invalid_request_error` starting "You have reached your specified API usage limits" (or "...workspace API usage limits"). Source: https://platform.claude.com/docs/en/api/rate-limits#reaching-your-spend-cap . ADE can use these error shapes as a hard backstop signal. A `spend-limit-reads-2026-09-26` anthropic-beta value appears in the legacy endpoint header enum, suggesting a read-spend-limit endpoint exists; its endpoint/shape was **not found/not documented in what I read**.

### 1.6 What Anthropic API endpoints CANNOT tell us

- No remaining prepaid-credit balance endpoint found (not documented).
- No normal-key way to read org spend.
- Lag: hours (new) / ~5 min claimed (legacy). Not suited to per-request enforcement.
- Cost `amount` is "before any credits are applied".

---

## 2. Claude Pro / Max subscription usage (Claude Code OAuth)

### 2.1 DOCUMENTED

**a) Status line JSON (stdin to the user's statusLine command).** Source: https://code.claude.com/docs/en/statusline

Config: `~/.claude/settings.json` -> `{"statusLine":{"type":"command","command":"...","refreshInterval":N}}`. Fields relevant here:

| Field | Meaning |
|---|---|
| `rate_limits.five_hour.used_percentage`, `.resets_at` | % (0-100) of 5-hour window consumed; Unix epoch seconds reset |
| `rate_limits.seven_day.used_percentage`, `.resets_at` | same for weekly window |
| `rate_limits.spend_limit.used_percentage`, `.resets_at`, `.used_usd`, `.limit_usd`, `.period` | Only behind a Claude apps gateway with spend limits (v2.1.251+; USD fields v2.1.284+, may be absent) |
| `cost.total_cost_usd` | Session cost, **computed client-side at list price** (unless `modelPricing` set), "May differ from your actual bill"; resets on `/clear` |
| `cost.total_duration_ms`, `total_api_duration_ms`, `total_lines_added/removed` | session stats |
| `context_window.*`, `model.id/display_name`, `session_id`, `prompt_cache.*` | context/model/cache info |

Quote: "The `rate_limits` object is only present for claude.ai Pro, Max, and Ultra subscribers, or behind a Claude apps gateway with spend limits, and only after the first API response." Script re-runs when a window's `resets_at` passes, on each assistant message, etc.

Example (trimmed from page):

```json
{
  "model": {"id": "claude-opus-5-5", "display_name": "Opus"},
  "cost": {"total_cost_usd": 0.01234, "total_duration_ms": 45000, "total_api_duration_ms": 2300},
  "context_window": {"used_percentage": 8, "context_window_size": 200000}
}
```

(`rate_limits` is described in the field table but not in the page's sample JSON; shape is `rate_limits.five_hour.{used_percentage,resets_at}` per the table.)

**b) `/usage` command.** Source: https://code.claude.com/docs/en/costs
- Session block: token counts and local-computed dollar estimate (list price); "The Session block in `/usage` shows API token usage and is intended for API users. Pro, Max, Ultra subscribers have usage included in their subscription, so the session cost figure isn't relevant for billing."
- Subscribers also get plan usage bars (5h/weekly), attribution (skills/subagents/MCP), usage-credits row (spend for current month vs your monthly spend limit, if set).
- Plan breakdown figures are "approximate and computed from local session history on this machine, so usage from other devices or claude.ai is not included." (The plan bars themselves come from a server usage endpoint; see 2.2; docs mention "the usage endpoint is rate limited" and show last-known data for up to 60 min.)
- `/cost` is not described on the pages I read; `/usage` carries the Session block (Total cost, API duration, code changes, per-model tokens).
- Usage credits (pay-past-limit) balance/spend/limit: viewable at claude.ai Settings > Usage (`/usage-credits` opens it). No API documented.

**c) Admin-level for subscription orgs.** Team: spend report CSV in claude.ai admin analytics. Enterprise: Enterprise Analytics API with `read:analytics` key (https://platform.claude.com/docs/en/manage-claude/analytics-api). "Usage inside the seat allowance isn't metered in dollars." (costs page.) Not applicable to an individual Pro/Max user.

**d) OpenTelemetry export from Claude Code** works on any plan: https://code.claude.com/docs/en/monitoring-usage (linked from costs page; not read in detail here).

### 2.2 UNDOCUMENTED (community-reverse-engineered; may break)

Sources are GitHub issues / blogs, **not Anthropic docs**:

- **`GET https://api.anthropic.com/api/oauth/usage`**, headers `Authorization: Bearer <claude.ai OAuth access token>`, `anthropic-beta: oauth-2025-04-20` (one author also reports a `User-Agent: claude-code/<ver>` is needed; unverified). Returns `five_hour` and `seven_day` objects `{utilization: <0-100 percent>, resets_at: <ISO8601>}`, also `seven_day_opus`, `seven_day_sonnet`, and a `limits` array in some implementations. Windows can be `null` (reported 2026-11 that some plans have `five_hour: null`; treat `{utilization:0, resets_at:null}` as idle, null as absent). Reported to 429 persistently if polled hard; tools cache 5-30 min. Not for enterprise plans. Sources: https://github.com/Maciek-roboblog/Claude-Code-Usage-Monitor/issues/202 , https://github.com/anthropics/claude-code/issues/31021 , https://github.com/anthropics/claude-code/issues/34346 , https://github.com/NousResearch/hermes-agent/issues/58226 , https://github.com/hank-warren/pi-extensions/issues/89 . Claude Code's own docs allude to this "usage endpoint is rate limited" (costs page) but do not publish its URL or schema.
- **`anthropic-ratelimit-unified-*` response headers** on `/v1/messages` calls made with the subscription OAuth login: `...-5h-utilization`, `...-7d-utilization` (fraction 0-1), `...-5h-reset`, `...-7d-reset` (Unix seconds), `...-5h-status`/`...-7d-status` (`allowed|allowed_warning|rejected`), `...-representative-claim` (`five_hour|seven_day`), `...-status`, `...-reset`, `...-overage-status`, `...-fallback-percentage`. Sources: https://www.ssdnodes.com/learn/claude-api-rate-limit-headers-explained , https://github.com/jonmast/cpa-quota-api-extension/issues/22 , https://github.com/rossoctl/context-guru/issues/984 . Docs only confirm the Claude apps gateway header family indirectly; Anthropic's rate-limits page documents only `anthropic-ratelimit-{requests,tokens,input-tokens,output-tokens}-*`. These headers are also the source of the documented statusline `rate_limits` ([per community analysis](https://github.com/rossoctl/context-guru/issues/984), unverified by Anthropic docs).
- Third-party/UNDOCUMENTED note: using the OAuth token outside Claude Code may violate ToS. [INFERENCE: not researched, flag for review before shipping.]

### 2.3 What subscription data CANNOT tell us

- No dollars: subscription use is percent-of-window only. No tokens-remaining, no per-session attribution via API.
- Claude Code's `cost.total_cost_usd` is a list-price estimate and is *not* what a subscriber pays.
- Window set can change (5h window reportedly absent on some plans in 2026-11).

---

## 3. OpenAI

### 3.1 Usage API

Source: https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage/index.md

- Base `https://api.openai.com/v1`. Auth: `Authorization: Bearer $OPENAI_ADMIN_KEY`. Examples in the reference use an **Admin key**; creating admin keys / the exact role needed: not documented on the pages I read. A normal project API key is not shown working. [INFERENCE: normal keys are rejected.]
- Endpoints (all GET): `/organization/usage/completions`, `/embeddings`, `/moderations`, `/images`, `/audio_speeches`, `/audio_transcriptions`, `/vector_stores`, `/code_interpreter_sessions`, `/file_searches`, ... (also `/decisions`).
- Params: `start_time` (Unix seconds, required, inclusive), `end_time` (exclusive), `bucket_width` `1m|1h|1d` (default `1d`), `limit` (1d: default 7, max 31; 1h: default 24, max 168; 1m: default 60, max 1440), `page` (cursor from `next_page`), `group_by[]` (completions: `project_id, user_id, api_key_id, model, batch, service_tier, api_source`), filters `project_ids[]`, `user_ids[]`, `api_key_ids[]`, `models[]`.
- Response: `{object:"page", data:[{object:"bucket", start_time, end_time, results:[...]}], has_more, next_page}`. Completions result: `input_tokens` (incl. cached), `output_tokens`, `num_model_requests`, `input_cached_tokens`, `input_cache_write_tokens` (+30m/12h variants), audio/image/text splits, and the grouped dims (`project_id`, `model`, ... null unless grouped).
- Trimmed example (from doc):

```json
{ "object":"page", "has_more":false, "next_page":null,
  "data":[{ "object":"bucket","start_time":1730419200,"end_time":1730505600,
    "results":[{ "object":"organization.usage.completions.result",
      "input_tokens":1000,"output_tokens":500,"num_model_requests":5,
      "project_id":null,"user_id":null,"api_key_id":null,"model":null }]}]}
```
- Units: tokens (counts). Freshness and rate limits for the usage endpoints: **not documented** in the reference pages read. (A third-party post claims minute-level bucket availability; don't rely on it.)

### 3.2 Costs API

Same reference page. **GET** `https://api.openai.com/v1/organization/costs`.
- Params: `start_time` (req), `end_time`, `bucket_width` (**only `1d`**), `limit` (1-180, default 7), `page`, `group_by[]` (`project_id, user_id, line_item, api_key_id, api_source`; "Support for combining `user_id` with `project_id` grouping or the `project_ids` filter depends on the organization and requested time range. Unsupported combinations return HTTP 400."), filters `project_ids[]`, `api_key_ids[]`, `line_items[]` (exact, e.g. `gpt-6-astra, input_tokens`).
- Result: `{object:"organization.costs.result", amount:{value:<number>, currency:"usd"}, line_item, project_id, api_key_id, quantity, quantity_unit}`. `quantity_unit` in `tokens|1000_tokens|duration_seconds|duration_minutes|duration_hours|gibibyte_hours|images|characters`.
- **Units: `amount.value` is a number in major units of `currency` ("usd")**, example 0.06 = $0.06 [INFERENCE: dollars, not cents; docs example shows `value: 0.06` and `currency: "usd"` but do not state the major/minor unit explicitly].
- Example:

```json
{"object":"page","data":[{"object":"bucket","start_time":1730419200,"end_time":1730505600,
 "results":[{"object":"organization.costs.result","amount":{"value":0.06,"currency":"usd"},
 "line_item":null,"project_id":null,"api_key_id":null,"quantity":null,"quantity_unit":null}]}],
 "has_more":false,"next_page":null}
```
- Daily granularity only; Scale Tier bundle costs are attributed to the org, not projects (https://help.openai.com/en/articles/10478918-reviewing-api-usage-and-costs).
- Dashboard access separately needs Owner or "Usage Dashboard" permission (same help page). Whether the Admin key maps to that permission: not documented.

### 3.3 Rate-limit headers on normal calls

Source: https://developers.openai.com/api/docs/guides/rate-limits#rate-limits-in-headers
`x-ratelimit-limit-requests`, `x-ratelimit-limit-tokens`, `x-ratelimit-remaining-requests`, `x-ratelimit-remaining-tokens`, `x-ratelimit-reset-requests` (e.g. `1s`), `x-ratelimit-reset-tokens` (e.g. `6m0s`), project-scoped `x-ratelimit-*-project-tokens`, `Retry-After` on 429/503. Rate limits (RPM/TPM), not dollars.
Also documented: monthly **spend limits** at org/project level; at a hard limit "affected API requests return a `429` error" (https://developers.openai.com/api/docs/guides/rate-limits#spend-limits). Usage tiers carry monthly usage limits (Free $100, Build $500, Launch $5,000, Grow $200,000).

### 3.4 Credit balance

- **No documented credit-balance API endpoint.** Docs point to the dashboard: "Credit-grant details are available on your organization's Billing page" (https://help.openai.com/en/articles/10478918-reviewing-api-usage-and-costs#view-credit-grants). The old `/dashboard/billing/credit_grants`-style dashboard endpoints are not documented; **not researched/verified** [INFERENCE: treat as unavailable].
- Not available: remaining prepaid balance, remaining headroom under spend limit via API (not documented).

---

## 4. Moonshot AI / Kimi

### 4.1 Balance (API platform)

Source: https://platform.kimi.ai/docs/api/balance.md (also served as platform.moonshot.ai/docs/api/balance)

- **GET** `https://api.moonshot.ai/v1/users/me/balance` . Auth: `Authorization: Bearer $MOONSHOT_API_KEY` (a normal server-side API key; no admin role mentioned).
- `.cn` variant: the docs page I fetched gives only `api.moonshot.ai`. Doc note: "API Keys from `platform.kimi.ai` and `platform.kimi.com` are completely independent... Using a Key from one platform on the other will result in a 401." Balance URL for `api.moonshot.cn`/`platform.kimi.com`: **not documented on the fetched page** [INFERENCE: same path at `https://api.moonshot.cn/v1/users/me/balance`; verify by test]. Currency of the CN endpoint (CNY vs USD): not documented here.
- Response (copied from doc):

```json
{ "code": 0,
  "data": { "available_balance": 49.58894, "voucher_balance": 46.58893, "cash_balance": 3.00001 },
  "scode": "0x0", "status": true }
```
- Fields: `data.available_balance` (number, **USD**, = cash + voucher; `<= 0` blocks inference with `exceeded_current_quota_error`), `data.voucher_balance` (USD, never negative), `data.cash_balance` (USD, may be negative = owed; when negative `available_balance == voucher_balance`). Also `code` (0 = success), `scode`, `status`.
- Errors: 401 invalid key, 500.
- Freshness, rate limit of this endpoint: not documented.

### 4.2 Usage / cost endpoint

- **None found** on the Kimi/Moonshot API docs (docs index: https://platform.kimi.ai/docs/llms.txt, not exhaustively crawled). Per-request token counts come in the chat completion `usage` object [INFERENCE: standard OpenAI-compatible; not verified here]. So spend must be computed locally from tokens x price, and cross-checked against balance deltas.

### 4.3 Kimi Code subscription (Kimi membership)

Documented, source https://www.kimi.com/code/docs/en/kimi-code/membership :
- Quota windows: rolling **5-hour rate window**; legacy plans also have a **7-day** window; all plans have a **monthly total quota** (new plans: no weekly window). Extra Usage balance (prepaid top-up in **RMB**, shared with Kimi web) with optional monthly spend cap.
- Where to read quota: Kimi Code Console (https://www.kimi.com/code/console), web "My Quota" page, and CLI `/usage` command. **No public API documented** for quota.

UNDOCUMENTED (community report): `GET https://api.kimi.com/coding/v1/usages` (also `api.kimi.ai`), called with the CLI session's OAuth token, returns `usages.limit_5h.used_ratio`, `usages.limit_7d.used_ratio`, legacy `used`/`limit` counters (e.g. `"10"`/`"100"`), and maybe `limit_month_total` / `limit_month_code`. Same report says the ratio fields were 0 while the console showed ~48.6%, i.e. **unreliable**. Source: https://github.com/MoonshotAI/kimi-code/issues/4133 (and issue #3817 referenced there). Do not trust without live verification.

### 4.4 Cannot tell us

No usage history, no per-key breakdown, no tokens consumed, nothing for the subscription plan except via undocumented endpoint.

---

## 5. OpenRouter

Base `https://openrouter.ai/api/v1`. Rate limits and key info: https://openrouter.ai/docs/api_reference/limits.md

### 5.1 GET `/api/v1/key` (current key info + budgets)
- Auth: `Authorization: Bearer <normal OpenRouter API key>` (the key being inspected; docs: "If you submit a valid API key").
- Response `data` (from the Limits page type definition): `label`, `limit` (number|null, key credit cap), `limit_reset` (string|null), `limit_remaining` (number|null), `include_byok_in_limit`, `usage` (all-time), `usage_daily` (UTC day), `usage_weekly` (UTC week from Monday), `usage_monthly` (UTC month), `byok_usage[_daily|_weekly|_monthly]`, `is_free_tier`, `free_model_daily_requests{used,limit,remaining}`, `budgets[]` and `effective_budget` (each `{scope, guardrail_id, budget_id, limit_usd, spend_usd, remaining_usd, reset_interval (daily|weekly|monthly|null), resets_at (ISO 8601 UTC|null), include_byok}`).
- Units: `usage*`, `limit*` are "credits"; `*_usd` fields are explicitly USD [INFERENCE: 1 credit = $1; page says "credits" without stating USD for those fields]. Budget windows are UTC.
- Example (from page, excerpt):

```json
{ "data": { "limit": null, "limit_remaining": null,
  "budgets": [{ "scope": "member", "limit_usd": 50, "spend_usd": 40, "remaining_usd": 10,
                "reset_interval": "weekly", "resets_at": "2026-10-12T00:00:00.000Z", "include_byok": false }],
  "effective_budget": { "scope": "member", "limit_usd": 50, "spend_usd": 40, "remaining_usd": 10 } } }
```
- Rate limit: per-IP burst limits apply to `GET /api/v1/key` and `GET /api/v1/credits`; responses don't include `X-RateLimit-*` headers; "wait a few seconds and retry with exponential backoff". Numeric values not documented. Docs suggest calling `/key` proactively to track `limit_remaining`.
- Freshness: not documented.
- Cannot tell: per-model/per-generation breakdown; account-level credit balance (use `/credits`).

### 5.2 GET `/api/v1/credits` (account balance)
Source: https://openrouter.ai/docs/api/api-reference/credits/get-remaining-credits.md
- **Requires a management key** ("Only management keys can perform this operation": 403 otherwise).
- Response: `{"data":{"total_credits":100.5,"total_usage":25.75}}` (double). Remaining = `total_credits - total_usage` [INFERENCE: computed locally; docs give no `remaining` field]. Units: credits (USD [INFERENCE]).
- Reading credits "doesn't count toward" the key-management daily limit (Limits page) but is subject to per-IP burst limit.

### 5.3 GET `/api/v1/generation?id=gen-...` (per-request cost)
Source: https://openrouter.ai/docs/api/api-reference/generations/get-request-&-usage-metadata-for-a-generation.md
- Query `id` required, pattern `^gen-[0-9A-Za-z-]+$`, max 128 chars. The `gen-...` ID is the `id` returned in the chat completion response [INFERENCE from example; not stated on this page]. Auth: Bearer key (spec `security: apiKey`; its 403 text mentions management key permission, so exact key requirement is ambiguous).
- Response `data` (example): `id`, `model`, `provider_name`, `created_at`, `total_cost` (0.0015), `usage` (0.0015), `upstream_inference_cost` (0.0012, BYOK/upstream), `cache_discount`, `tokens_prompt`, `tokens_completion`, `native_tokens_prompt/completion/cached/reasoning`, `latency`, `generation_time`, `is_byok`, `finish_reason`, `streamed`, `external_user`, ...
- Units: USD credits [INFERENCE]. Freshness: not documented (generation records may lag shortly after completion; not documented).
- Alternative that avoids a second call: not researched. [INFERENCE: chat completion `usage` object with cost field exists but not verified here.]

### 5.4 Error-based signals (useful for enforcement)
- 402 with `error.metadata.limit_source` in `openrouter_key_limit | openrouter_credits | openrouter_in_flight_budget`; 403 budget errors with `limit_source` `openrouter_guardrail_budget | openrouter_workspace_budget | openrouter_key_limit` plus `limit_usd, spend_usd, resets_at` (Limits page).

---

## ADE driver recommendation

Principle: poll authoritative sources at a low cadence to *calibrate*; compute spend locally from per-response token usage in between; honor each vendor's hard-stop errors as the enforcement signal.

| Provider | Poll | Cadence | Compute locally |
|---|---|---|---|
| **Anthropic API** | If user supplies an Admin key: `GET /v1/organizations/analytics/cost?starting_at=<first of month>&ending_at=<now rounded>&group_by[]=workspace_id` (or `api_key_id` to isolate the key ADE uses). Parse `cost.amount` as cents (string -> decimal). Use `data_refreshed_at` to know how stale. Legacy `cost_report` is an option for ~5 min freshness but marked legacy. | Every 15-60 min while app is open, plus once on page open. Data is 4-24 h behind per docs, so more frequent polling buys nothing. | Cost since `data_refreshed_at` = sum(tokens x price table) from each response `usage` (input, output, `cache_read_input_tokens`, cache creation 5m/1h). Display `authoritative + local delta`. Treat 429 `enforced_spend_limit_reached` and 400 "reached your specified API usage limits" as hard stops. Without an admin key: local-only. |
| **Claude Pro/Max** | Documented-only route: none from outside Claude Code. For ADE-embedded use, the status line `rate_limits.{five_hour,seven_day}` is the only documented source (needs the user to wire a statusLine script to feed ADE). UNDOCUMENTED options: `GET /api/oauth/usage` (<= 1 call / 5 min, cache; handle null windows and 429) or read `anthropic-ratelimit-unified-*` headers from ADE's own calls (free, per-request). Prefer headers. | Headers: every response. Endpoint: >= 5 min. | Nothing in dollars. Show % and `resets_at`. Local `$` only as "API-equivalent estimate" (list price), label as not billed. |
| **OpenAI** | With Admin key: `GET /v1/organization/costs?start_time=<month start>&bucket_width=1d&group_by[]=project_id` (and `/usage/completions?bucket_width=1h` for tokens). `amount.value` major units USD [INFERENCE]. | 15-60 min [freshness not documented, so be conservative]. | Delta since last bucket from response `usage` x price table. Handle 429 on hard spend limit. No balance API: show spend vs user-configured cap only. |
| **Moonshot/Kimi API** | `GET https://api.moonshot.ai/v1/users/me/balance` (normal key). Store `available_balance`, `cash_balance`, `voucher_balance` (USD). Spend = (previous balance - current balance) between polls, plus top-up detection when balance increases. | Every 5 min, and after each batch of requests. Rate limit not documented, so keep modest. | Per-request cost from token counts x price table to fill in between polls. Block at `available_balance <= 0` (API itself returns `exceeded_current_quota_error`). `.cn` endpoint: verify separately; currency may differ. |
| **Kimi Code subscription** | No documented API. UNDOCUMENTED `GET https://api.kimi.com/coding/v1/usages` reportedly returns zero ratios; unreliable. Recommend: show "n/a" or read from user-pasted console data; compute local request counts. | n/a | Local counts only. |
| **OpenRouter** | Per-key: `GET /api/v1/key` (normal key): use `limit_remaining`, `usage_daily/weekly/monthly`, `effective_budget.remaining_usd`. Account balance: `GET /api/v1/credits` only if user supplies a management key (`total_credits - total_usage`). Per-request exact cost: `GET /api/v1/generation?id=` after each call (optional; or use local calc). | `/key` every 1-5 min while a session is active (per-IP burst limit exists, so don't loop faster); `/credits` every 5-15 min. | Between polls: tokens x price from the model list pricing. Treat 402 `limit_source` and 403 budget errors as hard stops. |

General notes for the driver:
1. Normalize every provider to `{spent_usd, limit_usd?, remaining_usd?, window, as_of, source: authoritative|computed}` and always show `as_of`.
2. Anthropic cost amounts are **cents as strings**; OpenAI `amount.value` is a number in (probably) dollars; Moonshot balances are USD floats; OpenRouter credits are USD-equivalent [INFERENCE]. Convert once at the driver boundary using decimal, not float, for Anthropic strings.
3. Authoritative billing APIs lag (Anthropic 4-24 h, others undocumented). Never rely on them alone for enforcement; hard caps must be enforced on the local running total, with the vendor's 4xx/429 spend-limit errors as a secondary backstop.
4. Admin/management keys are a higher-privilege secret than inference keys: store separately and make them optional (feature degrades to local-only).
5. Subscription plans (Claude Pro/Max, Kimi Code) have no dollar-denominated budget; model them as quota windows (% used + reset time), separate from the USD budget math.
