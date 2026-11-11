# GitLab API contract

ADE's GitLab driver is built from the official docs, not from a paid test instance. The three reference files below were compiled from docs.gitlab.com (which describes 19.x) on 2026-11-11. Every claim in them links its source page. `[INFERENCE]` marks anything the docs did not state outright, and each of those needs proof from a real instance before code relies on it.

| File | Covers |
|---|---|
| [planning.md](planning.md) | issues (list/filter/create/edit), iterations + cadences, milestones, labels and scoped labels, boards, epics, GraphQL issue queries |
| [workflow.md](workflow.md) | issue links, work-item hierarchy (GraphQL), issue ↔ MR, branches, MRs, discussions and draft notes, approvals, diffs, pipelines and jobs |
| [platform.md](platform.md) | auth and scopes, `/user`, events, pagination, rate limits, caching, GraphQL limits, tier detection |

## Decisions these docs force

### Tracker → child issue

- **No hierarchy link.** The docs only allow issue → task as an issue's child, and tasks must sit in the same project as their parent issue. Issue → issue parenting is not supported. Epics can hold cross-project issues, but they are Premium and their REST API is deprecated. ADE therefore never relies on hierarchy or epics.
- **Link convention, Free tier:** `POST /projects/:id/issues/:iid/links` with `link_type=relates_to`, `target_project_id`, and `target_issue_iid` (cross-project works), **plus** a `Parent: group/tracker#N` line in the child's description.
- **Reading back** accepts: the link, the description reference, MR closing patterns, and the work-item hierarchy where one happens to exist.
- `blocks` / `is_blocked_by` links are Premium, so ADE only *reads* them, for blocker detection.

### Branch and MR

- **Branch:** there is no API for "create branch from issue". ADE calls `POST /projects/:id/repository/branches` with an `<iid>-<slug>` name, and GitLab links that prefix to the issue within the same project.
- **MR:**
  - **Draft:** REST has no `draft` param, so a draft is created with a `Draft: ` title prefix. "Mark ready" removes the prefix, or uses GraphQL `mergeRequestSetDraft`.
  - **Description:** carries `Closes #<child iid>` and mentions the parent.
- **Merge** uses `auto_merge` (`merge_when_pipeline_succeeds` is deprecated) and always sends `sha`, which some instances require.
- **Diffs:** use `/diffs`, not the deprecated `/changes`.

### Approvals

- `approve`, `unapprove`, and `GET …/approvals` are Free.
- `approval_state` and approval rules are Premium. Without them, the UI shows "approved by N" instead of "N of M required".

### Iterations

| Need | Free | Premium/Ultimate |
|---|---|---|
| Read iterations | n/a | REST `GET /groups/:id/iterations?state=current\|upcoming\|closed` |
| Filter issues by iteration | n/a | REST `iteration_id` (`None`/`Any` only); resolve "current" through the iterations endpoint first |
| Assign iteration | n/a | GraphQL only (`createIssue.iterationId`, `issueSetIteration`) |
| Cadence (length, start) | n/a | GraphQL only (`Group.iterationCadences`) |
| Milestones as sprints | REST/GraphQL; ADE computes "current" from `start_date`/`due_date` itself, because REST and GraphQL `Started` semantics differ | same |
| Label sprints (`Sprint::42`) | plain labels, not exclusive [INFERENCE], so ADE resolves multiple sprint labels on one issue to the newest | scoped (exclusive) |

- The REST iteration `state` field is an undocumented integer. ADE never decodes it; it queries by the `state` filter instead.
- `iterations.source = "auto"` probes in this order: GitLab iterations → milestones with dates → labels + configured cadence.

### Activity for standups

- `/events` and `/users/:id/events` **do not return MR events**. ADE combines:
  - user events (pushes, comments, issue transitions)
  - MR lists filtered by `updated_after`, author, and reviewer
  - project events for the configured repos
- `action_name` strings differ from the filter values, so parsing is defensive.
- Events older than 3 years are purged. This is irrelevant for standups.

### Platform

- **Token scopes:**
  - read-only: `read_api` + `read_user` (events need `read_user` or `api`)
  - writes: `api`
  - `ade doctor` checks scopes via `GET /personal_access_tokens/self` for personal tokens. For other token types it falls back to probing.
- **Pagination:** use keyset where supported (project issues ≥ 18.3), offset elsewhere, and never depend on `X-Total` (it can be omitted).
- **Rate limits:** GitLab.com allows 2,000 req/min per user. On self-managed the limit is off by default. ADE honours `RateLimit-*` headers when present and `Retry-After` on 429, and runs at most 4 concurrent requests per host.
- **Caching:** ETag is not documented for `/api/v4`. Incremental sync uses `updated_after` / `after` and local cache rows.
- **GraphQL limits:** complexity 250, query size 10,000 chars, 30 s timeout, 100 per page. ADE keeps queries small and paginates connections.

### Capabilities

`GET /metadata.plan` only exists from 19.6, and it is `null` on GitLab.com. ADE does not trust tier names. It probes each capability once per forge and caches the result (TTL 24 h):

- a REST 403/404, or a GraphQL `null` on a gated field, means "unavailable"
- every feature reads the capability, never the tier

`ade doctor` prints the capability matrix.

## Testing without a paid instance

1. **Doc-derived fixtures.** Fixtures are hand-authored from the documented examples in `internal/forge/gitlab/testdata/{free,premium}/`. `testdata/SOURCES.md` maps every fixture to its doc URL. No fixture is ever captured from a real instance.
2. **Fake GitLab with a tier switch.** An `httptest` server implements the subset ADE uses. In `free` mode it returns 403/404 and `null` for gated endpoints and fields, so both sides of every capability branch are tested.
3. **Schema-checked GraphQL.** `ade dev schema-pull` fetches GitLab.com's public GraphQL schema by introspection. GitLab.com runs EE, so the schema includes Premium types. The schema is committed. Tests validate every ADE query and mutation against it with `gqlparser`, catching typos and removed fields with no instance at all.
4. **Free sandbox (optional).** A GitLab.com Free group with synthetic data. `go test -tags live` runs against it when `ADE_LIVE_GITLAB_*` env vars are set; this is never wired to CI secrets.
5. **Read-only shape probe on real instances.** `ade doctor --probe` calls the read-only endpoints ADE depends on, including Premium ones, and reports only *shape* results: missing or renamed fields, unexpected types, status codes. It never reports values or writes anything. This is how paid-tier paths get confirmed against a real instance without a paid test instance and without moving data off the machine.

## Open questions (need a real instance)

From the reference files:

- the integer meaning of iteration `state`
- what Free returns for `blocks` links
- the direction of the link in issue-links list responses
- status codes for some gated endpoints
- whether `::` labels behave as plain labels on Free

`ade doctor --probe` reports each of these.
