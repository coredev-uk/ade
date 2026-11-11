# Fixture sources

Every fixture is hand-authored from the examples and attribute lists in GitLab's public documentation, as compiled in `docs/gitlab-api/`. **No fixture is captured from a real instance.** Ids, names and URLs are the docs' own placeholders. Where a fixture departs from its doc example, the departure is listed here.

`free/` is what an unlicensed instance sends: no `weight`, `iteration` or `epic` keys; EE approval attributes absent (CE shape). `premium/` adds the licensed fields.

| Fixture | Doc page | Note |
|---|---|---|
| `free/issue.json` | <https://docs.gitlab.com/api/issues/> (list/single issue example) | The documented example with `task_completion_status` set to 10/15 to match its own `task_status` text. |
| `premium/issue.json` | <https://docs.gitlab.com/api/issues/> (the Premium/Ultimate `weight`, `epic` and `iteration` snippets) | The three snippets merged into one issue; the iteration is the docs' cadence-generated one (`title: null`, integer `state`). Closed, to exercise `closed_at`. |
| `premium/issue_unweighted.json` | <https://docs.gitlab.com/api/issues/> (`weight`: "integer or null") | Authored: an issue on a licensed instance with `weight`, `epic` and `iteration` all `null`. |
| `free/issue_links.json` | <https://docs.gitlab.com/api/issue_links/> (list issue relations) | The documented example without the `weight` key, which Free omits. |
| `premium/issue_links.json` | <https://docs.gitlab.com/api/issue_links/> | The documented example, with `link_type` changed to `blocks` (a Premium link type) and `weight: null` kept. |
| `free/group_milestones.json` | <https://docs.gitlab.com/api/group_milestones/> | The documented group example, plus a second closed milestone with null dates (the docs: "either may be null"). |
| `free/merge_request.json` | <https://docs.gitlab.com/api/merge_requests/> (get single MR; the trimmed real example in `workflow.md` §5) | Fields the driver reads filled in from the documented attribute list (`draft`, `reviewers`, `detailed_merge_status`, `references`, `sha`); `head_pipeline` from the docs' example. |
| `free/merge_request_pre19.json` | <https://docs.gitlab.com/api/merge_requests/> (`work_in_progress`, deprecated; `pipeline`) | Authored from the attribute list: an MR as an older instance sends it (draft spelled `work_in_progress`, no `head_pipeline`). |
| `free/merge_request_approvals.json` | <https://docs.gitlab.com/api/merge_request_approvals/> (get approvals) | The CE shape the docs describe ("CE returns only `approved`, `approved_by`, `user_can_approve`, `user_has_approved`"): no `approvals_required`. |
| `premium/merge_request_approvals.json` | <https://docs.gitlab.com/api/merge_request_approvals/> | The documented EE example, `approvals_required: 2`. |
| `free/discussions.json` | <https://docs.gitlab.com/api/discussions/> (list MR discussions) | One overview thread with a reply, one `DiffNote` thread with the documented `position`, and a system note (`individual_note: true`). |
| `free/user.json` | <https://docs.gitlab.com/api/users/#retrieve-the-current-user> | The documented example trimmed to its public fields. |
| `premium/group_iterations.json` | <https://docs.gitlab.com/api/group_iterations/> | The documented titled iteration (its `due_date` moved after `start_date`: the docs' example has them reversed) plus the issue page's cadence-generated one (`title: null`). Both carry the integer `state` the driver must ignore. |
| `free/graphql_capabilities.json` | <https://docs.gitlab.com/api/graphql/> ("query fields return `null` when you lack permission or the feature") | Authored: a gated field is `null` next to a field-level error, the `Epic` work-item type is not listed. |
| `premium/graphql_capabilities.json` | <https://docs.gitlab.com/api/graphql/reference/objects/> (`Group.iterationCadences`, `Group.workItemTypes`) | Authored: a licensed group lists cadences and the `Epic` type. The Premium side is not verified against a licensed instance; `ade doctor --probe` reports it on one. |

GraphQL documents in `../queries/` are validated against `../schema.graphql`, which is a conversion of GitLab.com's public introspection result (see the header of that file).
