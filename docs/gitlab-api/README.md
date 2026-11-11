# GitLab API contract

ADE's GitLab driver is built from the official docs, not from a paid test instance. The three reference files below were compiled from docs.gitlab.com (which describes 19.x) on 2026-11-11. Every claim in them links its source page. `[INFERENCE]` marks anything the docs did not state outright, and each of those needs proof from a real instance before code relies on it.

| File | Covers |
|---|---|
| [planning.md](planning.md) | issues (list/filter/create/edit), iterations + cadences, milestones, labels and scoped labels, boards, epics, GraphQL issue queries |
| [workflow.md](workflow.md) | issue links, work-item hierarchy (GraphQL), issue ↔ MR, branches, MRs, discussions and draft notes, approvals, diffs, pipelines and jobs |
| [platform.md](platform.md) | auth and scopes, `/user`, events, pagination, rate limits, caching, GraphQL limits, tier detection |

## Decisions these docs force

### Tracker → child issue

- **No hierarchy link.** The docs only allow issue → task as an issue's child, and tasks must sit in the same project as their parent issue. Issue → issue parenting is not supported. Epics can hold cross-project issues, but they are Premium and their REST API is deprecated. ADE therefore never relies on hierarchy for the tracker → child link. It only *reads* a tracker issue's parent epic (GraphQL hierarchy widget, capability-gated) to display it.
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
| Label sprints (`Sprint::42`) | plain labels, not exclusive (verified: adding `workflow::in progress` to an issue already carrying `workflow::ready` keeps both), so ADE resolves multiple sprint labels on one issue to the newest | scoped (exclusive) |

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
- **Rate limits:** GitLab.com documents 2,000 req/min per user, but individual endpoints are limited much lower: re‑seeding the sandbox hit a `429` on `GET /groups/:id/labels` after a burst of about 150 requests, well under that figure. On self‑managed the limit is off by default. ADE honours `RateLimit-*` headers when present and `Retry-After` on 429 (the seed script does too), and runs at most 4 concurrent requests per host.
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

## Verified on a Free gitlab.com group (2026-11-11)

The sandbox, seeded by `scripts/sandbox/gitlab-seed.sh`, answered these questions directly:

| Question | Result |
|---|---|
| `GET /groups/:id/iterations` on Free | `403` |
| `POST …/issues/:iid/links` with `link_type=blocks` on Free | `403`; nothing is created |
| `::` labels on Free | non-exclusive; both `workflow::*` labels stay on the issue |
| Cross-project `relates_to` link on Free | works; the list shows `references.full` = `group/api#1`, `link_type` = `relates_to` |
| MR discussions on Free | resolvable (`notes[0].resolved` true/false) |
| `task_completion_status` | populated from Markdown checklists (`1/4`) |
| `GET /projects/:id/approval_rules` on Free | `200 []`, so status can't detect the feature; `merge_request_approvers_available: false` on an MR's `/approvals` response does |
| `approvals_required` on Free | `0`, which would read as "none required"; ADE reports "unknown" (`-1`) when approvers are unavailable |
| `group.epics` (GraphQL) on Free | deprecated; returns `{nodes: []}`, not null. ADE probes `workItemTypes(name: EPIC)` (empty on Free) plus `iterationCadences` (null on Free) |
| Issue `weight` / `iteration` on Free | absent from REST responses |
| Milestone `state` for a past milestone | stays `active` until someone closes it; ADE derives sprints from dates, never from state |
| Unknown `iteration_id` filter on Free | `[]` |
| `/api/graphql` auth | accepts the `PRIVATE-TOKEN` header |

So on GitLab, a 403 means "gated feature unavailable" for most capability probes; approval rules and epics need the field-level probes above.

Phase 2 write paths (MR page, review batching), also on the Free sandbox:

| Question | Result |
|---|---|
| Nested JSON bodies for writes | accepted: `position` as an object on `POST …/discussions` and `…/draft_notes`, `resolved` on `PUT …/discussions/:id`, `title` on `PUT …/merge_requests/:iid`, `sha` on `…/approve` and `…/merge` |
| Draft notes and `bulk_publish` on Free | work; after one `POST …/draft_notes/bulk_publish` the draft list is empty and each draft is a discussion, created within ~0.4s of each other (one request, one notification) |
| Position on a renamed file | `old_path` = old name, `new_path` = new name; an added line anchors with `new_line` alone (`old_line: null`); an unchanged line needs both numbers, which differ after an insertion (old 7 / new 8) |
| `GET …/diffs` for a rename with edits | `renamed_file: true`, `old_path`/`new_path` differ, `diff` holds the hunks without a `---`/`+++` preamble |
| Draft toggle | no REST draft parameter: ADE adds or strips the `Draft: ` title prefix with `PUT` (verified both ways) |
| Merge right after MR creation | `405` until GitLab has computed mergeability; a retry a few seconds later succeeds |
| Retry | the sandbox has no CI, so MRs have no pipeline: `ade mr retry` reports "no pipeline" and skips (audited as skipped); retry stays covered by the fake |

Phase 3 write paths (Start work), also on the Free sandbox:

| Question | Result |
|---|---|
| `POST …/issues` with `assignee_ids`, `milestone_id`, `labels` | accepted; a group milestone id from the tracker works in the child's project |
| `PUT …/issues/:iid` with `add_labels` + `remove_labels` | one update adds and removes, which is how ADE moves a workflow label (Free keeps every `::` label, so the old one is removed explicitly) |
| Linking an already linked pair (`POST …/links`) | `409`; ADE treats it as done |
| `POST …/repository/branches` for an existing name | `400 Branch already exists` (ADE maps it to "exists") |
| `GET …/repository/branches?search=^412-` | prefix search; ADE also filters client-side |
| `POST …/merge_requests` | no draft parameter: a `Draft: ` title prefix makes it a draft; a second open MR for the same source branch answers `409` |
| Quick actions in the MR description (`/label ~"type::story"`) | run on create and are removed from the stored description |
| `GET …/repository/files/:path/raw?ref=` for a template | works on a private project with the token header; a missing file is `404` |
| GraphQL `issueSetIteration` on Free | answers `{errors: [], issue: {iid}}` and does nothing (no error, no iteration), so ADE only calls it when the tracker issue carries an iteration |

## Open questions (need a paid instance)

- the integer meaning of iteration `state`
- status codes and shapes of other Premium endpoints when they *are* licensed
- the direction of the link in issue-links list responses when the caller is on the target side

`ade doctor --probe` reports each of these when it is run against a licensed instance.
