# GitLab planning APIs: issues, iterations, milestones, labels, boards, epics

Source of truth: docs.gitlab.com, read 2026-11-11 (docs version shows GitLab 19.x history entries). Every section lists its source URL. `[INFERENCE]` marks things not stated in the docs. "not documented" means the docs page says nothing.

Base for REST: `https://<host>/api/v4`. Auth header `PRIVATE-TOKEN: <token>`. Namespaced paths must be URL-encoded (`group%2Fproject`). Source: https://docs.gitlab.com/api/rest/

Common REST facts:
- `id` is unique instance-wide, `iid` is per-project. Single issues are fetched by `iid` under a project path (`GET /projects/42/issues/5`), never by `id`. https://docs.gitlab.com/api/rest/#id-vs-iid
- Null booleans are treated as false. `+` in ISO 8601 timestamps must be sent as `%2B`. Same page.
- Default page size is 20 on issues ("return 20 results by default"). https://docs.gitlab.com/api/issues/
- Array params use `name[]=` (e.g. `iids[]=42&iids[]=43`).

---

## 1. Issues API (REST)

Source: https://docs.gitlab.com/api/issues/
Tier: Free, Premium, Ultimate. Offering: GitLab.com, Self-Managed, Dedicated. Some params and response fields are Premium/Ultimate only (flagged below).

### Endpoints

| Method + path | Notes |
|---|---|
| `GET /issues` | Global. "Lists all issues the authenticated user has access to. By default, returns only issues created by the current user." Default `scope=created_by_me`. |
| `GET /groups/:id/issues` | Group issues. `scope` defaults to `all`. Does not state whether subgroups are included; GraphQL `Group.issues` has an `includeSubgroups` arg. |
| `GET /projects/:id/issues` | Project issues. `scope` defaults to `all`. Supports keyset pagination (introduced 18.3). |
| `GET /issues/:id` | "Only for administrators." Use the project form instead. |
| `GET /projects/:id/issues/:issue_iid` | Single issue. Params: `id`, `issue_iid`. |
| `POST /projects/:id/issues` | Create. |
| `PUT /projects/:id/issues/:issue_iid` | Edit; also close or reopen via `state_event`. |
| `DELETE /projects/:id/issues/:issue_iid` | Delete. |
| `PUT /projects/:id/issues/:issue_iid/reorder` | Reorder. |
| `POST /projects/:id/issues/:issue_iid/move` | Move to another project. |
| `GET /projects/:id/issues/:issue_iid/related_merge_requests`, `/closed_by`, `/participants`, `/time_stats` | Related data. |

GitLab.com gotcha: when feature flag `restrict_global_issues_scope_all` is on, `GET /issues?scope=all` returns `400 Bad Request`. Use `created_by_me`, `assigned_to_me`, or the group/project endpoints. Does not apply to Self-Managed or Dedicated.

### List filters

Documented on all three list endpoints unless noted. Types as in the docs.

| Param | Type | Notes |
|---|---|---|
| `scope` | string | `created_by_me`, `assigned_to_me`, `all`. Global default `created_by_me`; group/project default `all`. |
| `state` | string | `all`, `opened`, `closed`. |
| `assignee_id` | integer | `None` = unassigned, `Any` = has assignee. Mutually exclusive with `assignee_username`. |
| `assignee_username` | string array | Mutually exclusive with `assignee_id`. In CE the array may only hold one value, otherwise an invalid-parameter error. Only issues assigned to all passed users are returned. |
| `author_id` / `author_username` | integer / string | Mutually exclusive. `author_id` "combine with `scope=all` or `scope=assigned_to_me`". |
| `labels` | string | Comma-separated; issue must have ALL labels. `None` = no labels, `Any` = at least one. `No+Label` deprecated. Predefined names case-insensitive. |
| `milestone` | string | Milestone title. `None`/`Any` accepted; using these "will be deprecated in the future", use `milestone_id`. |
| `milestone_id` | string | Documented only in the global `/issues` table. Accepts `None`, `Any`, `Upcoming`, `Started`. Mutually exclusive with `milestone`. |
| `iteration_id` | integer | `None` / `Any` accepted. Mutually exclusive with `iteration_title`. Premium/Ultimate. |
| `iteration_title` | string | Premium/Ultimate. |
| `weight` | integer | `None` / `Any` accepted. Premium/Ultimate. |
| `epic_id` | integer | `None` / `Any` accepted. Premium/Ultimate. |
| `health_status` | string | `None` / `Any` accepted. Ultimate only. Documented in the global table. |
| `issue_type` | string | `issue`, `incident`, `test_case`, `task`. |
| `iids[]` | integer array | |
| `search` | string | Searches title and description. |
| `in` | string | `title`, `description`, or comma-joined. Default `title,description`. Documented on the global endpoint. |
| `order_by` | string | Global: `created_at`, `due_date`, `label_priority`, `milestone_due`, `popularity`, `priority`, `relative_position`, `title`, `updated_at`, `weight`. Group/project list omits `title`. Default `created_at`. |
| `sort` | string | `asc` / `desc`, default `desc`. |
| `created_after`, `created_before`, `updated_after`, `updated_before` | datetime | ISO 8601, "on or after" / "on or before". |
| `due_date` | string | `0` (none), `any`, `today`, `tomorrow`, `overdue`, `week`, `month`, `next_month_and_previous_two_weeks`. |
| `confidential` | boolean | |
| `my_reaction_emoji` | string | |
| `non_archived` | boolean | Default `true`. Documented on global and group lists. |
| `with_labels_details` | boolean | If true, `labels` becomes objects with `name`, `color`, `description`, `description_html`, `text_color`. Default false. |
| `not` | hash | Negation. Global accepts: `assignee_id`, `assignee_username`, `author_id`, `author_username`, `iids`, `iteration_id`, `iteration_title`, `labels`, `milestone`, `milestone_id`, `weight`. The group and project tables list a different set: `labels`, `milestone`, `author_id`, `author_username`, `assignee_id`, `assignee_username`, `my_reaction_emoji`, `search`, `in`. So `not[iteration_id]` is documented only on the global endpoint. |
| `cursor` | string | Project list only. Keyset pagination. |

Special values relevant to a dashboard:
- Milestone `Started` / `Upcoming` (REST): the user docs say the `Started` and `Upcoming` logic "changed in GitLab 18.0". The REST table says "The logic for `Upcoming` and `Started` differs from the logic used in the GraphQL API".
  - REST `Upcoming` = "assigned to milestones due in the future".
  - REST `Started` = "assigned to open, started milestones".
  - GraphQL `STARTED` = "open and started (overlaps current date)".
  - GraphQL `UPCOMING` = "starts in the future (start date > today)".
  - Sources: https://docs.gitlab.com/api/issues/ , https://docs.gitlab.com/api/graphql/reference/enums/ (`MilestoneWildcardId`), https://docs.gitlab.com/user/project/milestones/#special-milestone-filters
  - The user docs describe Started as excluding "milestones without a defined start and due date".
- Iteration `Current`: the REST docs do not list `Current` for `iteration_id` (only `None`, `Any`). `Current` exists in GraphQL as `IterationWildcardId.CURRENT`. For REST, resolve the current iteration via the iterations endpoints (`state=current`) and pass its `id`. This is an inference from the absence of REST docs.
- "My issues": REST `scope=assigned_to_me`, or `assignee_id=<me>` / `assignee_username[]=<me>`. GraphQL has `AssigneeWildcardId.ME`.

### Issue response fields (list and single)

Example response (trimmed from the docs):
```json
{
  "id": 76, "iid": 6, "project_id": 1,
  "title": "Consequatur vero maxime deserunt laboriosam est voluptas dolorem.",
  "description": "Ratione dolores corrupti mollitia soluta quia.",
  "state": "opened",
  "created_at": "2016-01-04T15:31:51.081Z", "updated_at": "2016-01-04T15:31:51.081Z",
  "closed_at": null, "closed_by": null, "due_date": "2016-07-22", "start_date": null,
  "labels": ["foo", "bar"],
  "milestone": {"project_id": 1, "state": "closed", "due_date": null, "iid": 2, "id": 17, "title": "v4.0",
                "description": "...", "created_at": "2016-01-04T15:31:39.996Z", "updated_at": "2016-01-04T15:31:39.996Z"},
  "assignees": [{"state": "active", "id": 1, "name": "Administrator", "web_url": "https://gitlab.example.com/root", "avatar_url": null, "username": "root"}],
  "assignee": {"...": "same shape as assignees[0] (deprecated)"},
  "author": {"state": "active", "id": 18, "web_url": "...", "name": "Alexandra Bashirian", "avatar_url": null, "username": "eileen.lowe"},
  "type": "ISSUE", "issue_type": "issue", "severity": "UNKNOWN",
  "upvotes": 4, "downvotes": 0, "merge_requests_count": 0, "user_notes_count": 1,
  "web_url": "http://gitlab.example.com/my-group/my-project/issues/6",
  "references": {"short": "#6", "relative": "my-group/my-project#6", "full": "my-group/my-project#6"},
  "time_stats": {"time_estimate": 0, "total_time_spent": 0, "human_time_estimate": null, "human_total_time_spent": null},
  "has_tasks": true, "task_status": "10 of 15 tasks completed",
  "task_completion_status": {"count": 0, "completed_count": 0},
  "confidential": false, "discussion_locked": false, "moved_to_id": null,
  "imported": false, "imported_from": "none",
  "_links": {"self": "...", "notes": "...", "award_emoji": "...", "project": "...", "closed_as_duplicate_of": "..."}
}
```

Additional fields by tier, each shown in the docs as a separate snippet:
- **Premium/Ultimate** `weight`: integer or null.
- **Premium/Ultimate** `epic`: `{"id":42,"iid":5,"title":"My epic epic","url":"/groups/h5bp/-/epics/5","group_id":8}`. Also top-level `epic_iid` (deprecated, "scheduled for removal in API version 5"; use `epic.iid`).
- **Premium/Ultimate** `iteration`:
  ```json
  {"id":90,"iid":4,"sequence":2,"group_id":162,"title":null,"description":null,"state":2,
   "created_at":"2022-03-14T05:21:11.929Z","updated_at":"2022-03-14T05:21:11.929Z",
   "start_date":"2022-03-08","due_date":"2022-03-14",
   "web_url":"https://gitlab.com/groups/my-group/-/iterations/90"}
  ```
  `title` is null for cadence-auto-generated iterations. `state` is an integer (see §2).
- **Ultimate** `health_status`: e.g. `"on_track"`. Other values: not documented in REST. GraphQL enum is `onTrack`, `needsAttention`, `atRisk`.

Notes:
- The `assignee` field is deprecated in favour of the `assignees` array.
- `references.relative` is relative to the requesting group/project. From the issue's own project it equals `short`. Across groups or projects it equals `full`.
- On Free the `milestone` object in the docs example has `project_id`; group milestones show `group_id` (see §3). Not documented whether the embedded object also carries `web_url` or `start_date`.
- `type` (e.g. `"ISSUE"`) and `issue_type` (`issue` etc.) both appear in the example. `type` is not described in the docs.
- Labels are plain name strings unless `with_labels_details=true`.
- Create is subject to content-creation rate limits: https://docs.gitlab.com/rate_limits/content_creation/ (linked from the issues page). The update rate limit is described under Edit below.

### Create: `POST /projects/:id/issues`

Params (docs):

| Param | Type | Notes |
|---|---|---|
| `id` | int/string | Required. |
| `title` | string | Required. |
| `description` | string | Max 1,048,576 characters. |
| `assignee_id` | integer | "Only appears on GitLab Free". |
| `assignee_ids` | integer array | Premium/Ultimate. |
| `confidential` | boolean | Default false. |
| `due_date` | string | `YYYY-MM-DD`. |
| `start_date` | string | `YYYY-MM-DD`. Introduced in GitLab 19.1. |
| `labels` | string | Comma-separated names. A missing label is created as a new project label. Labels per issue are limited (100, per the user docs). |
| `milestone_id` | integer | The **global** milestone ID. Mutually exclusive with `milestone`. |
| `milestone` | string | Title of a project or ancestor-group milestone, case-sensitive exact match. |
| `weight` | integer | >= 0. Premium/Ultimate. |
| `epic_id` | integer | Premium/Ultimate. |
| `epic_iid` | integer | Deprecated. |
| `issue_type` | string | `issue` (default), `incident`, `test_case`, `task`. |
| `severity` | string | Incidents only. |
| `created_at`, `iid` | | Admin / owner only. |
| `discussion_to_resolve`, `merge_request_to_resolve_discussions_of` | | |

**Gotcha:** there is NO `iteration_id` / `iteration_title` param on REST create or update in the docs. Iteration assignment via REST is not documented. Use GraphQL (`createIssue` has `iterationId`, or `iterationWildcardId: CURRENT` plus `iterationCadenceId`; `issueSetIteration` exists) or the `/iteration` quick action in a description or note. The quick-action route is an inference.

### Edit: `PUT /projects/:id/issues/:issue_iid`

At least one parameter is required. Params: `add_labels`, `remove_labels`, `labels` (empty string clears all), `assignee_ids` (`0` or empty unassigns all), `milestone_id` (`0` or empty unassigns), `milestone`, `title`, `description`, `confidential`, `discussion_locked`, `due_date`, `start_date`, `weight`, `epic_id`, `epic_iid`, `issue_type`, `severity`, `state_event` (`close` | `reopen`), `created_at`, `updated_at` (admin/owner). The docs list `assignee_id` among the "at least one" params, but its row is missing from the attribute table.

Update rate limit: 600 requests/min/user, shared with UI, REST and GraphQL updates of issues, epics and work items. It is behind feature flag `work_item_update_rate_limit` (introduced 19.6, disabled by default). Exceeding it returns 429. Setting: `work_item_update_limit`.

Pagination: offset by default. Project issues also supports keyset (`cursor`; "should use keyset-based pagination when requesting consecutive pages"; introduced 18.3). Group and global lists: keyset not documented. https://docs.gitlab.com/api/rest/ (pagination section)

---

## 2. Iterations

Tier: **Premium, Ultimate** (GitLab.com, Self-Managed, Dedicated). Not available on Free. No Free fallback is documented.

### REST

Sources: https://docs.gitlab.com/api/group_iterations/ , https://docs.gitlab.com/api/iterations/

- `GET /groups/:id/iterations`
- `GET /projects/:id/iterations`. "Projects do not have their own iterations." It lists iterations of the project's ancestor groups.

Params (same on both):

| Param | Type | Notes |
|---|---|---|
| `state` | string | `opened`, `upcoming`, `current`, `closed`, `all`. |
| `search` | string | Title match. |
| `in` | array of strings | `title`, `cadence_title`. Default `[title]`. |
| `include_ancestors` | boolean | Default `true`. |
| `include_descendants` | boolean | Default `false`. |
| `updated_before`, `updated_after` | datetime | ISO 8601. |

Response (group endpoint, as documented):
```json
[{"id": 53, "iid": 13, "sequence": 1, "group_id": 5, "title": "Iteration II", "description": "Ipsum Lorem ipsum",
  "state": 2, "created_at": "2020-01-27T05:07:12.573Z", "updated_at": "2020-01-27T05:07:12.573Z",
  "due_date": "2020-02-01", "start_date": "2020-02-14",
  "web_url": "http://gitlab.example.com/groups/my-group/-/iterations/13"}]
```
- The project endpoint's example omits `sequence`, but the issue example includes it.
- Iterations created by cadence "Enable automatic scheduling" return `title: null` and `description: null`.
- **`state` is an integer in REST responses**, even though the `state` *filter* takes strings. The docs show `2` but never define the mapping. [INFERENCE: GitLab source uses 1 = upcoming, 2 = current/started, 3 = closed. Verify against a real instance.]
- Cadence id/title are not in the REST iteration object (not documented).
- No documented REST endpoints to create or edit iterations, and no documented REST endpoints for cadences. The searched docs show only list endpoints. Use GraphQL for those.
- Pagination: not stated on these pages; default REST offset pagination. [INFERENCE]

### Iteration cadences (GraphQL)

Source: https://docs.gitlab.com/api/graphql/reference/objects/ (`IterationCadence`, `Iteration`, `Group`, `Project`), https://docs.gitlab.com/user/group/iterations/

`IterationCadence` fields: `id: IterationsCadenceID!`, `title: String!`, `description`, `automatic: Boolean` ("automatically generate upcoming iterations"), `active: Boolean`, `durationInWeeks: Int`, `iterationsInAdvance: Int` ("future iterations to schedule in addition to the current one"), `rollOver: Boolean!`, `startDate: Date` ("Automation start date").

Query: `Group.iterationCadences` and `Project.iterationCadences`. Args: `active`, `automatic`, `durationInWeeks`, `id`, `includeAncestorGroups`, `title` (fuzzy). Connection pagination (`first`/`after`...).

`Iteration` (GraphQL) fields: `id: ID!`, `iid: String!`, `sequence: Int!`, `title`, `description`, `descriptionHtml`, `state: IterationState!` (`all`, `closed`, `current`, `opened`, `upcoming`), `startDate: Time`, `dueDate: Time`, `createdAt`, `updatedAt`, `group: Group!`, `iterationCadence: IterationCadence!`, `webPath`, `webUrl`, `scopedPath`, `scopedUrl` (project parents only), `report`.

Query: `Group.iterations` / `Project.iterations`. Args: `id`, `iid`, `in` (`TITLE`, `CADENCE_TITLE`), `includeAncestors` (default true), `includeDescendants`, `iterationCadenceIds`, `search`, `sort` (`CADENCE_AND_DUE_DATE_ASC|DESC`), `state`, `timeframe`, `title` (deprecated). There is also top-level `Query.iteration(id:)`.

Mutations: `iterationCadenceCreate` (args `groupPath!`, `active!`, `automatic!`, `title`, `description`, `startDate`, `durationInWeeks`, `iterationsInAdvance`, `rollOver`), `iterationCadenceUpdate`, `iterationCadenceDestroy`, `iterationCreate`, `updateIteration`, `issueSetIteration` (`projectPath!`, `iid!`, `iterationId`). `createIteration` is deprecated since 14.0.

User-docs behaviour: cadences auto-create iterations every 1, 2, 3 or 4 weeks. Iteration date ranges cannot overlap inside a cadence. Iterations are group-level only. Title, start and due dates are only editable when automatic scheduling is off. Roll-over is done by the "GitLab Automation Bot" user at midnight instance time zone.

---

## 3. Milestones

Tier: **Free, Premium, Ultimate**. Sources: https://docs.gitlab.com/api/milestones/ , https://docs.gitlab.com/api/group_milestones/ , https://docs.gitlab.com/user/project/milestones/

### Endpoints

Project: `GET|POST /projects/:id/milestones`, `GET|PUT|DELETE /projects/:id/milestones/:milestone_id`, `GET .../issues`, `GET .../merge_requests`, `POST .../promote`, `GET .../burndown_events` (Premium/Ultimate).

Group: `GET|POST /groups/:id/milestones`, `GET|PUT|DELETE /groups/:id/milestones/:milestone_id`, `GET .../issues`, `GET .../merge_requests`, `GET .../burndown_events` (Premium/Ultimate).

`:milestone_id` is the milestone's `id` (not `iid`), documented as "The ID of the project's milestone".

### List params (both)

`iids[]` (ignored if `include_ancestors=true`), `state` (`active` | `closed`), `title` (group: case-sensitive), `search` (title or description), `search_title` (multiple terms separated by `+` or `%20`; all must match), `include_ancestors`, `include_parent_milestones` (deprecated), `updated_before`, `updated_after`, `containing_date` (`start_date <= x <= due_date`), `start_date` + `end_date` (must be passed together), `sort` (`due_date_asc` default, `due_date_desc`, `start_date_asc|desc`, `name_asc|desc`, `created_asc|desc`, `updated_asc|desc`). Group only: `include_descendants`.

### Fields

Project example:
```json
{"id": 12, "iid": 3, "project_id": 16, "title": "10.0", "description": "Version",
 "due_date": "2013-11-29", "start_date": "2013-11-10", "state": "active",
 "updated_at": "2013-10-02T09:24:18Z", "created_at": "2013-10-02T09:24:18Z", "expired": false}
```
Group example: same but with `"group_id": 16` instead of `project_id`, plus `"web_url": "https://gitlab.com/groups/gitlab-org/-/milestones/42"`. The project example shows no `web_url`.

States: `active`, `closed`. Update via `state_event` = `close` | `activate`. `expired` is boolean. Dates are `YYYY-MM-DD` and either may be null. Per the user docs, "A milestone closes after its due date".

### Issues for a milestone

- `GET /projects/:id/milestones/:milestone_id/issues` returns issues in the usual issue shape. No filter params are documented.
- `GET /groups/:id/milestones/:milestone_id/issues` does NOT include issues from subgroups. The docs suggest `GET /issues?milestone=1.0.0&state=opened` instead.
- For dashboards prefer `GET /issues?milestone_id=Started` or the list endpoints with `milestone`, which support filters and pagination.

Title rules: duplicate milestone titles are not allowed across ancestors/children (user docs). Project milestones apply to issues in that project only. Group milestones apply to any issue in the group's projects. Issues can also use project milestones from ancestor groups via the `milestone` title.

GraphQL `Milestone` fields: `id`, `iid`, `title`, `description`, `state` (`active`|`closed`), `startDate`, `dueDate`, `expired`, `upcoming`, `groupMilestone`/`projectMilestone`/`subgroupMilestone`, `stats`, `webPath`, `createdAt`, `updatedAt`, `project`, `group`. Queries: `Group.milestones`, `Project.milestones` (args incl. `containingDate`; other args not read). Source: https://docs.gitlab.com/api/graphql/reference/objects/

---

## 4. Labels

Sources: https://docs.gitlab.com/api/labels/ , https://docs.gitlab.com/api/group_labels/ (referenced, not read), https://docs.gitlab.com/user/project/labels/

### Basics

- Tier: Free, Premium, Ultimate.
- Types: project, group, and instance labels ("created by instance administrators and copied to all new projects"). Max 100 labels per work item.
- `GET /projects/:id/labels` params: `with_counts`, `include_ancestor_groups` (default true), `search`, `archived`. Offset pagination.
- Single: `GET /projects/:id/labels/:label_id`. `label_id` accepts an ID or a title.
- Create: `POST /projects/:id/labels` with `name`, `color` (required; `#RRGGBB` or a CSS color name), `description`, `priority`, `archived`.
- Update: `PUT /projects/:id/labels/:label_id` with `new_name`, `color`, `description`, `priority`, `archived`.
- Delete: `DELETE /projects/:id/labels/:label_id`. The old `DELETE /projects/:id/labels` with `name` is deprecated.
- `archived` attribute: introduced 18.3, GA in 18.10 per the API page (the user page says 18.11 [conflict between pages]).
- Response example:
  ```json
  {"id": 1, "name": "bug", "color": "#d9534f", "text_color": "#FFFFFF", "description": "Bug reported by user",
   "description_html": "Bug reported by user", "open_issues_count": 1, "closed_issues_count": 0,
   "open_merge_requests_count": 1, "subscribed": false, "priority": 10, "is_project_label": true, "archived": false}
  ```
  Counts appear with `with_counts=true` (the doc example does not say which fields are conditional).
- Minimum role to create/edit/delete labels is Planner (changed from Reporter in 17.7).
- Filtering issues: `labels=a,b` is AND semantics. `not[labels]` is supported. Scoped wildcard filter `scope::*` is described only for the web UI: "enter `<scope>::*` in the searched label name." That the REST `labels` param accepts `scope::*` is not documented.

### Scoped labels (`key::value`)

- Tier: **Premium, Ultimate** (section header in https://docs.gitlab.com/user/project/labels/#scoped-labels). Offering: all.
- Behaviour: "An issue, merge request, or epic cannot have two scoped labels of the form `key::value` with the same `key`. If you add a new label with the same `key` but a different `value`, the previous `key` label is replaced."
- Nested scopes: everything before the LAST `::` is the scope. `workflow::backend::review` and `workflow::backend::development` are mutually exclusive. `workflow::frontend::review` is not exclusive with them.
- Moving an issue across board lists with scoped labels swaps the label too.
- Free behaviour: the docs do not say what happens on Free. The label-creation steps on all tiers say the title text box "can also be used to create scoped, mutually exclusive labels", but the Scoped labels section is tagged Premium/Ultimate. [INFERENCE: on Free, a label named `a::b` can be created and assigned, but exclusivity is not enforced and the UI does not render it as scoped. Not stated in docs.] ADE should enforce nothing on its own for `::` labels, and if it wants single-value semantics on Free it must remove sibling labels itself (e.g. `remove_labels` in the same PUT).
- REST `labels` or `add_labels` on update with a replacing scoped label: the replacement behaviour via REST is not explicitly documented. [INFERENCE: it follows the UI rule on Premium.]

---

## 5. Issue boards

Sources: https://docs.gitlab.com/api/boards/ , https://docs.gitlab.com/api/group_boards/ , https://docs.gitlab.com/user/project/issue_board/

Tier matrix (user doc):

| Tier | Project boards | Group boards | Configurable (scoped) boards | Assignee lists |
|---|---|---|---|---|
| Free | Multiple | 1 | No | No |
| Premium | Multiple | Multiple | Yes | Yes |
| Ultimate | Multiple | Multiple | Yes | Yes |

- Multiple project boards: all tiers. Multiple group boards: Premium/Ultimate. (The API page marks `POST /groups/:id/boards` as Premium/Ultimate.)
- List types:
  - Open and Closed: built in, always present, leftmost/rightmost. They are not returned by the lists endpoint ("Does not include `open` and `closed` lists").
  - Label lists: Free.
  - Assignee lists: Premium/Ultimate.
  - Milestone lists: Premium/Ultimate.
  - Iteration lists: Premium/Ultimate.
  - Status lists: Premium/Ultimate. Introduced 18.2, GA in 18.4. The `status_id` REST param for list creation was introduced in 19.6. Status lists can include open and closed issues.
- Board scope (milestone, labels, assignee, weight, iteration): Premium/Ultimate ("Configurable issue boards").

### Endpoints (project; group mirrors under `/groups/:id/boards`)

- `GET /projects/:id/boards`, `GET /projects/:id/boards/:board_id`, `POST /projects/:id/boards` (`name`, max 255), `PUT /projects/:id/boards/:board_id` (`name`, `hide_backlog_list`, `hide_closed_list`, and Premium/Ultimate `assignee_id`, `milestone_id`, `labels`, `weight`), `DELETE /projects/:id/boards/:board_id`.
- Lists: `GET /projects/:id/boards/:board_id/lists`, `GET .../lists/:list_id`, `POST .../lists`, `PUT .../lists/:list_id` (updates position), `DELETE .../lists/:list_id`.
- Create-list params: `label_id`, `assignee_id`, `milestone_id`, `iteration_id`, `status_id` (assignee/milestone/iteration/status are Premium/Ultimate). "Label, assignee, milestone, iteration, and status arguments are mutually exclusive."
- The group-board lists endpoints were not read. [INFERENCE: they mirror the project ones.]
- GraphQL: `Project.boards`, `Project.board(id)`, `Group.boards`, `Group.board(id)`, mutation `boardListCreate`. Other board fields not read.

Board response (project, trimmed from the docs):
```json
{"id": 1, "name": "board1", "hide_backlog_list": false, "hide_closed_list": false,
 "project": {"id": 5, "name": "Diaspora Project Site", "path_with_namespace": "diaspora/diaspora-project-site", "web_url": "http://example.com/diaspora/diaspora-project-site"},
 "milestone": {"id": 12, "title": "10.0"},
 "lists": [{"id": 1, "label": {"name": "Testing", "color": "#F0AD4E", "description": null},
            "position": 1, "max_issue_count": 0, "max_issue_weight": 0, "limit_metric": null}]}
```
- Group boards show `"group": {"id", "name", "web_url"}` instead of `project`, and group list objects only show `id`, `label`, `position` in the docs.
- Create-board response (project) additionally shows `"group": null, "assignee": null, "labels": [], "weight": null`.
- Premium/Ultimate responses have different parameters, per the docs. The exact extra fields are not itemized beyond the example above.
- For non-label lists, the list object's field names (`assignee`, `milestone`, `iteration`) are not shown in the REST docs. Only the status list shape is shown: `status: {id, name, icon_name, color, position, description, category}` with `label: null`.
- If a project has no board, GET returns `[]`. Deleting the last board makes GitLab create a default `Development` board (17.6 behaviour, user doc).
- Issues in a board list are not returned by these endpoints. To list a board list's issues, filter issues by the list's label/milestone/assignee. [INFERENCE]
- Role: Planner or higher to manage.

---

## 6. Epics

Tier: **Premium, Ultimate**. ADE should not depend on them. Sources: https://docs.gitlab.com/api/epics/ , https://docs.gitlab.com/api/graphql/epic_work_items_api_migration_guide/

- The REST page is titled "Epics API (deprecated)": "deprecated in GitLab 17.0 and is planned for removal in v5 of the API." From 17.4 to 18.0 with the new look enabled, and in 18.1 and later, "use the Work Items API instead". The migration guide softens this: the REST API "is still supported but deprecated", "continues to work", "does not receive new features", and has "no set removal date, but it will happen in a major release".
- Epics are work items now (GA in 18.1). Legacy epic IDs differ from work item IDs. Only `iid` matches. REST responses include `work_item_id`.
- The GraphQL `Epic` type is planned for removal in 19.0. `Issue.epic` is "Deprecated in GitLab 17.5. Replaced by WorkItem type." Issue create/update `epicId` is "Deprecated in 17.5, replaced by WorkItem hierarchyWidget".
- Without the feature, epics endpoints return `403`.
- REST endpoints: `GET /groups/:id/epics`, `GET /groups/:id/epics/:epic_iid`, create/update/delete (not fully read). Filters on the list: `author_id`, `author_username`, `labels`, `state` (`opened`, `closed`, `all`; default `all`), `search`, `order_by` (`created_at`, `updated_at`, `title`), date filters, `include_ancestor_groups` (default false), `include_descendant_groups` (default true), `my_reaction_emoji`, `not`. Response fields include `id`, `work_item_id`, `iid`, `group_id`, `parent_id`, `title`, `state`, `web_url`, `references`, `labels`, `start_date`, `due_date`, `color`.
- Work-item way: `group(fullPath:) { workItems(types: [EPIC]) { nodes { id iid title } } }`.
- Recommendation for ADE: only read the `epic` object on issues when present, treat it as optional. Do not use the epics REST API or the GraphQL `Epic` type. On Free there is no epic data at all. Parent-grouping on Free should come from milestones or labels.
- The issue list filter `epic_id` and the response `epic` / `epic_iid` fields are Premium/Ultimate, and `epic_iid` is scheduled for removal.

---

## 7. GraphQL equivalents (assigned issues + iteration + milestone in one query)

Sources: https://docs.gitlab.com/api/graphql/reference/queries/ (`issues`), https://docs.gitlab.com/api/graphql/reference/objects/ (`Issue`, `Group.issues`, `Project.issues`), https://docs.gitlab.com/api/graphql/reference/enums/

Entry points:
- `Query.issues`: status Experiment (introduced 15.6). "Find issues visible to the current user. **At least one filter must be provided.**" Returns `IssueConnection`.
- `Group.issues`: same args plus `includeSubgroups: Boolean`, `includeArchived`.
- `Project.issues`: same args minus `includeSubgroups`, plus `releaseTag`, `releaseTagWildcardId`. Project args were extracted by name only.
- Pagination: connection style (`first`, `after`, `last`, `before`; `pageInfo`). `IssueConnection` also exposes `count` (capped at `limit + 1`, optional `limit` arg) and `weight`.

Filter args (all three):
- Assignment: `assigneeUsernames: [String!]`, `assigneeId: String` (`NONE`/`ANY` wildcard values), `assigneeWildcardId: AssigneeWildcardId` (`ANY`, `ME`, `NONE`; incompatible with the username args), `assigneeUsername` (deprecated 13.11).
  - GraphQL has no `scope` arg. "Assigned to me" = `assigneeWildcardId: ME` or `assigneeUsernames: ["me"]`.
  - `ME` is documented in the enum ("Logged-in user is assigned").
- State: `state: IssuableState` (`all`, `closed`, `locked`, `opened`).
- Time: `updatedAfter`, `updatedBefore`, `createdAfter`, `createdBefore`, `closedAfter`, `closedBefore` (type `Time`), `dueAfter`, `dueBefore`.
- Iteration: `iterationId: [ID]` (global IDs), `iterationTitle: String`, `iterationWildcardId: IterationWildcardId` (`ANY`, `CURRENT`, `NONE`), `iterationCadenceId: [IterationsCadenceID!]`.
- Milestone: `milestoneTitle: [String]`, `milestoneWildcardId: MilestoneWildcardId` (`ANY`, `NONE`, `STARTED`, `UPCOMING`).
- Labels: `labelName: [String]`.
- Other: `search` + `in: [IssuableSearchableField!]` (default `[TITLE, DESCRIPTION]`), `types: [IssueType!]`, `iids`, `weight` / `weightWildcardId`, `healthStatusFilter` (`ANY`, `NONE`, `atRisk`, `needsAttention`, `onTrack`), `epicId` / `epicWildcardId`, `confidential`, `myReactionEmoji`, `subscribed`, `status` (work item status filter, experiment 18.0), `not: NegatedIssueFilterInput`, `or: UnionedIssueFilterInput`.
- Sort: `sort: IssueSort`, default `created_desc`. Values include `CREATED_ASC|DESC`, `UPDATED_ASC|DESC`, `DUE_DATE_ASC|DESC`, `MILESTONE_DUE_ASC|DESC`, `PRIORITY_ASC|DESC`, `LABEL_PRIORITY_ASC|DESC`, `RELATIVE_POSITION_ASC`, `TITLE_ASC|DESC`, `WEIGHT_ASC|DESC`, `HEALTH_STATUS_ASC|DESC`, `STATUS_ASC|DESC`, `POPULARITY_ASC|DESC`, `CLOSED_AT_ASC|DESC`. Lowercase `created_asc` etc. are deprecated (13.5).
- Issue types (`IssueType`): `ISSUE`, `INCIDENT`, `TEST_CASE`, `TASK`, `REQUIREMENT`, `TICKET`, `OBJECTIVE`, `KEY_RESULT`, `EPIC`.

`Issue` fields relevant to ADE: `id: ID!` (global ID), `iid: String!`, `title`, `description`, `state: IssueState!`, `createdAt`, `updatedAt`, `closedAt`, `dueDate`, `startDate`, `webUrl`, `webPath`, `reference(full: Boolean)`, `projectId: Int`, `type: IssueType`, `confidential`, `weight: Int`, `healthStatus: HealthStatus` (enum `onTrack`, `needsAttention`, `atRisk`), `assignees` (connection of `UserCore`), `author`, `labels` (`LabelConnection`), `milestone: Milestone`, `iteration: Iteration`, `epic` (deprecated 17.5), `hasEpic`, `taskCompletionStatus: TaskCompletionStatus!`, `timeEstimate`, `totalTimeSpent`, `blocked`, `blockedByCount`, `blockingCount`, `userNotesCount`, `upvotes`, `downvotes`, `mergeRequestsCount`, `status: WorkItemStatus` (experiment 18.0), `workItemType`, `relativePosition`, `userPermissions`, `discussionLocked`, `movedTo`, `closedAsDuplicateOf`. `has_tasks` has no direct GraphQL field. `taskCompletionStatus` exists (subfields not read).

Example (composed from the documented args and fields, not copied from the docs; field subselections like `UserCore.username` and `Label.title` are [INFERENCE] from the type names, not read):
```graphql
query MyIssues($after: String) {
  issues(assigneeWildcardId: ME, state: opened, sort: UPDATED_DESC, first: 50, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id iid title webUrl state updatedAt dueDate weight healthStatus
      labels { nodes { title color } }
      milestone { id title startDate dueDate state expired }
      iteration { id iid title sequence startDate dueDate state iterationCadence { id title } }
      assignees { nodes { username } }
    }
  }
}
```
On Free the `iteration`, `weight`, `healthStatus` fields are present in the schema but not populated. [INFERENCE]. The docs do not say what GraphQL returns for gated fields on Free.

Mutations for write paths (all return `errors`):
- `createIssue` (input `projectPath!`, `title!`, `description`, `assigneeIds`, `labels`, `labelIds`, `milestoneId`, `iterationId`, `iterationWildcardId: CURRENT`, `iterationCadenceId`, `weight`, `healthStatus`, `dueDate`, `type`, `confidential`, ...).
- `updateIssue` (input `projectPath!`, `iid!`, `addLabelIds`, `removeLabelIds`, `labelIds`, `milestoneId` (null removes it), `stateEvent: CLOSE|REOPEN`, `title`, `description`, `dueDate`, `weight`, `healthStatus`, ...). It has no `iterationId` and no `assigneeIds`.
- `issueSetAssignees`, `issueSetIteration`, `issueMove` exist.
- `workItemUpdate` / `workItemCreate` are the work-item path (experiment).

Work items: issues are work items now. `Query.workItem` and `workItems` exist (experiment). Issue `status` and custom fields are surfaced only through work items / newer args. For ADE's dashboard the `issues` connection is sufficient. The epic migration guide notes the work item API is "marked as experimental" while used in production.

---

## Summary for ADE (what to assume per tier)

| Feature | Free | Premium/Ultimate |
|---|---|---|
| Issues list/get/create/edit REST | Yes | Yes |
| `assignee_id` single assignee on create | Yes (`assignee_id`) | `assignee_ids` array |
| `iteration` / `iteration_id` filter, `iteration` field | No | Yes (REST read-only; write via GraphQL) |
| `weight`, `epic`, `health_status` (Ultimate) | No | Yes |
| Milestones (project + group) | Yes | Yes (+ burndown events) |
| Iterations + cadences | No | Yes |
| Scoped-label exclusivity | No (exclusivity unspecified, treated as plain label) | Yes |
| Project boards (label lists) | Yes, multiple | Yes (+ assignee/milestone/iteration/status lists, scoped boards) |
| Group boards | 1 | Multiple |
| Epics | No | Yes, but REST deprecated; use work items |
