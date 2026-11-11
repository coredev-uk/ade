# LiteLLM Proxy: spend, budget and usage reporting

Base URL: whatever the operator deployed, e.g. `https://litellm.corp.example` (default port 4000). Auth for everything below is `Authorization: Bearer sk-...`; `x-api-key` also works on some routes (see the `/model_group/info` docstring in `proxy_server.py`).
Everything is **USD, as a JSON float** (not cents, not tokens).
Researched 2026-11-11 against docs.litellm.ai and the BerriAI/litellm `main` source. Source-code citations point at `https://raw.githubusercontent.com/BerriAI/litellm/main/litellm/proxy/...` ("src:" below). A self-hosted proxy may run an older version than `main`; route availability must be probed at runtime.

Realistic ADE situation: the developer holds **one virtual key** (role `internal_user` or a team key), not the master key and not an enterprise `get_spend_routes` key. Section 0 summarises what that key can do.

## 0. Summary: what a plain virtual key can see

| Need | Call | Plain virtual key? |
|---|---|---|
| Cost of the request just made | response header `x-litellm-response-cost` | yes |
| Key's running spend + cap, after each request | headers `x-litellm-key-spend`, `x-litellm-key-max-budget` | yes |
| Key spend / max_budget / reset time / models / limits | `GET /key/info` (no params = own key) | yes |
| User-level spend/budget | `GET /user/info` (own `user_id` only) | yes, if key has a `user_id` |
| Team spend/budget | `GET /team/info?team_id=` | yes, only for own team (key's team or membership) |
| End-user (customer) spend | `GET /customer/info` | **no** (admin-view roles only) |
| Per-request spend rows | `GET /spend/logs`, `/spend/logs/v2` | yes, scoped to own `user_id` for `internal_user` roles |
| Daily per-model breakdown | `GET /user/daily/activity?user_id=<own>` | yes, own user only |
| Org-wide reports | `/global/spend/report`, `/key/spend/report`, ... | **no**: enterprise licence AND admin (or `get_spend_routes` key) |
| Per-token prices | `GET /model/info`, `/model_group/info` | yes (models the key may use) |
| Remaining balance | no dedicated endpoint; compute `max_budget - spend` | n/a |

## 1. Per-request cost: response headers and body

Source: https://docs.litellm.ai/docs/proxy/response_headers and src: `common_request_processing.py` (`cost_headers`, header assembly near "x-litellm-key-max-budget").

Attached to every proxied LLM response (`/chat/completions`, `/v1/messages`, `/responses`, ...), including errors where the proxy assembles headers.

| Header | Type | Meaning |
|---|---|---|
| `x-litellm-response-cost` | float (USD, string-encoded) | Cost of this call. Cache hits report `0.0` (src). Omitted when the model has no known price and cost computed 0 (src: `cost_headers`). |
| `x-litellm-response-cost-input` / `-output` / `-cache-read` / `-cache-creation` / `-reasoning` / `-tool-usage` | float | Component costs. Sum of input + cache-read + cache-creation + output + tool-usage = total; reasoning is a subset of output. Cache/reasoning headers appear only when nonzero; component headers appear **on non-streaming responses only** (docs). |
| `x-litellm-response-cost-original`, `-discount-amount`, `-margin-amount`, `-margin-percent` | float | Present in source only (`cost_headers`); not in the docs table. [INFERENCE] used when operators configure discounts/margins. |
| `x-litellm-key-spend` | float (USD) | Key's total spend **including this call**. Source computes `key.spend (as cached at auth time) + response_cost` (src), so it is an estimate, not a fresh DB read. Docs: "Total spend for the API key"; also available on pass-through endpoints. |
| `x-litellm-key-max-budget` | string | Key's `max_budget`; the literal string `None` when no cap (src: `str(user_api_key_dict.max_budget)`). Not in the docs header table; present in source. |
| `x-litellm-key-tpm-limit`, `x-litellm-key-rpm-limit` | string | Key limits, `None` if unset (src). |
| `x-ratelimit-remaining-requests`, `-remaining-tokens`, `-limit-requests`, `-limit-tokens`, `-reset-requests`, `-reset-tokens` | int | Key-level limits when the key has them; otherwise passed-through provider values or absent (docs). |
| `x-litellm-call-id`, `x-litellm-model-id`, `x-litellm-model-group`, `x-litellm-model-api-base`, `x-litellm-version` | string | Correlation / routing. `call-id` = `request_id` in spend logs [INFERENCE]. |
| `x-litellm-attempted-retries`, `-attempted-fallbacks` | int | Retry/fallback counts. |
| `x-litellm-response-duration-ms`, `x-litellm-overhead-duration-ms` | float | Latency. |
| `llm_provider-*` | string | Upstream provider headers, prefixed. |

Example (docs, "More examples"): `x-litellm-response-cost: 0.000214`, `x-litellm-key-spend: 12.847`.

**There is no remaining-budget header.** Docs list no `x-litellm-key-remaining-budget` or similar; compute `x-litellm-key-max-budget - x-litellm-key-spend` client-side.

**Streaming caveat.** Docs: "HTTP headers cannot change after a stream commits" (response_headers page, auto-router section). Source has a `_resolve_stream_headers` refresh hook (src: `common_request_processing.py`), but I could not confirm that cost headers are populated for streamed responses before the first byte. [INFERENCE] For streams, ADE should not rely on `x-litellm-response-cost`; compute from the final `usage` chunk x model prices, then reconcile with `/key/info`.

**Response body.** No documented cost field in the OpenAI-shaped body. Cost is only in headers (and the spend logs). Body `usage` carries tokens only. (Not documented otherwise; https://docs.litellm.ai/docs/proxy/cost_tracking points to headers.)

**Hidden-cost gotchas**
- Requests with usage but no price entry are logged with `spend=0` and a warning, not flagged in the response (https://docs.litellm.ai/docs/proxy/cost_tracking, "Requests that price to $0").
- LiteLLM cost = LiteLLM's `model_prices_and_context_window.json` (or operator override) x tokens, i.e. **computed locally by the proxy, not read from the provider's bill** (https://docs.litellm.ai/docs/proxy/virtual_keys "How is it calculated?"). It can diverge from the provider invoice (https://docs.litellm.ai/docs/troubleshoot/cost_discrepancy).
- `user`/`x-litellm-customer-id` in a request attributes spend to an end-user record but the key still accrues spend.

## 2. Key level: `GET /key/info`

Source: https://docs.litellm.ai/docs/proxy/virtual_keys ("Spend Tracking"), src: `management_endpoints/key_management_endpoints.py` (`info_key_fn`, `_can_user_query_key_info`).

- **Request:** `GET {base}/key/info` with `Authorization: Bearer <key>`. Query `key=` is optional: "if no key is passed, it will use the Key passed in Authorization Header" (src docstring). `key` accepts plaintext or sha256 hash (hash preferred to keep it out of access logs).
- **Auth:** any valid key querying **itself**. Allowed callers (src `_can_user_query_key_info`): proxy admin / admin viewer; `api_key == key` (own key); a key whose `user_id` matches the target's owner; a member of the target key's team (default team-member permissions include `/key/info`, https://docs.litellm.ai/docs/proxy/access_control). Otherwise `403`. Unknown key: `404 Key not found in database`. Needs a database (`prisma_client`), else error.
- **Not enterprise.**
- **Response:** `{"key": "<as passed>", "info": {...}}`. Fields in `info` (src docstring):

| Field | Type | Notes |
|---|---|---|
| `spend` | float USD | "When `budget_duration` is set this covers only the current budget window, not the key's lifetime". |
| `max_budget` | float or null | Hard cap enforced against `spend`. |
| `budget_duration` | string or null | `"30s"/"30m"/"30h"/"30d"`, also `"1mo"` for per-model budgets. |
| `budget_reset_at` | ISO datetime or null | When the current window ends and spend resets to 0. Resets snap to boundaries (30d/1mo = 1st of month, 7d = Monday, 1h = on the hour), so `reset_at - duration` is not the window start. |
| `soft_budget` | float or null | Alert threshold only (section 5). Present on the key row (`soft_budget` in `LiteLLM_VerificationToken`; shown in `/key/generate` response, users doc). Not in the `/key/info` docstring field list. [INFERENCE] returned because `info` is the full row. |
| `model_max_budget` | dict | `{"gpt-4": {"budget_limit": 0.0005, "time_period": "30d"}}`. |
| `model_max_budget_usage` | dict, only if per-model budgets | `{model: {current_spend, budget_limit, time_period, budget_reset_at}}`. |
| `budget_limits`, `budget_limits_usage` | list/dict | Concurrent windows, e.g. usage `{"1h": {"current_spend": 0.0009}}`. |
| `models` | list | Allowed model names. |
| `tpm_limit`, `rpm_limit`, `max_parallel_requests` | int or null | |
| `metadata` | dict | |
| `blocked`, `expires`, `last_active`, `status` (`active/expired/revoked/deleted`), `key_alias`, `team_id`, `user_id` | | |
| `litellm_budget_table` | object or null | Linked shared budget row (`max_budget`, `budget_duration`, ...); [INFERENCE] a key with `budget_id` may carry its cap here rather than on the key row. |

Trimmed example (docs, virtual_keys page):
```json
{"key": "sk-tXL0wt5-lOOVK9sfY2UacA",
 "info": {"token": "sk-tXL0wt5-lOOVK9sfY2UacA", "spend": 0.0001065,
          "expires": "2023-11-24T23:19:11.131000Z",
          "models": ["gpt-6-luna", "gpt-6.1-sol", "claude-sonnet-5"],
          "aliases": {"mistral-7b": "gpt-6-luna"}, "config": {}}}
```
(Old sample; current source also returns the fields in the table.)
Per-model usage example (users doc): `"model_max_budget_usage": {"gpt-6.1-sol": {"current_spend": 0.0002, "budget_limit": 0.0001, "time_period": "30d", "budget_reset_at": "2026-11-14T09:30:12.418093+00:00"}}`; `budget_reset_at` there since v1.107.0.

- **Freshness:** spend is batch-written to the DB; docs say with default `proxy_batch_write_at: 10`s a request's cost can take "about 20 seconds" to show in DB (users doc, per-model section). Budget enforcement uses a Redis counter, and `/key/info` per-model usage reads "the same value used to enforce" (users doc). `key.spend` itself is read from the DB/cache row. [INFERENCE] lag of ~10-20 s.
- **Rate limits for the endpoint:** not documented.
- **Cannot tell:** remaining balance (compute it), per-request history, provider-side spend, other keys of the same user (`/user/info` lists those).
- A key under a team: "only the team (and team-member) budgets are enforced; the key owner's personal budget does not apply" (users doc, top banner). Key's own `max_budget` still applies in addition [INFERENCE from `virtual_key_max_budget_check` running alongside team checks].

`POST /v2/key/info` / `GET /v2/key/info` with `{"keys": [...]}`: batch version, "Currently admin only" per its docstring, though the per-key `_can_user_query_key_info` filter lets non-admins query their own. Not needed by ADE.

## 3. User, team, customer

### `GET /user/info`
Src: `management_endpoints/internal_user_endpoints.py` (`user_info`, `_enforce_user_info_access`); docs https://docs.litellm.ai/docs/proxy/cost_tracking ("Total spend per user").
- Auth: normal key. `user_id` omitted = the key's own `user_id`. Another user's id gives `403 key not allowed to access this user's info` unless admin or admin-viewer. **Keys without `user_id` (service-account/team keys) have nothing to look up** (404 `User None not found` [INFERENCE from code path `user_id=None`]).
- Response: `{"user_id", "user_info": {spend, max_budget, budget_duration, budget_reset_at, models, tpm_limit, rpm_limit, model_max_budget, model_max_budget_usage, ...}, "keys": [ ...key rows incl. spend... ], "teams": [...]}`. Docs example shows `user_info: {"spend": 0.1}` and the full `keys` rows (shape in the docs listing includes `spend`, `max_budget`, `budget_duration`, `budget_reset_at`, `soft_budget_cooldown`, `model_spend`, `model_max_budget`).
- `GET /v2/user/info`: lightweight, user row only, "Internal users can only query themselves". Note on `spend`: "the user's running budget counter, which the budget reset job resets whenever `budget_reset_at` elapses ... NOT lifetime spend" (src docstring).
- Needs DB. Not enterprise.

### `GET /team/info?team_id=<id>`
Src: `management_endpoints/team_endpoints.py` (`team_info`, `validate_membership`); docs virtual_keys page ("Team Spend").
- Auth: normal key allowed if `key.team_id == team_id` (team keys), or the key's `user_id` is a member (src `validate_membership`); else `403`. `team_id` required (422 otherwise).
- Response: `team_info` (team row: `spend`, `max_budget`, `soft_budget`, `budget_duration`, `budget_reset_at`, `tpm_limit`, `rpm_limit`, `models`, `members_with_roles`, `model_max_budget_usage` per users doc, `caller_edit_access`), `keys`, `team_memberships` (member-level `spend`/budget). Docs "Sample Response" from `/team/new`: `"spend": 0.0, "max_budget": null, "budget_duration": null, "budget_reset_at": null`.
- Per-member budgets (`max_budget_in_team`) and "Current Cycle Spend" exist (users doc, "Team Members"); the exact JSON field names in `/team/info` for member spend are not documented here. Team-member budgets exist as `LiteLLM_TeamMembership` rows.

### `GET /customer/info?end_user_id=<id>` (alias `/end_user/info`)
Docs: https://docs.litellm.ai/docs/proxy/customers. Response: `{"user_id","blocked","alias","spend","allowed_model_region","default_model","litellm_budget_table"}`.
Source `types.py` lists `/customer/info` only in the admin-view-only route list ("handlers already gate on PROXY_ADMIN_VIEW_ONLY"), so a plain virtual key **cannot** call it. Irrelevant to ADE unless ADE sets `user`/`x-litellm-customer-id`. Customer budgets: `ExceededBudget: End User=... over budget` HTTP 401 per docs sample.

## 4. Spend logs and reports

### `GET /spend/logs` (deprecated) and `GET /spend/logs/v2` (preferred)
Src: `spend_tracking/spend_management_endpoints.py` (`view_spend_logs`); docs https://docs.litellm.ai/docs/proxy/cost_tracking.
- `/spend/logs` params: `api_key`, `user_id`, `request_id`, `start_date`, `end_date` (`YYYY-MM-DD`), `summarize` (default `true` = daily aggregates; `false` = individual rows). Capped at the 10,000 most recent rows; header `x-litellm-spend-logs-truncated: true` on truncation.
- `/spend/logs/v2` params: `start_date`, `end_date` (`YYYY-MM-DD HH:MM:SS`), `page` (from 1), `page_size` (max 1000), plus filters `api_key`, `user_id`, `team_id`, `model`, `status_filter`, `min_spend`, `max_spend`. Response `{"data": [...], "total", "page", "page_size", "total_pages", "total_is_capped"}`.
- Auth: both are in `spend_tracking_routes`, which are in the `internal_user` route list. For `internal_user` / `internal_user_viewer` roles the code **forces `user_id = caller's user_id`** (src lines ~3563). A key with no user_id: [INFERENCE] returns nothing useful. Team-wide logs require the team-member permission `/spend/logs` (access_control doc). Master key sees all.
- Row (docs "Spend Log Entry Format"): `api_key` (hash), `user`, `team_id`, `request_tags`, `end_user`, `model_group`, `api_base`, `spend` (USD), `total_tokens`, `completion_tokens`, `prompt_tokens`, `metadata{attempted_fallbacks, original_model_group}`; also `request_id`, `startTime`, `endTime`, `model` [INFERENCE from the table schema/`ui_view_spend_logs` filters].
- Retention is operator-configured (https://docs.litellm.ai/docs/proxy/spend_logs_deletion); rows may be deleted.
- Not enterprise. Needs DB and `store_prompts`-style settings are not required for spend rows.

### `GET /user/daily/activity` (+ `/aggregated`, `/export`)
Src: `management_endpoints/common_daily_activity.py`, `internal_user_endpoints.py`; docs cost_tracking "Daily Spend Breakdown API".
- Params: `start_date`, `end_date` (`YYYY-MM-DD`), `user_id` ("Admins can filter by any user or omit for global view. **Non-admins must provide their own user_id**", src), `model`, `api_key`, page/size params on non-aggregated variant [INFERENCE: not enumerated above]. Daily granularity only.
- Response: `{"results":[{"date","metrics":{spend,prompt_tokens,completion_tokens,total_tokens,api_requests},"breakdown":{"models":{...},"providers":{...},"api_keys":{...}}}],"metadata":{total_spend,total_prompt_tokens,total_completion_tokens,total_api_requests}}`. Trimmed docs example:
```json
{"results":[{"date":"2025-03-27","metrics":{"spend":0.0177072,"prompt_tokens":111,"completion_tokens":1711,"total_tokens":1822,"api_requests":11},
 "breakdown":{"models":{"gpt-6-luna":{"metrics":{"spend":0.0177072,"api_requests":11}}},"api_keys":{"3126b6eaf1...":{"metrics":{"spend":0.0177072},"metadata":{"key_alias":"my-key","key_exists":true}}}}}],
 "metadata":{"total_spend":0.0177072,"total_api_requests":11}}
```
- Unlike `spend` in `/user/info`, this is **not reset** at budget rollover ("daily spend records that only ever accumulate", src `user_info_v2` docstring). Best source for calendar-period totals.
- Related: `/team/daily/activity` (needs team-member permission), `/tag/daily/activity`, `/organization/daily/activity` (org admin), `/gateway/daily/activity`. Request counts are "derived from spend logs ... record each upstream attempt separately" (docs).
- No premium check found in the user-daily source. Not enterprise [INFERENCE: grep for `premium_user` in those files found nothing].

### Enterprise reports
- `GET /global/spend/report?start_date&end_date&group_by=team|customer|api_key&api_key=&internal_user_id=` : docs heading "(Enterprise) Generate Spend Reports"; source raises if `premium_user is not True` (src `spend_management_endpoints.py`, line ~1609). Admin only in practice; a non-admin key needs `permissions={"get_spend_routes": true}`, itself enterprise (docs "Allowing Non-Proxy Admins to access /spend endpoints"). Response by `group_by=team`: `[{"group_by_day","teams":[{"team_name","total_spend","metadata":[{"model","spend","total_tokens","api_key"}]}]}]`; by api_key/internal_user: `[{"api_key","total_cost","total_input_tokens","total_output_tokens","model_details":[...]}]`.
- `/key/spend/report`, `/user/spend/report`, `/team/spend/report`, `/organization/spend/report`: the shared prereq `_spend_report_prereqs` returns **403 not premium** without an enterprise licence (src).
- `/global/spend/*` family and `/spend/keys`, `/spend/users`: `/spend/keys` and `/spend/users` are scoped to the caller's own rows for non-admins (docs cost_tracking "Spend list endpoints"; empty list `[]` for keys without `user_id`; 403 for another user's id). `/global/spend/*` are not in `internal_user_routes` (types.py comment: "non-admin roles must not see other tenants' spend").
- `/global/spend/reset`: master key only; resets all key/team spend.
- `/cost/estimate` is in `spend_tracking_routes`; I did not find docs for it. Not documented here.

## 5. Budgets: semantics and failure behaviour

Sources: https://docs.litellm.ai/docs/proxy/users, https://docs.litellm.ai/docs/proxy/alerting, src `auth/auth_checks.py`, `litellm/__init__.py`, `litellm/exceptions.py`.

- **Scopes:** global proxy `litellm_settings.max_budget`; organization; team; team member (`max_budget_in_team`); key (`max_budget`); user (internal user; only for keys without `team_id`); end-user/customer; per-model budget on key/user/team; tag budgets; concurrent windows on a key via `budget_limits`.
- **Needs a database.** Without one, budgets "fail open" and cap nothing (users doc warning).
- **`budget_duration`:** string `Ns/Nm/Nh/Nd` (also `1mo` for model budgets). Unset = never resets. At reset `spend` returns to 0 (or the overage if `budget_rollover` is enabled, src). Resets are checked by a scheduler every ~10 min by default (`proxy_budget_rescheduler_min_time/max_time`), so reset can lag `budget_reset_at`. Multi-window `budget_limits` reset at fixed boundaries: `1h` hourly, `24h` midnight UTC, `7d` Monday, `30d` 1st of month (docs).
- **Hard budget:** `max_budget`. Check is `spend >= max_budget` before the request (src `virtual_key_max_budget_check`). A **reservation** mechanism (default on) estimates max cost from `max_tokens` x price and may reject a request *before* the budget is actually reached; reconciled after (docs "Budget reservation").
- **Soft budget:** `soft_budget`: when `spend >= soft_budget` LiteLLM fires a `budget_alerts` event (Slack/webhook/email); it **never blocks** (src `virtual_key_soft_budget_check`: "Triggers a budget alert"; docs alerting "Soft Budget Alerts"). Must stay below `max_budget` (access_control doc). Nothing is surfaced in the response headers.
- **Free models:** budget checks are skipped for models with `input_cost_per_token: 0` and `output_cost_per_token: 0` set explicitly (users doc).
- **Exceeded: HTTP status.** `BudgetExceededError.status_code = litellm.budget_exceeded_status_code`, which on `main` defaults to **422** ("set to 429 to restore the pre-422 budget_exceeded response code", src `__init__.py`). Docs examples show older codes: key budget `{"detail":"Authentication Error, ExceededTokenBudget: Current spend for token: 7.2e-05; Max Budget for Token: 2e-07"}` (old), per-model `"type":"budget_exceeded","code":"400"`, user per-model `"code":"429"`, internal-user default `"type":"budget_exceeded","code":"400"`, end-user `"type":"auth_error","code":401`. **ADE must not depend on the status code**: classify as budget-exceeded if the body `error.type == "budget_exceeded"` OR the message contains `Budget has been exceeded` / `ExceededBudget` / `ExceededTokenBudget`, any of 400/401/422/429.
- **Message shapes (src):** key `Budget has been exceeded! Key=<alias> (<masked name>) Current cost: <spend>, Max budget: <max>`; team `Budget has been exceeded! Team=<id> Current cost: ..., Max budget: ...`; user `ExceededBudget: User=<id> over budget. Spend=..., Budget=...`; end user `ExceededBudget: End User=<id> over budget. Spend=..., Budget=...`; per-model `LiteLLM Virtual Key: <hash>, key_alias: None, exceeded budget for model=<m>` (docs). Error envelope is OpenAI-style `{"error":{"message","type","param","code"}}`; `code` is a string.
- **Fail-closed:** with `general_settings.fail_closed_budget_enforcement: true`, an unverifiable budget gives `503` (docs).
- Budget fallbacks: an operator can route over-budget keys to another model instead of erroring (https://docs.litellm.ai/docs/proxy/budget_fallbacks); ADE would see success with a different `x-litellm-model-group`.
- **Team vs user:** key in a team = team/team-member budgets only, key owner's personal budget ignored (v1.95.0+; users doc).
- **Model-specific budgets (`model_max_budget`) are Enterprise** (users doc, "LiteLLM Enterprise feature"); per-key/team/user `max_budget`, `budget_duration`, `soft_budget`, `budget_limits` are not marked enterprise. Team-admin role and org admin are Premium.

## 6. Prices for pre-flight estimates

### `GET /v1/models` (or `/models`)
OpenAI-shaped list: `{"data":[{"id","object":"model","created","owned_by"}],"object":"list"}` (docs https://docs.litellm.ai/docs/proxy/model_discovery). Lists only models the key may use. **No pricing.** Operators can hide models with `model_info.discoverable: false`.

### `GET /v1/model/info` (also `/model/info`) and `GET /model_group/info`
Src: `proxy_server.py` (`model_info_v1`, `model_group_info`); routes are in `model_info_routes`/`info_routes`, callable by any valid key (`non_proxy_admin_allowed_routes_check`: "/model/info just shows models user has access to"). Docs: model_management says `/model/info` "Returns the full model list with API keys masked" and that mapped cost data appears alongside `model_info`.
- `/v1/model/info` params: `litellm_model_id` (single deployment id = `x-litellm-model-id` header), `include_team_models`, `teamId`, `healthy_only`.
- Response: `{"data":[{"model_name","litellm_params":{"model",...},"model_info":{"id","db_model", "input_cost_per_token","output_cost_per_token", "max_input_tokens","max_output_tokens","mode", ...}}]}`. The pricing keys inside `model_info` are from the LiteLLM cost map or the operator's override [INFERENCE: docs only show `id`,`db_model` in the example and say mapped cost/context data is included; key names `input_cost_per_token`/`output_cost_per_token` are the documented override names in users doc "Fallback to 'free' models"]. Cache and reasoning prices (`cache_read_input_token_cost`, ...) may exist: not documented in the pages read.
- `/model_group/info` is the better call: typed `ModelGroupInfo` per model group with `model_group`, `providers`, `max_input_tokens`, `max_output_tokens`, **`input_cost_per_token`**, **`output_cost_per_token`**, `input_cost_per_pixel`, `mode`, `tpm`, `rpm`, `deployment_pricing[]`, `supports_*` flags (src `litellm/types/router.py`). The docstring says end users should use it. Prices are per single token in USD.
- Caveats: a group can span several deployments with different prices (see `deployment_pricing`); `null` when LiteLLM has no price (spend then logs as 0).

## 7. Enterprise vs open source

| Feature | Tier | Source |
|---|---|---|
| Virtual keys, `max_budget`, `budget_duration`, `soft_budget` (alerts), team/user/customer budgets, `/key/info`, `/user/info`, `/team/info`, `/spend/logs`, `/spend/logs/v2`, `/spend/keys`, `/user/daily/activity`, cost headers, `/model/info`, `/model_group/info`, `/v1/models` | Open source (all need a Postgres DB for key/budget features) | users doc; virtual_keys doc; source shows no premium check |
| `/global/spend/report`, `/key|user|team|organization/spend/report` | Enterprise (licence) | cost_tracking doc heading "(Enterprise)"; `premium_user` check in src |
| `permissions: {"get_spend_routes": true}` on a key | Enterprise | cost_tracking doc |
| Per-model budgets (`model_max_budget`) on key/user/team | Enterprise | users doc |
| Tag spend (`metadata.tags`), team-admin / org-admin roles, team-member permissions | Enterprise / Premium | cost_tracking doc; access_control doc |
| Custom UI/SSO etc. | n/a | not relevant |

## ADE driver recommendation

Lowest-privilege design, plain virtual key:

1. **Per request (inline, free):** read `x-litellm-response-cost` and `x-litellm-key-spend` / `x-litellm-key-max-budget` from every non-streaming response. Accumulate local cost; treat `x-litellm-key-spend` as the freshest "key total", but it is cached-spend + this call, not authoritative. For streams, compute cost from the final `usage` chunk x prices (below) because headers are committed before the stream ends.
2. **Poll `GET /key/info` (no params, own key) every 30-60 s while a session is active, and once at startup and after any budget-exceeded error.** This is the authoritative `spend`, `max_budget`, `budget_duration`, `budget_reset_at`, `soft_budget`, `model_max_budget_usage` and limits. Remaining = `max_budget - spend` (null cap = unlimited). Expect ~10-20 s DB write lag, so merge with locally accumulated cost since the last poll (`local_delta` = costs from responses after the poll timestamp) to show a live number.
3. **If `/key/info` shows `max_budget == null` and the key has a `team_id`:** call `GET /team/info?team_id=` (every ~60 s) for the team cap/spend; if no `team_id` but has `user_id`, call `GET /user/info` for the user cap. The binding cap is whichever of key/team/user is enforced (team replaces user when the key is in a team). If both `/key/info` and these return no cap, show "no cap configured" and rely on local totals.
4. **History/daily view (rare, on demand):** `GET /user/daily/activity?user_id=<own>&start_date&end_date` (non-reset, daily buckets, per model). Request-level audit: `GET /spend/logs/v2?...&page_size=100`. Both are scoped to the caller; do not poll.
5. **Pre-flight estimate:** fetch `GET /model_group/info` once at startup and on model-list refresh (every ~10 min); estimate cost = `prompt_tokens*input_cost_per_token + max_output_tokens*output_cost_per_token`. Refuse a call locally if estimate > `max_budget - spend`; the proxy's own reservation (`max_tokens` x price) may reject earlier, so send `max_tokens`.
6. **Compute locally:** remaining budget, burn rate, time-to-reset (`budget_reset_at`), local per-model cost from `usage`, soft-limit warnings (`spend >= soft_budget` from `/key/info`; no header signals it), pre-flight guard.
7. **Error handling:** treat any 4xx whose body type is `budget_exceeded` or message matches `Budget has been exceeded`/`ExceededBudget` as a budget stop regardless of status code (422 on current `main`, 400/401/429 in older versions). Parse `Current cost`/`Max budget` from the message as a fallback, and then re-poll `/key/info` to see which scope tripped.
8. **Do not depend on:** `/customer/info`, `/global/spend/*`, `/spend/report`, model budgets (enterprise/admin only). Degrade gracefully on `403`/`404`/`5xx` (DB-less deployments return errors on `/key/info`) and fall back to purely local accounting using prices from `/model_group/info` (which may be `null`: then show cost as unknown).
