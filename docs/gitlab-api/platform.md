# GitLab platform-level API contract (auth, users, events, pagination, limits, GraphQL, tier detection)

Research date: 2026-11-11. Docs read are the current docs.gitlab.com (they reference GitLab 19.x features, so "current" ≈ 19.6). Base REST URL: `https://<host>/api/v4`. [^base]
Convention: "not documented" = docs did not say. `[INFERENCE]` = my reasoning, not read.

[^base]: https://docs.gitlab.com/api/rest/ ("path must start with `/api/v4`")

---

## 1. Authentication

### 1.1 Header styles
Source: https://docs.gitlab.com/api/rest/authentication/

- Personal / project / group access tokens: `PRIVATE-TOKEN: <token>` header ("recommended"). Also accepted: `Authorization: Bearer <token>` ("OAuth-compliant headers"), and `private_token` query param (documented for GraphQL URL and for impersonation tokens).
- OAuth 2.0 tokens: `Authorization: Bearer <token>` or `access_token` param, **not both** (both → `400 Bad Request`). OAuth tokens valid 2 hours; refresh via `refresh_token`.
- Job token: `JOB-TOKEN` header (specific endpoints only). Deploy tokens cannot be used with the public API.
- Session cookie `_gitlab_session` is accepted (web frontend); generating one via API unsupported.
- Sudo / impersonation tokens: admin only; irrelevant to ADE.
- Project access tokens: Free+Premium+Ultimate on self-managed; **Premium/Ultimate only on GitLab.com**.
- Errors:
  - Missing/invalid token → `401` `{"message":"401 Unauthorized"}`.
  - Valid token but missing scope → `403` `insufficient_scope`; body and `WWW-Authenticate: Bearer realm="Protected by OAuth 2.0", error="insufficient_scope", ..., scope="api read_api"` list accepted scopes (token needs ONE of them). Example: `GET /namespaces` with only `read_user` returns that header.
  - Docs note some endpoints return 401 even for authenticated-but-unauthorized users (e.g. listing another user's PATs). https://docs.gitlab.com/api/rest/troubleshooting/#status-code-401
- Troubleshooting recipe (documented): re-issue `GET /user` with same token: 200 = token valid; 401 = token invalid; 403 = valid but cannot read user (e.g. fine-grained PAT). Not usable for CI job tokens. https://docs.gitlab.com/api/rest/troubleshooting/

### 1.2 Scopes (Tier: Free/Premium/Ultimate; GitLab.com, Self-Managed, Dedicated)
Source: https://docs.gitlab.com/security/tokens/access_token_scopes/

| Scope | Token types | Meaning (docs) |
|---|---|---|
| `api` | personal, group, project | Complete read **and write** access to the API for the token's scope (incl. registries). |
| `read_api` | personal, group, project | Read access to the API for the token's scope. |
| `read_user` | **personal only** | Read-only access to authenticated user's profile via `/user`, "also grants access to read-only API endpoints under `/users`". |
| `read_repository` | personal, group, project | Read (pull) access to repositories via Git-over-HTTP **or the repository files API**. |
| `write_repository` | personal, group, project | Pull+push via Git-over-HTTP. "Does not support API authentication." |
| `self_rotate` | personal, group, project | Rotate this token only (since 17.9). |
| others | | `read_registry`, `write_registry`, `read_virtual_registry`, `write_virtual_registry`, `create_runner`, `manage_runner`, `ai_features`, `k8s_proxy`, `admin_mode`, `read_service_ping`, `sudo`. |

A personal access token reaches all groups/projects the user can; group token = its group's subgroups/projects; project token = only its project.
Fine-grained PATs also exist (https://docs.gitlab.com/auth/tokens/fine_grained_access_tokens/ — linked, **not read**); PAT API shows a `granular_scopes` response attribute (introduced 19.2) with `access` values `user`, `instance`, `all_memberships`, `personal_projects`, `selected_memberships` and `permissions` like `["read_job"]`.

**Which scope ADE needs**
- Read-only ADE (issues, MRs, pipelines, events, read comments): `read_api` (docs: "most endpoints accept `read_api` only for GET and HEAD requests"). Add `read_user` only if you want `/user`+`/events` with minimal scope; `/events` and `/users/:id/events` accept `read_user` **or** `api` (see §3) — note: the Events API prereq lists `read_user` or `api`, NOT `read_api`. [INFERENCE] a `read_api`-only token may therefore get 403 on `/events` and `/users/:id/events`; test with a real token (Events docs list only `read_user` or `api`). `GET /user` works with `read_api`/`api`/`read_user` [INFERENCE: docs for `/user` state read_user grants it; the generic rule says read_api works for GETs].
- Write ADE (create/comment/merge/transition): `api` (GraphQL docs: mutations require `api`).
- `read_repository` only matters for file contents via repository files API/git; `api`/`read_api` already cover REST repo endpoints for the token scope [INFERENCE — not stated explicitly].
- Recommended minimal read-only set: `read_api` + `read_user`. Write set: `api` (superset, also covers `read_user`-type reads? **not documented** — keep `read_user` too).

### 1.3 Token self-introspection
Source: https://docs.gitlab.com/api/personal_access_tokens/ (Tier: Free/Premium/Ultimate; all offerings)

```
GET /personal_access_tokens/self
```
- "return details on the personal access token you used to authenticate the request" (use keyword `self` for `:id`). `GET /personal_access_tokens/:id` also accepts `self`.
- Response (same attributes as list), from the docs example:
```json
{
  "id": 4, "name": "Test Token", "revoked": false,
  "created_at": "2020-07-23T14:31:47.729Z", "description": "Test Token description",
  "scopes": ["api"], "user_id": 3,
  "last_used_at": "2021-10-06T17:58:37.550Z", "last_used_ips": ["192.0.2.10"],
  "active": true, "expires_at": null,
  "granular_scopes": [{"access":"selected_memberships","permissions":["read_job"],"project_id":6,"group_id":null}]
}
```
- `granular_scopes` present only for tokens created with granular scopes; `granular_scopes` on `/self` introduced 19.5 (list/retrieve 19.2); `last_used_ips` (up to 5) listed in rotate history as 19.4. `expires_at` is date `YYYY-MM-DD` or null.
- Errors documented: `401` if token does not exist / no access; `404` only for admins when token doesn't exist.
- **Gotchas**: documented only for *personal* access tokens (rotate says `405` "if the token is not a personal access token"). Behavior of `/personal_access_tokens/self` with project/group access tokens or OAuth tokens: **not documented** [INFERENCE: use `GET /user` fallback; for project/group tokens docs have separate APIs (project/group access token API) not read].
- Fallback verification: `GET /user` (§2) and the 403 `insufficient_scope` response body.
- Rotation: `POST /personal_access_tokens/self/rotate` needs `api` or `self_rotate`; new token expires in 1 week by default. Default expiry for new PATs: 365 days if unspecified (400 days max with flag).
- PAT usage time is updated every 10 min, IPs every minute.

---

## 2. Users: `GET /user`
Source: https://docs.gitlab.com/api/users/#retrieve-the-current-user — Tier Free/Premium/Ultimate, all offerings. Scope: `read_user`, `read_api`, or `api` (see §1.2).

Response (regular user) — fields copied from docs example:
`id` int, `username`, `email`, `name`, `state` ("active"), `locked` bool, `avatar_url`, `web_url`, `created_at`, `bio`, `location` (nullable), `public_email`, `linkedin`, `twitter`, `discord`, `github`, `website_url`, `organization`, `job_title`, `pronouns`, `bot` bool, `work_information` (null), `followers` int, `following` int, `local_time`, `last_sign_in_at`, `confirmed_at`, `theme_id`, `last_activity_on` (date), `color_scheme_id`, `projects_limit`, `current_sign_in_at`, `identities` [{provider, extern_uid}], `can_create_group`, `can_create_project`, `two_factor_enabled`, `external`, `private_profile`, `commit_email`, `preferred_language`.
- Premium/Ultimate additionally: `shared_runners_minutes_limit`, `extra_shared_runners_minutes_limit`.
- Admin response adds `is_admin` (true/false) etc.; self-managed/Dedicated only. `is_admin` is therefore an admin-only-ish signal [INFERENCE]. Types of each field beyond the example JSON: not documented.
- Do not depend on all fields: docs example is the admin-superset; field types not tabulated.
- Public `GET /users` list (regular user) returns only `id, username, name, state, locked, avatar_url, web_url`; supports keyset pagination; `GET /users/:id` is rate-limited on GitLab.com at 300 req/10 min.
- GitLab.com: `GET /user` rate limit separate? not documented.

Also useful: `GET /user/status` ("Retrieve your user status", rate limit 240/min on .com for `/users/:user_id/status`). Not read in detail.

GraphQL equivalent: `query { currentUser { ... } }` (needs `read_api`). https://docs.gitlab.com/api/graphql/

---

## 3. Events API
Source: https://docs.gitlab.com/api/events/ — Tier: Free, Premium, Ultimate; GitLab.com, Self-Managed, Dedicated. `epics` target type introduced 17.3.

### 3.1 Endpoints
| Endpoint | Scope | Notes |
|---|---|---|
| `GET /events` | `read_user` or `api` | Events for **the authenticated user**. Has extra `scope` param ("Include all events across a user's projects", example `scope=all`). |
| `GET /users/:id/events` | `read_user` or `api` | `:id` = ID **or username** of a user. Has `page`, `per_page` (default 20). |
| `GET /projects/:project_id/events` | not documented (no prerequisite listed) | "all visible events for a project"; `project_id` int or URL-encoded path. |

Docs caveat (both user endpoints): "Does not return events associated with epics or merge requests. Returns bulk push events with limited commit details." [GOTCHA — merge request events are NOT returned by `/events` and `/users/:id/events` per docs; the project endpoint has no such statement.] Also: some epic features not returned; some MR notes may use `DiscussionNote` type, "not supported by the API".

### 3.2 Parameters
| Param | Type | Notes |
|---|---|---|
| `action` | string | "action type" per contributions calendar table (see 3.3). Exact string forms (`created` vs `opened`) — see gotcha below. |
| `target_type` | string | `design`, `issue`, `merge_request`, `milestone`, `note`, `project`, `snippet`, `user`, `wiki` (epics are not in the list even though 17.3 intro note exists). |
| `before` / `after` | date ISO 8601 | Date granularity in examples (`2017-01-31`). Example: `?target_type=issue&action=created&after=2017-01-31&before=2017-03-01&scope=all`. Whether bounds are exclusive: not documented. |
| `sort` | `asc`/`desc` | by creation date; default `desc`. |
| `page`, `per_page` | int | offset pagination (§4). |
| `scope` | string | `/events` only. |

### 3.3 Action values
Contribution events table (https://docs.gitlab.com/user/profile/contributions_calendar/#user-contribution-events): `approved` (MR), `closed` (Epic, Issue, MR, Milestone, Work item), `commented` (Alert, Commit, Design, Issue, MR, Snippet), `created` (Design, Epic, Issue, MR, Milestone, Project, Wiki page, Work item), `destroyed` (Design, Milestone, Wiki page), `expired` (project membership), `joined` (membership), `left` (membership), `merged` (MR), `pushed` ("commits to (or deleted commits from) a repository"), `reopened` (Epic, Issue, MR, Milestone), `updated` (Design, Wiki page).

**Gotcha — response `action_name` differs from filter strings.** The docs' example responses show `action_name` values `"opened"` (Issue, even for filter `action=created`), `"closed"`, `"pushed"`, `"commented on"` (with space; target_type `Note`), `"created"` (wiki). So the response `action_name` is a display-ish string, not strictly the filter enum. Mapping beyond these samples (e.g. `"accepted"` for merged, `"joined"`, `"pushed to"`, `"pushed new"`): **not documented** — handle unknown strings defensively. [INFERENCE from examples]

### 3.4 Response shape (from docs examples)
Common: `id`, `title` (null), `project_id`, `action_name`, `target_id`, `target_iid`, `target_type` ("Issue", "Note", "WikiPage::Meta", null for pushes; note capitalized class names vs lowercase filter), `author_id`, `target_title`, `created_at` (ISO 8601 Z; **absent in the `/users/:id/events` push example and some others** — treat as optional), `author` {name, username, id, state, avatar_url, web_url}, `author_username`, `imported` bool, `imported_from` ("none").
Note: `target_iid` in the project-events example equals `target_id` (160/160) whereas `/events` example differs (160 vs 53) — inconsistent docs; don't trust.

Push event (`/users/:id/events`):
```json
{ "id": 4, "title": null, "project_id": 15, "action_name": "pushed",
  "target_id": null, "target_iid": null, "target_type": null, "author_id": 1,
  "author_username": "john", "imported": false, "imported_from": "none", "target_title": null,
  "push_data": { "commit_count": 1, "action": "pushed", "ref_type": "branch",
    "commit_from": "50d4420237a9de7be1304607147aec22e4a14af7",
    "commit_to": "c5feabde2d8cd023215af4d2ceeb7a64839fc428",
    "ref": "main", "commit_title": "Add simple search to projects in public area" } }
```
`push_data` fields: `commit_count`, `action`, `ref_type`, `commit_from`, `commit_to`, `ref`, `commit_title`. Bulk push (exceeds "Push event activities limit"): `commit_count: 0`, `ref_count` = number of refs pushed, and `null` for `commit_from`, `commit_to`, `ref`, `commit_title`. Allowed values of `action`/`ref_type` besides `pushed`/`branch`: not documented.

Note event (`target_type: "Note"`, `action_name: "commented on"`):
```json
"note": { "id": 1312, "body": "What an awesome day!", "attachment": null,
  "author": {…same user object…}, "created_at": "2015-12-04T10:33:56.698Z",
  "system": false, "noteable_id": 377, "noteable_type": "Issue", "noteable_iid": 377 }
```
`noteable_iid` appears only in the project-events example (not in `/users/:id/events` example). `target_id` = note id.
Wiki event: extra `wiki_page` {format, slug, title, wiki_page_meta_id} (only when `target_type=wiki`).

### 3.4b Retention (documented)
- User activity: "GitLab removes user activity events older than **3 years** from the events table" — https://docs.gitlab.com/user/profile/contributions_calendar/#event-time-period-limit
- Project activity: same, older than three years — https://docs.gitlab.com/user/project/working_with_projects/#view-project-activity
- Contributions calendar UI shows only last 12 months.

### 3.5 Alternatives for contribution/activity data
- Contribution calendar (past 12 months) in GraphQL: `User.contributionCalendar` (type `UserContributionCalendar`) — **Experiment, introduced 19.5**, null when current user cannot read profile. https://docs.gitlab.com/api/graphql/reference/objects/ (internal fields not read). Not suitable as a hard dependency.
- `User.contributedProjects` (GraphQL connection, projects the user contributed to).
- REST: `GET /users/:user_id/contributed_projects` (rate limit mention on .com, 100/min) — endpoint listed in rate-limit table only; shape not read.
- RSS/Atom activity feed: per user profile, requires a feed token (https://docs.gitlab.com/user/profile/contributions_calendar/#retrieve-user-activity-as-a-feed).
- Group `contribution analytics` (UI; https://docs.gitlab.com/user/group/contribution_analytics/ — not read; likely Premium [INFERENCE]).
- Per-resource: list issues/MRs with `updated_after`, notes APIs, etc. (other agents' scope).
- ADE-relevant design note: for "my activity" prefer events (`GET /events?after=…`) with keyset-free offset paging; fall back to per-project issue/MR queries for MR coverage (events omit MRs per docs).

---

## 4. Pagination (REST)
Source: https://docs.gitlab.com/api/rest/#pagination

### 4.1 Offset (default; all endpoints)
- Params: `page` (default 1), `per_page` (default **20**, **max 100**).
- Response headers: `x-next-page`, `x-page`, `x-per-page`, `x-prev-page`, `x-total`, `x-total-pages`; `Link` header with `rel` = `prev`/`next`/`first`/`last` (use these links, don't build URLs).
- **Headers omitted**: if query returns **more than 10,000 records**, `x-total`, `x-total-pages` and `rel="last"` link are not returned. (Docs for GitLab.com repeat this.) https://docs.gitlab.com/api/rest/#pagination-response-headers
- Max offset: a configurable "max offset allowed" limit applies to offset pagination **only on endpoints that also support keyset pagination**; numeric default not stated in the text I read. https://docs.gitlab.com/administration/instance_limits/#max-offset-allowed-by-the-rest-api-for-offset-based-pagination
- `GET /users`: offset pagination deprecated (16.5) / keyset **enforced when >50,000 records (17.0)**.
- Example headers: `x-next-page: 3`, `x-page: 2`, `x-per-page: 3`, `x-prev-page: 1`, `x-total: 8`, `x-total-pages: 3`.

### 4.2 Keyset
- Params: `pagination=keyset`, `per_page` (≤100), `order_by` (required), `sort` (required).
- Next page via `Link: <...>; rel="next"` (contains `id_after=…` or `cursor=…`); also `X-NEXT-CURSOR`/`X-PREV-CURSOR` headers. End of data: no `Link` header and empty array. No `x-total*` headers.
- Supported resources (docs table): group audit events, groups (`order_by=name&sort=asc`, unauthenticated only), instance audit events, package pipelines, project jobs (`order_by=id&sort=desc`), project audit events, projects (`order_by=id`), users (`id|name|username|created_at|updated_at`), registry repository tags, repository tree (17.1), **project issues** (`created_at|updated_at|title|id|weight|due_date|relative_position`; introduced **18.3**), runner jobs.
- **Not supported (per this table)**: events, merge requests, group issues, notes, pipelines. → ADE must use offset pagination for those.

---

## 5. Rate limits

### 5.1 Self-managed
Source: https://docs.gitlab.com/administration/settings/user_and_ip_rate_limits/ (Offering: Self-Managed)
- Unauthenticated/authenticated API and web limits are **disabled by default** (admin toggles).
- Defaults when enabled: authenticated API **7200 requests / 3600 s per user**; authenticated web 7200/3600; unauthenticated API 3600/3600 per IP.
- `project/:id/jobs` endpoint: 600 calls/min per authenticated user default.
- Application-level limits also exist and aren't reflected in headers (issue creation, note creation, project export…). A request can 429 despite headers showing quota.
- Other defaults (instance_limits): search 30/min authenticated, 10/min unauthenticated; autocomplete users 300/min auth. https://docs.gitlab.com/administration/instance_limits/

### 5.2 GitLab.com
Source: https://docs.gitlab.com/user/gitlab_com/rate_limits/
- **Current** (in effect): authenticated API traffic per user **2,000 req/min**; authenticated non-API HTTP 1,000/min; unauthenticated per IP 500/min; all traffic per IP 2,000/min; note creation on issues/MRs 60/min; issue creation 200/min; search API 100/min per user; `GET /users/:id` 300 per 10 min; single project 400/min; group projects 600/min; `GET /projects` 2,000 per 10 min; repository files API 500/min per IP+path; user followers/following 100/min; user status 240/min; project members list 200/min; Duo `aiAction` 160/8 h.
- **Proposed tiered limits (NOT yet in effect; planned January 2027 per docs, enforcement tied to "Advanced Capacity")**: sustained per user per hour: Free 5,000, Premium 15,000, Ultimate 25,000; unauthenticated 60/hour/IP. Burst per minute: Free 100 (300 web), Premium 1,250, Ultimate 2,000. Group-level traffic limits too. Docs say lower of plan/current limit applies. ADE should assume ~5,000/hour on Free .com when budgeting.
- Rate-limit responses for **Projects, Groups, Users APIs do not include informational headers**.

### 5.3 Headers
Source: https://docs.gitlab.com/administration/settings/user_and_ip_rate_limits/#response-headers
All responses: `RateLimit-Limit` (quota normalized to per-minute), `RateLimit-Name` (e.g. `throttle_authenticated_api`), `RateLimit-Observed`, `RateLimit-Remaining` (= Limit − Observed), `RateLimit-Reset` (Unix epoch seconds). Headers show only the most restrictive Rack::Attack limit.
On `429`: plus `RateLimit-ResetTime` (RFC 2616 date, e.g. `Tue, 05 Jan 2021 11:00:00 GMT`) and `Retry-After` (seconds). Default 429 body is plain text `Retry later` (admin-customizable). Project/group creation windows are 24 h so `Retry-After` can be ≤ 86,400.
Guidance: wait `Retry-After`, exponential backoff if still throttled, watch `RateLimit-Remaining`.

---

## 6. Conditional requests / caching
- Public REST docs list `304 Not Modified` as a status code ("resource hasn't been modified since the last request") — https://docs.gitlab.com/api/rest/troubleshooting/ — but **do not document ETag/If-None-Match support for public REST endpoints** (grep of that page: no match) and give no list of endpoints.
- Internal dev doc: GitLab implements Redis-backed ETag caching for *specific polled paths* (registered in `Gitlab::EtagCaching::Router`); client sends `If-None-Match`, server returns `304` without hitting the DB. The middleware keys on **request path only and ignores query params**. https://docs.gitlab.com/development/polling — these are mostly the web frontend's JSON endpoints (e.g. pipelines polling `/projects/5/pipelines` in the example), **not guaranteed for `/api/v4`**.
- Also `Poll-Interval` header mentioned there (frontend guide).
- Rate-limit page guidance: "batch requests, cache responses, use pagination, honor Retry-After".
- Conclusion for ADE: [INFERENCE] do not rely on server ETags for `/api/v4`; send `If-None-Match` opportunistically if an `ETag` is returned, and otherwise cache client-side using `updated_after` filters (issues/MRs support it, other agents' scope) and `after` on events.

---

## 7. GraphQL
Source: https://docs.gitlab.com/api/graphql/ (Tier Free/Premium/Ultimate; all offerings)

- Endpoint: `POST /api/graphql` (e.g. `https://gitlab.com/api/graphql`). Explorer: `/-/graphql-explorer`. Versionless.
- Auth: `Authorization: Bearer <token>` header (PAT/project/group/OAuth), or `private_token` / `access_token` query param, or session cookie. Scopes: `read_api` (queries), `api` (mutations). Invalid token → HTTP 401 `{"errors":[{"message":"Invalid token"}]}`. Mutations always require auth.
- Authorization semantics: **query fields return `null` (no error) when you lack permission or the resource does not exist**; mutations return a top-level `errors` entry ("The resource that you are attempting to access does not exist or you don't have permission to perform this action"). For connections: `"field": null` = no permission; `"field": {"nodes": []}` = permitted but empty. Missing tier/feature also yields `null` silently ⇒ this is a usable tier-probe signal. [partly INFERENCE for tier]
- Limits (docs table):
  | Limit | Default |
  |---|---|
  | Max page size | 100 nodes (most connections; `subscriptionUsage.usersUsage.users` = 20); larger requests are silently truncated to max |
  | Max query complexity | **200 unauthenticated, 250 authenticated** (≈1 per field; can query score via `queryComplexity`, see getting_started) |
  | Max query size | **10,000 characters** per query/mutation (use variables/fragments) |
  | Request timeout | **30 seconds** |
  | Blob data | 20 MB total when >1 blob path |
  | Rate limits | .com: general limits (§5) |
- Pagination: Relay connections. Args `first`/`after` (forward) and `last`/`before`; default 100 per page; `pageInfo { endCursor hasNextPage }` (also `edges { cursor node }`); cursors are opaque base64 strings. Source: https://docs.gitlab.com/api/graphql/getting_started/
- Multiplex (batching multiple queries in one request) is supported (graphql-ruby multiplex).
- IDs: field/arg `id` is a Global ID `gid://gitlab/Issue/123`; projects/groups by `fullPath` (type `ID`); IIDs are `String`; issue-ish objects via (`fullPath`, `iid`).
- Deprecation: deprecated items kept ≥6 releases then removed on next `XX.0`; test with `?remove_deprecated=true`. Experiment-labeled items and default-off flagged fields can change anytime (e.g. `User.contributionCalendar`, 19.5 experiment).
- Introspection of instance: `query { metadata { version revision enterprise plan kas { enabled externalUrl externalK8sProxyUrl version } featureFlags(names:[…]) } }` — `Metadata` type fields: `enterprise` Boolean!, `featureFlags` [GitlabInstanceFeatureFlag!]! (experiment, 17.4; max 20 names; docs warn external consumers not to use feature flags), `kas`, `plan` String (nullable), `revision` String!, `version` String!. https://docs.gitlab.com/api/graphql/reference/objects/#metadata ; `query.metadata` is the top-level field. Whether `metadata` requires auth: not documented.
- Work-items migration: the reference marks many Epic/issue concepts as work items; see other agents' docs — not in my scope.

---

## 8. Feature/tier detection without admin

### 8.1 `GET /version` and `GET /metadata`
Source: https://docs.gitlab.com/api/metadata/ — Tier Free/Premium/Ultimate; .com, Self-Managed, Dedicated. Both paths return the same body (docs list them together). Docs examples include `PRIVATE-TOKEN`; whether unauthenticated access works is not documented (practically requires auth in recent versions [INFERENCE]).
```json
{
  "version": "18.1.1-ee",
  "revision": "ceb07b24cb0",
  "kas": { "enabled": true, "externalUrl": "grpc://gitlab.example.com:8150",
           "externalK8sProxyUrl": "https://gitlab.example.com:8150/k8s-proxy", "version": "18.1.1" },
  "enterprise": true,
  "plan": "ultimate"
}
```
| Field | Type | Notes |
|---|---|---|
| `version` | string | e.g. `18.1.1-ee`; `-ee` suffix suggests EE [INFERENCE from example] |
| `revision` | string | |
| `kas.*` | object | `enabled` bool; `externalUrl`, `externalK8sProxyUrl`, `version` string or null (null if disabled); `externalK8sProxyUrl` since 17.6 |
| `enterprise` | bool | "whether the GitLab instance is Enterprise Edition" — **EE ≠ licensed**: an EE install with no license is still `enterprise: true` but runs Free-tier features. |
| `plan` | string or null | **Introduced 19.6.** `premium`/`ultimate`; trial reports its plan; `free` for no license or expired trial; expired paid license still reports its plan; **`null` on GitLab.com** (per-namespace subscriptions). Absent on < 19.6 instances. |

Detection algorithm [INFERENCE built on docs]: `plan` present & non-null ⇒ instance tier (19.6+). `plan` null ⇒ GitLab.com/namespace-scoped ⇒ per-group plan needed (e.g. group/project API fields — not documented here). `plan` absent (older) ⇒ use `enterprise` flag: `false` ⇒ definitely Free (CE); `true` ⇒ unknown tier, probe features (below).

### 8.2 `GET /license` — admin only
https://docs.gitlab.com/api/license/ — Tier Free/Premium/Ultimate; **Self-Managed and Dedicated only**; prerequisite "You must have administrator access". `403 Forbidden` if user not permitted. Fields: `id, plan, created_at, starts_at, expires_at, historical_max, maximum_user_count, expired, overage, user_limit, active_users, licensee{Name,Email,Company}, add_ons{…}`. Not usable by ADE for ordinary users.

### 8.3 Premium endpoint on a Free instance — status code
- Project iterations (`GET /projects/:id/iterations`) is Tier **Premium, Ultimate** (https://docs.gitlab.com/api/iterations/). The docs **do not state** what a Free instance returns. [INFERENCE: licensed-feature gates in GitLab EE API typically return `403 Forbidden` or `404 Not Found` — unverified.] ADE must treat 403/404 on a Premium-only endpoint as "feature unavailable" and ideally combine with `/metadata` `plan`/`enterprise`.
- GraphQL: unavailable-tier fields return `null` / empty without error (see §7 and "If you receive an unexpected `null`, verify … Your instance has the required subscription tier").
- General documented statuses: 401 unauthenticated, 403 forbidden/insufficient scope, 404 "resource couldn't be accessed… or user isn't authorized", 405, 429. https://docs.gitlab.com/api/rest/troubleshooting/
- Moved projects: GET → 301 with `Location`; non-GET → 405 "Non GET methods are not allowed for moved projects". Path params with `/` must be URL-encoded (`%2F`); `+` in ISO timestamps must be `%2B`.

### 8.4 Per-field tier hints seen
- `GET /users` admin: `auditors`, `skip_ldap` Premium+. User fields `is_auditor`, `using_license_seat`, `group_saml`, `provisioned_by_group_id` Premium/Ultimate.
- Project access tokens: Premium+ on .com.
- Project iterations: Premium+.

---

## 9. Items not verified / open questions for main agent
1. Whether `read_api`-only tokens can call `GET /events` and `GET /users/:id/events` (docs list `read_user` or `api`). Test with a real token or default ADE to `read_api`+`read_user`.
2. Exact HTTP status for Premium-only REST endpoints on Free instances (not documented).
3. ETag behaviour of `/api/v4` endpoints (not documented).
4. Numeric default of the "max offset" limit for offset pagination on keyset-capable endpoints (docs text lacks value).
5. Fine-grained PAT scopes doc (https://docs.gitlab.com/auth/tokens/fine_grained_access_tokens/) not read.
6. `/personal_access_tokens/self` semantics for project/group/OAuth tokens.
