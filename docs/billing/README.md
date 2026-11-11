# AI billing sources

ADE reads spend and budget from each provider's own billing endpoint where one exists, and meters per-request cost locally everywhere. These reference files were compiled from the providers' docs on 2026-11-11. The LiteLLM file also draws on source code, where its docs are silent. Every claim links its source, and `[INFERENCE]` marks anything unconfirmed.

| File | Covers |
|---|---|
| [litellm.md](litellm.md) | per-request cost headers, `/key/info`, user and team info, spend logs and reports, budget semantics and errors, model prices, enterprise vs OSS |
| [vendors.md](vendors.md) | Anthropic admin usage/cost, Claude Pro/Max subscription limits, OpenAI usage/costs, Moonshot/Kimi balance, OpenRouter key/credits |

## What a normal (non-admin) key can see

| Source | Remaining budget | Spend | Notes |
|---|---|---|---|
| LiteLLM virtual key | `max_budget − spend` from `/key/info` | per-request headers + `/key/info` | Team keys are checked against team budgets only. Budgets need the proxy's DB; without one they fail open. `soft_budget` alerts but never blocks. |
| Moonshot/Kimi | `available_balance` (USD) | balance delta | No usage/cost endpoint. |
| OpenRouter | `limit_remaining` from `/api/v1/key` | `usage` | `/credits` needs a management key. |
| Anthropic API | — | — | Needs an Admin key (`/v1/organizations/analytics/cost`, 4–24 h lag). At the platform spend cap, the API returns 429 `enforced_spend_limit_reached`. |
| OpenAI API | — | — | Needs an Admin key (`/v1/organization/costs`, daily buckets). No balance endpoint. |
| Claude subscription (omp / Claude Code) | — | omp per-turn `usage.cost.total` (list-price equivalent) | Claude Code's statusline exposes 5-hour and 7-day `used_percentage`. The OAuth usage endpoint is undocumented, so ADE does not call it. |

## Rules ADE follows

1. **Per-request metering always.** Cost comes from response headers (LiteLLM), driver-reported cost (omp), or usage × price table. Each request becomes an `ai` audit event.
2. **Authoritative snapshot when available.** Poll at the cadence each file recommends: LiteLLM `/key/info` every 60 s, Moonshot balance every 5 min, admin reports daily.
3. **Displayed spend = snapshot + local delta since the snapshot's `as_of`.**
4. **Provider stops are the backstop.** ADE detects them by response body, not status code (LiteLLM's status differs between versions), and records them as `budget.stop` events.
5. **Pre-flight estimates** use provider prices: LiteLLM `/model_group/info`, otherwise the profile's price table.

## Local testing

`nix develop .#gateway` provides `litellm` and `postgresql`. A local proxy with a DB-backed virtual key and a small `max_budget` exercises the cost headers, `/key/info`, and the budget-exceeded path, without touching any real gateway.

`scripts/gateway/gateway.sh up [STATE_DIR] [MAX_BUDGET_USD]` starts that proxy (Postgres, a master key, a mock model so nothing is billed, and a virtual key with the given `max_budget`) and writes `STATE_DIR/env` with `ADE_GATEWAY_URL` and `ADE_GATEWAY_KEY` for an `openai-compat` profile with `billing = { source = "litellm" }`; `down STATE_DIR` stops it. Observed against LiteLLM 1.108: an over-budget key gets HTTP 422 with `error.type = "budget_exceeded"` and `Budget has been exceeded! Key=… Current cost: …, Max budget: …`. `scripts/verify/phase4.sh` drives it end to end.
