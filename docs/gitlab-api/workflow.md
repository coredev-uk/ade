# GitLab workflow API reference (issue links, work items, MRs, discussions, approvals, pipelines)

Scope: built from docs.gitlab.com only (no live instance). Docs fetched on 2026-11-11; they describe GitLab **19.x** (history notes mention 19.0–19.6). Anything not read from a doc is tagged `[INFERENCE]`. "Not documented" means the page says nothing.

Global REST conventions (source: https://docs.gitlab.com/api/rest/):
- Base `https://<host>/api/v4`. Auth `PRIVATE-TOKEN: <token>`. Project `:id` = numeric ID or URL-encoded path (`group%2Fproject`).
- Offset pagination: `page` (default 1), `per_page` (default 20, max 100); `Link` header with `rel=prev|next|first|last`. Total headers (`X-Total`, etc.) are omitted if a query returns >10,000 records; on GitLab.com some headers may be missing. There is a max-offset limit.
- Keyset pagination: `pagination=keyset&per_page=..&order_by=..&sort=..`; next page URL is in `Link: <...>; rel="next"`; absent Link + empty array = end. Only selected endpoints. Among the endpoints below: `GET /projects/:id/jobs` (recommended keyset) and `GET /pipelines` (keyset ONLY, offset unsupported, `cursor` param). Everything else here is offset.
- `IID` = per-project integer; `id` = global integer. MR/issue paths always use IID.

---------------------------------------------------------------------------

## 1. Issue links API

Source: https://docs.gitlab.com/api/issue_links/ · Page tier: **Free, Premium, Ultimate**; GitLab.com, Self-Managed, Dedicated. User docs: https://docs.gitlab.com/user/project/issues/related_issues/

| Operation | Method + path |
|---|---|
| List links of an issue | `GET /projects/:id/issues/:issue_iid/links` |
| Get one link | `GET /projects/:id/issues/:issue_iid/links/:issue_link_id` |
| Create link | `POST /projects/:id/issues/:issue_iid/links` |
| Delete link | `DELETE /projects/:id/issues/:issue_iid/links/:issue_link_id` |

**Tiers / link_type**
- Valid `link_type`: `relates_to`, `blocks`, `is_blocked_by`; default `relates_to` (create). API page does NOT state a tier per type.
- User docs: "relates to" is Free; **`blocks` / `is_blocked_by` ("Blocking issues") are Premium, Ultimate** (https://docs.gitlab.com/user/project/issues/related_issues/#blocking-issues). Whether the API rejects `blocks` on Free is not documented `[INFERENCE: it does]`.
- Min role to link: Guest (changed from Reporter in 17.0) on BOTH projects; "user must be allowed to update both issues" (API page).
- Cross-project: supported. Create takes `target_project_id` (ID or URL-encoded path) + `target_issue_iid`; UI doc: "You can link issues in different projects."; relationship hidden in UI unless user can see both issues.
- Bidirectional: creating A blocks B makes B "is_blocked_by" A when viewed from B `[INFERENCE from "two-way relationship"; the response for list returns link_type relative to the queried issue — not explicitly documented]`.

**List response** (array, sorted by link creation ascending; filtered by user authorization). Each element is an *issue* object plus link fields — note `id` is the linked issue's global ID, not the link's:
```json
[{
  "id": 84, "iid": 14, "issue_link_id": 1, "project_id": 4,
  "created_at": "2016-01-07T12:44:33.959Z", "title": "Issues with auth", "state": "opened",
  "assignees": [], "assignee": null, "labels": ["bug"],
  "author": {"name":"Alexandra Bashirian","avatar_url":null,"state":"active","web_url":"https://gitlab.example.com/eileen.lowe","id":18,"username":"eileen.lowe"},
  "description": null, "updated_at": "2016-01-07T12:44:33.959Z", "milestone": null,
  "user_notes_count": 0, "due_date": null, "web_url": "http://example.com/example/example/issues/14",
  "confidential": false, "weight": null,
  "link_type": "relates_to", "link_created_at": "2016-01-07T12:44:33.959Z", "link_updated_at": "2016-01-07T12:44:33.959Z"
}]
```
**Get / Create / Delete response** (single): `{ "id": <issue_link_id>, "source_issue": {issue obj}, "target_issue": {issue obj}, "link_type": "relates_to" }`. Issue obj fields in the example: id, iid, project_id, created_at, title, state, assignees, assignee, labels, author, description, updated_at, milestone, subscribed, user_notes_count, due_date, web_url, confidential, weight.
- `id` in the get/create/delete response introduced in **18.9** (https://gitlab.com/gitlab-org/gitlab/-/work_items/585093). "Retrieve an issue link" endpoint is documented on the same page (history marks only the `id` attribute as 18.9).
- Delete: params `id`, `issue_iid`, `issue_link_id` (plus an odd `link_type` row in the doc table); returns the same JSON object as create. Status code not stated on the page `[INFERENCE: 200]`.
- Create example request: `POST /projects/4/issues/1/links?target_project_id=5&target_issue_iid=1`.
- Pagination: not stated; default is offset (REST convention).
- Gotcha: this API is issue-only. Links involving epics/tasks/objectives use the work-item API (§2). Quick actions `/relate /blocks /blocked_by /unlink` exist (https://docs.gitlab.com/user/work_items/linked_items/).

---------------------------------------------------------------------------

## 2. Work items (GraphQL; there is no REST work-items API)

Sources: https://docs.gitlab.com/api/graphql/reference/{queries,mutations,objects,input_objects,enums}/ · https://docs.gitlab.com/user/work_items/ · https://docs.gitlab.com/user/work_items/child_items/ · https://docs.gitlab.com/user/work_items/linked_items/ · https://docs.gitlab.com/api/graphql/epic_work_items_api_migration_guide

**REST:** `GET https://docs.gitlab.com/api/work_items/` is a 404 — no REST work-items reference exists in docs. The only REST relatives are: Issues API (`issue_type` filter/param: `issue|incident|test_case|task`; create/list/update), Issue links API (§1), and the deprecated Epics REST API (Premium/Ultimate; deprecated 17.0, "planned for removal in v5", "use Work Items API instead from 18.1"; response includes `work_item_id` which maps to GraphQL `gid://gitlab/WorkItem/<id>`; legacy Epic id ≠ WorkItem id, only `iid` matches) — https://docs.gitlab.com/api/epics/.

**Migration status of issues**
- Issues are work items of type `issue`; the work-item view for issues is GA in **18.7** (flag `work_item_view_for_issues` removed). The unified "Plan > Work items" list replaced the separate Issues/Epics list pages in **18.10** (GA, flag `work_item_planning_view` removed); `/issues/:iid` and `/epics/:iid` URLs redirect to `/work_items/:iid`.
- Epics became work items in 17.2 (beta); epic GraphQL API "planned for removal in GitLab 19.0" (migration guide); epic REST API deprecated, no removal date.
- Legacy REST issues API still works and is not marked deprecated. Whole GraphQL work-item surface is still labelled `Status: Experiment` per field in the reference (workItem introduced as experiment in 15.1), while the migration guide says it "is used in production" and "will be GA before 19.0" (guide text may be stale).
- Issue `iid` == work item `iid` within the project `[INFERENCE from epics guide stating iid is shared; same model]`. Work item global ID format: `gid://gitlab/WorkItem/<id>`. Work item type IDs: `gid://gitlab/WorkItems::Type/<n>`, NOT guaranteed identical across instances — query them.

**Types** (https://docs.gitlab.com/user/work_items/): Issue, Epic, Task, Objective, Key result, Test case, Incident, Ticket (GraphQL `IssueType` enum: `EPIC, INCIDENT, ISSUE, KEY_RESULT, OBJECTIVE, REQUIREMENT, TASK, TEST_CASE, TICKET`). Configurable/custom types are Premium/Ultimate, GA 19.1; custom types copy issue widgets & hierarchy restrictions.

**Tiers**
- Work items / issues / tasks / child items / linked items framework: Free, Premium, Ultimate.
- Epics: Premium, Ultimate. Nested child epics (multi-level, up to 7 levels): **Ultimate** (epic→issue is Premium).
- Objectives/key results: Ultimate (GitLab.com & Self-Managed only). Weight/status/iteration/health fields on items are Premium/Ultimate (health = Ultimate).
- `blocks`/`is_blocked_by` between work items: Premium, Ultimate.

### Queries
- `workItem(id: WorkItemID!)` (top-level; experiment, 15.1) → `WorkItem`.
- `namespace(fullPath: ID!) { workItem(iid: "10") {...} }` — pattern used by the migration guide (the field `workItem(iid)` on Namespace is used in the guide's examples; its schema entry not read directly).
- `workItemsByReference(refs: [String!]!, contextNamespacePath: ID)` → `WorkItemConnection` (16.7, experiment). refs may be short refs or URLs.
- `project(fullPath){ workItems(...) }`, `group(fullPath){ workItems(...) }`, `namespace(fullPath){ workItems }` → `WorkItemConnection` (Relay connection: `nodes`, `pageInfo{endCursor hasNextPage}`, args `first/after/last/before`; `count` extra field).
  `Project.workItems` args (read from reference): `assigneeUsernames, assigneeWildcardId, authorUsername, closedAfter/Before, confidential, createdAfter/Before, crmContactId, crmOrganizationId, customField, dueAfter/Before, healthStatusFilter, hierarchyFilters, ids, iid, iids, in, includeDescendantWorkItems, iterationCadenceId, iterationId, iterationWildcardId, labelName, milestoneTitle, milestoneWildcardId, myReactionEmoji, not, or, parentIds, parentWildcardId, relativeDateFilters, releaseTag, releaseTagWildcardId, requirementLegacyWidget, search, sort (WorkItemSort), state (IssuableState), status, subscribed, types ([IssueType!]), updatedAfter/Before, verificationStatusWidget, weight, weightWildcardId, workItemTypeIds`.
  `HierarchyFilterInput`: `parentIds` (max 100), `parentWildcardId` (18.3, `WorkItemParentWildcardId`), `includeDescendantWorkItems`.
  Example from migration guide: `group(fullPath:"gitlab-org"){ workItems(types:[EPIC]){ nodes{ id iid title } } }`.
- `namespace(fullPath){ workItemTypes(name: EPIC){ nodes{ id name } } }` to get `workItemTypeId`.
- Pagination: GraphQL cursor (keyset) pagination everywhere.

### `WorkItem` object (selected fields, all read from objects ref)
`id: WorkItemID!`, `iid: String!` (string, not int), `title`, `titleHtml`, `description`, `descriptionHtml`, `state: WorkItemState!` (`OPEN|CLOSED`), `confidential`, `createdAt/updatedAt/closedAt: Time`, `author: UserCore`, `closedBy`, `webUrl`, `webPath`, `reference(full: Boolean)`, `namespace`, `project`, `workItemType: WorkItemType!`, `widgets: [WorkItemWidget!]` (args `onlyTypes`, `exceptTypes`), `userPermissions`, `lockVersion`, `movedToId`, `archived`, `hidden`, `imported`, `userDiscussionsCount`, `promotedToEpicUrl`, `duplicatedToWorkItemUrl`.
`WorkItemType`: `id, name, iconName, enabled, supportedConversionTypes, widgetDefinitions, canUserCreateItems, supportsMoveAction, ...`.
Widget interface `WorkItemWidget` with `type: WorkItemWidgetType` (values include `HIERARCHY, LINKED_ITEMS, DEVELOPMENT, ASSIGNEES, LABELS, DESCRIPTION, NOTES, ...`).

### `WorkItemWidgetHierarchy` (parent/children)
Fields: `parent: WorkItem`, `hasParent: Boolean!`, `hasChildren: Boolean!`, `children(state: WorkItemState): WorkItemConnection` (all states if omitted), `ancestors: WorkItemConnection`, `depthLimitReachedByType: [{depthLimitReached, workItemType}]` (17.4), `rolledUpCountsByType` (17.3), `type`.
Query:
```graphql
query { namespace(fullPath:"g/p"){ workItem(iid:"10"){ id widgets{ ... on WorkItemWidgetHierarchy { parent{ id title } children{ nodes{ id title state } } hasChildren } } } } }
```
**Allowed types are discoverable:** `WorkItemType.widgetDefinitions { ... on WorkItemWidgetDefinitionHierarchy { allowedParentTypes{nodes{name}} allowedChildTypes{nodes{name}} propagatesMilestone autoExpandTreeOnMove } }`.

### Set / clear parent
`workItemUpdate(input: WorkItemUpdateInput!)` (experiment, 15.1) returns `{ workItem, errors }`.
```graphql
mutation { workItemUpdate(input:{ id:"gid://gitlab/WorkItem/<child>", hierarchyWidget:{ parentId:"gid://gitlab/WorkItem/<parent>" } }){ workItem{ id } errors } }
```
`WorkItemWidgetHierarchyUpdateInput`: `parentId: WorkItemID` (**`null` removes the parent**), `childrenIds: [WorkItemID!]` (max 1000; 50 if flag `work_item_hierarchy_add_children_limits`), `adjacentWorkItemId`, `relativePosition: BEFORE|AFTER` (reorder).
Other mutations: `workItemHierarchyAddChildrenItems(id, childrenIds)` → `{addedChildren, errors}` (18.2); `workItemCreate` takes `hierarchyWidget:{parentId}` (`WorkItemWidgetHierarchyCreateInput`) plus `title!`, `workItemTypeId!`, `namespacePath` (project or group), `descriptionWidget{description}`, `labelsWidget{labelIds}`, `assigneesWidget`, `milestoneWidget`, `linkedItemsWidget{linkType, workItemsIds}`, `developmentWidget`, etc.; `workItemsHierarchyReorder`, `workItemsReorder`, `workItemConvert`, `workItemDelete`, `workItemSubscribe`, `workItemBulkUpdate`, `workItemBulkMove` also exist. Close/reopen: `workItemUpdate(input:{id, stateEvent: CLOSE|REOPEN})`. Update-widget inputs on `workItemUpdate`: `assigneesWidget, descriptionWidget, labelsWidget{addLabelIds,removeLabelIds}, milestoneWidget{milestoneId}, startAndDueDateWidget, statusWidget (17.11), weightWidget, healthStatusWidget, iterationWidget, notesWidget, confidential, title`.
Old `epicTreeReorder` is deprecated in 17.5 "replaced by WorkItem type".

### Which types can parent which
- docs.gitlab.com (child_items, epics, tasks): epic → child epic (Ultimate); epic → issue (Premium); issue → task. Diagram on child_items page: `Parent epic > Child epic > Issue > Task`. Tasks cannot have children; Key results are children of objectives and cannot have children; objectives nest up to 9 levels, ≤100 child OKRs; epics up to 7 levels; max 5000 direct child issues+epics; an item has at most one parent (re-adding moves it).
- **Issue → issue parent/child: not documented on docs.gitlab.com.** The GitLab handbook design doc (https://handbook.gitlab.com/handbook/engineering/architecture/design-documents/work_items/) table: Epic: parent of Epic, Issue / child of Epic. Issue: parent of Task / child of Epic. Task: parent of none / child of Issue. Objective: parent of Objective, Key result / child of Objective. Key result: none / Objective. It also lists issue > issue as a *planned* consideration, not current. Treat issue→issue as unsupported unless `allowedChildTypes` says otherwise. Fixture builders should derive allowed types from `allowedParentTypes/allowedChildTypes`.
- Cross-project: epic can adopt issues from any project incl. "a different group hierarchy" (paste full URL; Guest+ role on the issue's project; viewer permission on the epic). Child epic from another group requires multi-level epics for both groups. **Task ↔ issue must be in the same project** ("The issue and task must belong to the same project." https://docs.gitlab.com/user/tasks/). Moving an issue moves its child items (17.3 GA). Linked items between tasks/OKRs: "in the same project" per tasks/OKR pages; issue links are cross-project.
- Roles: Guest/Planner/Reporter/Developer/Maintainer/Owner for the item's project (and view-permission on parent).

### Linked items (GraphQL)
`workItemAddLinkedItems(input:{id, workItemsIds:[WorkItemID!]! (max 10), linkType: RELATED|BLOCKS|BLOCKED_BY})` → `{message, workItem, errors}`; `workItemRemoveLinkedItems`. Widget `WorkItemWidgetLinkedItems`: `blocked, blockedByCount, blockingCount, linkedItems(filter: WorkItemRelatedLinkType)` → `LinkedWorkItemTypeConnection`.

### Development widget (work item ↔ MR/branch)
`WorkItemWidgetDevelopment`: `closingMergeRequests`(conn of `WorkItemClosingMergeRequest{id, fromMrDescription, mergeRequest}`), `closingMergeRequestsCount`, `relatedMergeRequests` (17.6, single work item per request), `relatedBranches` (branches that mention the item but have no MR), `willAutoCloseByMergeRequest`, `featureFlags`.
Mutations: `workItemAddClosingMergeRequest(id, mergeRequestReference "!1"|"path!1"|URL, contextNamespacePath)` (17.1 experiment); `mergeRequestCreateWorkItemRelations(projectPath, iid, workItemIds, linkType: CLOSES|RELATED)` and `mergeRequestDestroyWorkItemRelations`. `MergeRequestWorkItemLinkType` enum: `CLOSES, MENTIONED (automatic only), RELATED`. Explicit MR↔work-item links GA in 19.6 (flag `explicit_mr_work_item_relations` removed); "Work items" MR sidebar widget (groups Closing/Mentioned) GA 19.0.

---------------------------------------------------------------------------

## 3. Issue ↔ MR

Sources: https://docs.gitlab.com/api/issues/ (`## Merge requests`), https://docs.gitlab.com/api/merge_requests/, https://docs.gitlab.com/user/project/issues/managing_issues/#closing-issues-automatically. Tier: Free (all).

| Endpoint | Meaning |
|---|---|
| `GET /projects/:id/issues/:issue_iid/related_merge_requests` | MRs related to the issue (mentioned/related). Params: `id`, `issue_iid`. Returns array of full MR objects (same shape as MR list, e.g. id, iid, project_id, title, description, state, created_at, updated_at, closed_by, closed_at, target_branch, source_branch, user_notes_count, upvotes …). |
| `GET /projects/:id/issues/:issue_iid/closed_by` | MRs that will close the issue when merged. Same params/shape. Private project/confidential issue needs credentials. |
| `GET /projects/:id/merge_requests/:merge_request_iid/closes_issues` | Issues that would close on merge. With GitLab tracker returns issue objects: assignee(s), author, blocking_issues_count, closed_at, closed_by, confidential, created_at, description, discussion_locked, downvotes, due_date, id, iid, issue_type (`issue,incident,test_case,requirement,task`), labels, merge_requests_count, milestone, project_id, state (`opened|closed`), task_completion_status, time_stats, title, type (uppercase issue_type), updated_at, upvotes, user_notes_count, web_url, weight. With external tracker (Jira) only `id`,`title`. |
| `GET /projects/:id/merge_requests/:merge_request_iid/related_issues` | Issues referenced in MR title, description, commits, comments, discussions (not necessarily closing). Same issue shape. |
All paginated (offset).

**Closing patterns** (docs: `managing_issues` "Default closing pattern"): keywords (case variants of) `Close/Closes/Closed/Closing`, `Fix/Fixes/Fixed/Fixing`, `Resolve/Resolves/Resolved/Resolving`, `Implement/Implements/Implemented/Implementing` + reference. Accepted reference formats: `#123`; cross-project `group/project#123`; full issue URL; full work item URL (`/-/work_items/123`; group-level `/groups/<g>/-/work_items/123`) — work-item (task/objective/key result) refs introduced in 17.3. Lists work: `Closes #4, #6, Related to #5` closes 4 and 6 only. Applies in MR description and commit messages; closing happens when the commit/MR reaches the **default branch**. Not applied for the very first push of an existing repo. Merging user's permission to close the target issues is checked at merge time (for pattern-based links); explicit links (GraphQL / MR work-item widget) are checked at link-creation time and persist. Project setting "Auto-close referenced issues on default branch" (Settings > Repository > Branch defaults) disables it for issues of that project only; MRs in that project can still close other projects' issues. Self-managed admins can change the regex. With auto-merge on, the closing-issues list can no longer change. Cross-project closing: supported by pattern (see above).
- Branch name prefix `123-…` (same project only) links branch/MR to issue/task, adds `Closes #123` to MR description, copies milestone & labels when MR is same-project non-fork (https://docs.gitlab.com/user/project/repository/branches/#prefix-branch-names-with-a-number).

---------------------------------------------------------------------------

## 4. Branches / "create branch from issue"

Source: https://docs.gitlab.com/api/branches/ · Free.
- `POST /projects/:id/repository/branches` params: `branch` (required; "cannot contain spaces or special characters except hyphens and underscores" per doc), `ref` (required; branch name or commit SHA). 201 → branch object: `name, merged, protected, default, developers_can_push, developers_can_merge, can_push, web_url, commit{id, short_id, created_at, parent_ids, title, message, author_name, author_email, authored_date, committer_name, committer_email, committed_date, trailers, extended_trailers, web_url}`.
- `GET /projects/:id/repository/branches` (params `search` with `^term`/`term$`, `regex` re2; offset-paged), `GET /projects/:id/repository/branches/:branch` (URL-encoded name), DELETE branch (not read).
- GraphQL `createBranch(input:{projectPath, name, ref})` → `{branch, errors}`.
- **No REST/GraphQL "create branch from issue" or "create MR from issue" endpoint is documented.** The UI flow ("Create merge request and branch", base: project's branch-name template, 17.8 for tasks, "existing branch from issue" 17.11) is UI-only. Reproduce via API: create a branch named `<issue-iid>-slug` (REST above; this prefix links it to the issue if same project), then `POST /projects/:id/merge_requests` with description `Closes #<iid>`; or link explicitly via `mergeRequestCreateWorkItemRelations` (CLOSES). The only issue-related MR-create hook in REST: Create-issue params `merge_request_to_resolve_discussions_of` and `discussion_to_resolve` (issue from MR threads, the reverse direction). `[INFERENCE]` for the recommended recipe. Empty repo: UI creates default branch + README.

---------------------------------------------------------------------------

## 5. Merge requests

Source: https://docs.gitlab.com/api/merge_requests/ (Free, Premium, Ultimate). Offset-paginated.

### List
`GET /merge_requests` (global), `GET /projects/:id/merge_requests`, `GET /groups/:id/merge_requests`. Defaults: only MRs created by current user unless `scope=all`; `state` default `all` per doc table. Key params:
- `scope`: `created_by_me` (default) | `assigned_to_me` | `reviews_for_me` (reviewer is current user) | `all`.
- `state`: `opened|closed|locked|merged|all` (`locked` is transient, usually empty).
- `reviewer_id` (int, or `None`/`Any`) / `reviewer_username` (mutually exclusive); `assignee_id` (`None|Any`) / `assignee_username[]`; `author_id` / `author_username`; `approved_by_ids[]`/`approved_by_usernames[]`/`approver_ids[]` (approver_ids Premium+).
- `draft` (boolean; **introduced 19.0**) — replaces deprecated `wip=yes|no`. Mutually exclusive.
- `updated_after/updated_before/created_after/created_before/merged_after/merged_before` ISO 8601; `source_branch`, `target_branch`, `labels` (`None|Any`), `milestone`, `my_reaction_emoji`, `search` + `in` (`title,description`), `not{labels,milestone,author_id,author_username,assignee_id,assignee_username,reviewer_id,reviewer_username,my_reaction_emoji}`, `order_by` (`created_at|updated_at|merged_at(17.2)|label_priority|priority|milestone_due|popularity|title`), `sort`, `view=simple`, `with_labels_details`, `with_merge_status_recheck`, `non_archived`, `environment`, `deployed_after/before`.
- Gotcha: list does not proactively refresh `merge_status`/`has_conflicts`; pass `with_merge_status_recheck=true` (ignored for users below Developer). `409/408/429` possible (`search` is rate-limited 429; DB timeout 408).
- `scope=reviews_for_me`+`state=opened` is the REST way to get "MRs awaiting my review".

### Get single
`GET /projects/:id/merge_requests/:merge_request_iid` params: `include_diverged_commits_count`, `include_rebase_in_progress`, `render_html`. Mergeability is checked asynchronously — poll.
Fields worth modelling: `id, iid, project_id, title, description, state (opened|closed|merged|locked), draft, work_in_progress (deprecated), created_at, updated_at, merged_at, closed_at, prepared_at, merge_after (17.8), target_branch, source_branch, source_project_id, target_project_id, author{bot(19.5), id,name,username,state,avatar_url,web_url}, assignees[], reviewers[], merge_user (merged_by deprecated 14.7), labels[], milestone, upvotes, downvotes, user_notes_count, web_url, references{short,relative,full}, sha, merge_commit_sha, squash_commit_sha, squash, squash_on_merge, should_remove_source_branch, force_remove_source_branch, discussion_locked, merge_when_pipeline_succeeds (=auto-merge set), merge_status (deprecated 15.6), detailed_merge_status, has_conflicts, blocking_discussions_resolved, changes_count (STRING, "1000+" cap, empty on create), diff_refs{base_sha,head_sha,start_sha} (empty on create), diverged_commits_count, rebase_in_progress, merge_error, latest_build_started_at/finished_at, first_deployed_to_production_at, first_contribution, pipeline (use head_pipeline), head_pipeline{id,iid,project_id,sha,ref,status,source,web_url,created_at,updated_at,started_at,finished_at,committed_at,duration,queued_duration,coverage,before_sha,tag,yaml_errors,user,detailed_status{icon,text,label,group,tooltip,has_details,details_path,favicon,illustration,action}} (only if user can view pipelines), user{can_merge: bool}, task_completion_status{count,completed_count}, time_stats, subscribed, approvals_before_merge (deprecated 16.0, Premium), allow_collaboration, imported, imported_from`.
Trimmed real example (from docs):
```json
{"has_conflicts": false, "blocking_discussions_resolved": true, "changes_count": "1",
 "head_pipeline": {"id": 538317940, "iid": 1877, "project_id": 15513260, "status": "success", "...": "..."},
 "diff_refs": {"base_sha":"1162f7...","head_sha":"e82eb4...","start_sha":"1162f7..."},
 "merge_error": null, "first_contribution": false, "user": {"can_merge": true}}
```
**`detailed_merge_status` values** (https://docs.gitlab.com/api/merge_requests/#merge-status): `approvals_syncing, checking, ci_must_pass, ci_still_running, commits_status, conflict, discussions_not_resolved, draft_status, jira_association_missing, mergeable, merge_request_blocked, merge_time, need_rebase, not_approved, not_open, preparing, requested_changes, security_policy_pipeline_check, security_policy_violations, status_checks_must_pass, unchecked, locked_paths, locked_lfs_files, title_regex`. (GraphQL enum `DetailedMergeStatus` uses upper-case variants, e.g. `BLOCKED_STATUS`, `EXTERNAL_STATUS_CHECKS`, `JIRA_ASSOCIATION` — differ in naming from REST.) Legacy `merge_status`: `unchecked|checking|can_be_merged|cannot_be_merged|cannot_be_merged_recheck`. `has_conflicts` is false unless `merge_status=cannot_be_merged`.
Empty-field gotcha: right after create, `diff_refs` and `changes_count` are empty and populate asynchronously; `prepared_at` set once diff, pipelines, mergeability, LFS link, notifications done.

### Create / draft
`POST /projects/:id/merge_requests` — required `source_branch`, `target_branch`, `title`; optional `target_project_id` (fork → upstream), `description`, `assignee_id`, `assignee_ids`, `reviewer_ids`, `labels` (comma string; auto-creates), `milestone_id`/`milestone` (exclusive), `remove_source_branch`, `squash`, `allow_collaboration`, `merge_after` (17.8). **There is no `draft` param on create or update in the REST docs.** Draft is set by prefixing the title with `Draft:`, `[Draft]` or `(Draft)` (user doc https://docs.gitlab.com/user/project/merge_requests/drafts/); mark-ready = update `title` without the prefix (`PUT .../merge_requests/:iid` with `title`), or post note `/ready`/`/draft` quick actions, or GraphQL `mergeRequestSetDraft(input:{projectPath, iid, draft: Boolean!})` → `{mergeRequest, errors}`. Response `draft: true` and `work_in_progress` deprecated alias. Draft MRs can't merge (`detailed_merge_status=draft_status`), run same pipelines as ready ones. Commits starting `draft:`/`fixup!` also mark draft (non-toggle).
Update: `PUT /projects/:id/merge_requests/:merge_request_iid` (at least one optional field): `title, description, target_branch, assignee_id(s), reviewer_ids, labels, add_labels, remove_labels, milestone_id, state_event (close|reopen), remove_source_branch, squash, discussion_locked, allow_collaboration, merge_after`.
GraphQL alternatives: `mergeRequestCreate`, `mergeRequestUpdate`, `mergeRequestSetReviewers/Assignees/Labels/Locked`, `mergeRequestAccept`, `mergeRequestRequestChanges`, `mergeRequestDestroyRequestedChanges`, `mergeRequestReviewerRereview`.

### Merge
`PUT /projects/:id/merge_requests/:merge_request_iid/merge` params: `auto_merge` (bool; "merges when checks pass"), `merge_when_pipeline_succeeds` (**deprecated 17.11**, use `auto_merge`), `sha` (must equal source HEAD; **required if the group/instance "Require a commit SHA on the merge requests API" setting is on, introduced 19.2**), `squash`, `squash_commit_message`, `merge_commit_message`, `should_remove_source_branch`. Errors: `400 SHA must be provided when merging`, `401` (no permission), `405` (cannot merge now), `409 SHA does not match HEAD of source branch`, `422 Branch cannot be merged`. 19.1+: `auto_merge` on projects with merge trains adds MR to the train instead of merging directly. Returns the MR object. Cancel auto-merge: `POST .../cancel_auto_merge` (19.5; 201 returns MR; 403/409); old `POST .../cancel_merge_when_pipeline_succeeds` deprecated 19.5.
Other: a "Merge to default merge ref path" endpoint exists in the same doc (path not recorded here; not read in detail).

### Rebase
`PUT /projects/:id/merge_requests/:merge_request_iid/rebase` param `skip_ci`. **202** `{"rebase_in_progress": true}`; poll `GET .../merge_requests/:iid?include_rebase_in_progress=true` → `{"rebase_in_progress": false, "merge_error": null | "Rebase failed. Please rebase locally"}`. Errors 403 (cannot push / source branch missing / protected from force push), 409 (failed to enqueue), 429 (rate limit 30/min/user; rate limit feature flag `merge_requests_rebase_rate_limit` 19.6, off by default).

---------------------------------------------------------------------------

## 6. MR discussions, notes and draft notes

Source: https://docs.gitlab.com/api/discussions/ , https://docs.gitlab.com/api/notes/ , https://docs.gitlab.com/api/draft_notes/. All **Free**. Offset-paginated (default 20).

**Discussions**
- `GET /projects/:id/merge_requests/:merge_request_iid/discussions` and `.../discussions/:discussion_id` (discussion_id is a string SHA).
- Response: `[{id (string), individual_note (bool), notes:[{id, type ("DiscussionNote"|"DiffNote"|null), body, attachment, author{id,name,username,state,avatar_url,web_url}, created_at, updated_at, system, noteable_id, noteable_type "MergeRequest", project_id, noteable_iid, commit_id, resolved, resolvable, resolved_by, resolved_at, position{...}, suggestions[{id,from_line,to_line,appliable,applied,from_content,to_content}]}]}]`.
  DiffNote example `position`:
```json
"position": {"base_sha":"b5d6e7b1…","start_sha":"7c9c2ead…","head_sha":"4803c71e…","old_path":"package.json","new_path":"package.json","position_type":"text","old_line":27,"new_line":27,
  "line_range":{"start":{"line_code":"588440f6…_10_10","type":"new","old_line":null,"new_line":10},"end":{"line_code":"588440f6…_11_11","type":"old","old_line":11,"new_line":11}}}
```
- Create thread: `POST /projects/:id/merge_requests/:merge_request_iid/discussions` — `body` (required), `commit_id`, `created_at` (admin/owner), `position[...]`. 201 returns the discussion.
  - `position[position_type]` (required if position): `text|image|file`; `position[base_sha]`, `position[head_sha]`, `position[start_sha]` required (get from `GET .../merge_requests/:iid/versions`, newest first; use `base_commit_sha, head_commit_sha, start_commit_sha`); text: `position[new_path]`+`position[old_path]` required; `position[new_line]` only → added line; `position[old_line]` only → removed line; **both** → unchanged line (numbers can differ); image: `width, height, x, y`; multi-line: `position[line_range][start|end][line_code|type|old_line|new_line]`; `type` `new|old` or omit for unchanged. `line_code` = `<sha1(filename)>_<old>_<new>`. Wrong SHAs can hit bug https://gitlab.com/gitlab-org/gitlab/-/issues/296829. Overview (non-diff) thread = omit `position`.
- Reply: `POST .../discussions/:discussion_id/notes` — `body` (required) (400 on system notes). Edit: `PUT .../discussions/:discussion_id/notes/:note_id` with **exactly one of** `body` or `resolved`. Delete: `DELETE .../notes/:note_id`.
- **Resolve/unresolve a thread:** `PUT /projects/:id/merge_requests/:merge_request_iid/discussions/:discussion_id` with `resolved=true|false` (required); needs Developer+ or being the MR author; 200 returns discussion. GraphQL: `discussionToggleResolve(input:{id: DiscussionID!, resolve: Boolean!})`; `createDiffNote`, `createNote`, `createDiscussion` also exist.
- Plain MR comment (no thread): `POST /projects/:id/merge_requests/:merge_request_iid/notes` params `body` (≤1,000,000 chars), `internal`, `created_at`, `merge_request_diff_head_sha` (required for `/merge` quick action). `PUT/DELETE .../notes/:note_id`.

**Draft notes (batch review)** — tier **Free, Premium, Ultimate** (page tier). Visible only to author until published.
| Op | Path |
|---|---|
| list | `GET /projects/:id/merge_requests/:merge_request_iid/draft_notes` |
| get | `GET .../draft_notes/:draft_note_id` |
| create | `POST .../draft_notes` — `note` (req), `commit_id`, `in_reply_to_discussion_id`, `resolve_discussion`, `position[...]` (same sub-fields as discussions) |
| update | `PUT .../draft_notes/:draft_note_id` — `note`, `position` |
| delete | `DELETE .../draft_notes/:draft_note_id` |
| publish one | `PUT .../draft_notes/:draft_note_id/publish` |
| publish all | `POST .../draft_notes/bulk_publish` — optional `note` (summary), `internal`, `reviewer_state` (`requested_changes`\|`reviewed`; sets review state, **not** a formal approval) |
Draft note object (docs show array even for single GET): `{id, author_id, merge_request_id, resolve_discussion, discussion_id, note, commit_id, line_code, position{base_sha,start_sha,head_sha,old_path,new_path,position_type,old_line,new_line,line_range}}`. Note body may be normalized on publish. Response codes for create/publish not documented in the page.

---------------------------------------------------------------------------

## 7. MR approvals

Source: https://docs.gitlab.com/api/merge_request_approvals/ ; user doc https://docs.gitlab.com/user/project/merge_requests/approvals/.
**Free (all tiers):** `POST /projects/:id/merge_requests/:merge_request_iid/approve` (optional `sha` — must equal HEAD, else **409**; `approval_password` when re-auth required), `POST .../unapprove` (removes current user's approval), `PUT .../reset_approvals` (bot/project-or-group token only; humans get 401), `GET .../approvals`. Free allows Developer+ users to approve; approvals are optional and never block merge. approve/unapprove return **201** + approval-state object.
**Premium/Ultimate:** `GET /projects/:id/merge_requests/:merge_request_iid/approval_state` (rules detail), MR approval-rule CRUD (`/approval_rules`), project approvals config (`GET/POST /projects/:id/approvals`), project approval rules (`/projects/:id/approval_rules`), group rules. Required approvals/rules/code-owner rules = Premium+.
`GET .../approvals` response (EE attributes returned "with or without a license", incl. GitLab.com; CE returns only `approved, approved_by, user_can_approve, user_has_approved`):
```json
{"id":5,"iid":5,"project_id":1,"title":"Approvals API","state":"opened","merge_status":"can_be_merged",
 "approved":true,"approvals_required":2,"approvals_left":0,"require_password_to_approve":false,
 "approved_by":[{"user":{"id":1,"username":"root","name":"Administrator","state":"active","avatar_url":"…","web_url":"…"},"approved_at":"2016-06-10T04:21:41.050Z"}],
 "suggested_approvers":[],"approvers":[{"user":{…}}],"approver_groups":[],
 "user_has_approved":true,"user_can_approve":false,"approval_rules_left":[],"has_approval_rules":true,
 "merge_request_approvers_available":true,"multiple_approval_rules_available":true,"invalid_approvers_rules":[]}
```
Semantics: `approved` on EE = rules satisfied (true when no rules even if `approved_by` empty); on CE = at least one approval. `approvers`, `approver_groups`, `require_password_to_approve` are deprecated (use `/approval_state`, `require_reauthentication_to_approve`). `approval_rules_left[].rule_type`: `any_approver|code_owner|regular|report_approver`.
`GET .../approval_state` (Premium+): `{"approval_rules_overwritten":true,"rules":[{"id":1,"name":"Ruby","rule_type":"regular","eligible_approvers":[{user}],"approvals_required":2,"users":[{user}],"groups":[],"contains_hidden_groups":false,"approved_by":[{user}],"source_rule":null,"approved":true,"overridden":false}]}`; `approved_by` there excludes users that satisfy no rule.
Gotchas: path params must be single values; new commits reset approvals by default — wait until `detailed_merge_status` not in `checking|approvals_syncing` and the diff `patch_id_sha` non-null, or send `sha` with the approve call. GraphQL review states (`MergeRequestReviewState`): `APPROVED, REQUESTED_CHANGES, REVIEWED, REVIEW_STARTED, UNAPPROVED, UNREVIEWED`. REST has no endpoint for request-changes except `bulk_publish?reviewer_state=requested_changes`; GraphQL `mergeRequestRequestChanges`.

---------------------------------------------------------------------------

## 8. MR diffs and versions

Source: https://docs.gitlab.com/api/merge_requests/ ; Free.
- **Preferred:** `GET /projects/:id/merge_requests/:merge_request_iid/diffs` — params `page` (default 1), `per_page` (default 20), `unidiff` (bool). Offset-paginated; subject to instance diff limits (over-limit MRs return limited results). Response array items: `old_path, new_path, a_mode, b_mode, diff, collapsed, too_large, new_file, renamed_file, deleted_file, generated_file`. `collapsed`/`too_large` added in **18.4** (collapsed: diff omitted but fetchable; too_large: cannot be fetched). Example: `{"old_path":"README","new_path":"README","a_mode":"100644","b_mode":"100644","diff":"@@ -1 +1 @@\n-Title\n+README","collapsed":false,"too_large":false,"new_file":false,"renamed_file":false,"deleted_file":false,"generated_file":false}`.
- **Deprecated:** `GET .../merge_requests/:iid/changes` — deprecated 15.7, scheduled removal in API v5; returns MR object plus `changes[]` (all in one response, not paginated). Use `/diffs`.
- `GET .../merge_requests/:iid/raw_diffs` — plain unified diff text for the whole MR (not JSON).
- `GET .../merge_requests/:iid/versions` — diff versions, newest first: `[{id, head_commit_sha, base_commit_sha, start_commit_sha, created_at, merge_request_id, state ("collected"), real_size (string), patch_id_sha}]`. `GET .../versions/:version_id` returns one version (adds `commits` and `diffs` per docs section "Retrieve a merge request diff version"; not read in full). Position SHAs map: `base_commit_sha`→`position[base_sha]`, `head_commit_sha`→`head_sha`, `start_commit_sha`→`start_sha`.
- `GET .../merge_requests/:iid/commits` lists MR commits.

---------------------------------------------------------------------------

## 9. Pipelines and jobs

Sources: https://docs.gitlab.com/api/pipelines/ , https://docs.gitlab.com/api/jobs/ , https://docs.gitlab.com/api/merge_requests/. Free.

**MR pipelines**
- `GET /projects/:id/merge_requests/:merge_request_iid/pipelines` → `[{id, sha, ref, status}]` (minimal; offset `page/per_page`). Example `{"id":77,"sha":"959e04…","ref":"main","status":"success"}`.
- `POST .../merge_requests/:iid/pipelines` → creates a merge-request (detached or merged-results) pipeline; returns a pipeline object (`id, sha, ref "refs/merge-requests/1/head", status, web_url, before_sha, tag, yaml_errors, user, created_at, updated_at, started_at, finished_at, committed_at, duration, coverage, detailed_status, archived`). Needs `.gitlab-ci.yml` configured for MR pipelines.
- MR object `head_pipeline` (§5) is the pipeline for source branch HEAD.

**Pipeline status values** (project pipelines `status` filter): `created, waiting_for_resource, preparing, waiting_for_callback, pending, running, success, failed, canceling, canceled, skipped, manual, scheduled`. GraphQL `PipelineStatusEnum` is the same set upper-cased.
- `GET /projects/:id/pipelines` filters: `status, ref, sha, scope (running|pending|finished|branches|tags), source, name, username, yaml_errors, order_by (id|status|ref|updated_at|user_id), sort, created/updated_after/before`. List items: `id, iid, project_id, status, source, ref, sha, name, web_url, created_at, updated_at`. Child pipelines excluded unless `source=parent_pipeline` (17.0). Offset paginated.
- `GET /projects/:id/pipelines/:pipeline_id` full object: `id, iid, project_id, name, sha, ref, status, source, created_at, updated_at, web_url, before_sha, tag, yaml_errors, user, started_at, finished_at, committed_at, duration, queued_duration, coverage, detailed_status{icon,text,label,group,tooltip,has_details,details_path,illustration,favicon}`.
- Global `GET /pipelines` (19.3; pipelines triggered by auth user; **keyset only**, `cursor`, 100 max) with `merge_request{iid,title,web_url}` and `project` (19.4).
- Retry: `POST /projects/:id/pipelines/:pipeline_id/retry` — retries failed/canceled jobs; no-op if none; returns pipeline object (`status: pending` etc.); `429` rate limit; `409` if retry already in progress (19.5 flag, GA 19.6). Cancel: `POST .../pipelines/:id/cancel`.

**Jobs**
- `GET /projects/:id/pipelines/:pipeline_id/jobs` — params `scope` (string or array of statuses), `include_retried` (default false; retried jobs excluded by default); includes child pipelines; sorted ID desc; offset pagination.
- `GET /projects/:id/jobs` — `scope`; offset or keyset (keyset recommended); ID desc.
- `GET /projects/:id/jobs/:job_id`.
- Job status values: `canceled, canceling, created, failed, manual, pending, preparing, running, scheduled, skipped, success, waiting_for_callback, waiting_for_resource`.
- Job object fields: `id, name, stage, status, failure_reason (e.g. script_failure, stuck_or_timeout_failure), ref, tag, source, allow_failure, archived, created_at, started_at, finished_at, erased_at, duration (float seconds), queued_duration, coverage, web_url, commit{id,short_id,title,message,author_name,author_email,created_at}, pipeline{id,project_id,ref,sha,status}, runner{...}|null, runner_manager|null, artifacts[{file_type,size,filename,file_format}], artifacts_file{filename,size}, artifacts_expire_at, tag_list, user{...}, project{ci_job_token_scope_enabled}`.
- **Job log:** `GET /projects/:id/jobs/:job_id/trace` — returns the raw log as plain text (follows redirects: use `--location`); 200 serves the log, **404 if job not found OR no log**. Not paginated.
- Retry job: `POST /projects/:id/jobs/:job_id/retry` (optional `job_inputs` hash, 18.10; 429 with Retry-After on rate limit) returns the new job object. Cancel: `POST .../jobs/:job_id/cancel`. Run manual job: `POST .../jobs/:job_id/play` (`job_inputs` 18.10). Erase: `POST .../jobs/:job_id/erase`.

---------------------------------------------------------------------------

## 10. Things to verify against a real instance (docs gaps)
1. Whether `blocks`/`is_blocked_by` on Free returns 4xx and which code (docs only say Premium for "Blocking issues" in user docs).
2. Whether `GET .../links` reports `link_type` relative to the queried issue for reverse links.
3. Issue→issue parent/child (not in docs; handbook says not currently allowed).
4. HTTP status codes for issue-link delete, draft note create/publish, discussion resolve.
5. Exact structure of `GET .../versions/:version_id` and `WorkItem` pagination of `children` when >100.
6. `Namespace.workItem(iid:)` schema entry (only seen in migration guide examples).
