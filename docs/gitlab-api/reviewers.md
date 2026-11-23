# GitLab reviewers API: list, add, re-request

Scope: what `ade mr request-review` and the MR page's `v` call. Built from docs.gitlab.com on 2026-11-23 (the docs describe GitLab 19.x) and from the committed `internal/forge/gitlab/schema.graphql`. `[INFERENCE]` marks anything the docs did not state outright. All of it is Free tier: reviewers are not a licensed feature (the user doc for requesting a review lists no tier).

## List reviewers and their verdicts

`GET /projects/:id/merge_requests/:merge_request_iid/reviewers`. Source: https://docs.gitlab.com/api/merge_requests/#retrieve-merge-request-reviewers.

- Params: `id` (ID or URL-encoded path), `merge_request_iid`.
- Response: array of `{ "user": {id, name, username, state, avatar_url, web_url}, "state": "unreviewed", "created_at": "…" }`. The docs' example shows `unreviewed`; the values are the GraphQL `MergeRequestReviewState` enum in lower case (https://docs.gitlab.com/api/graphql/reference/enums/): `unreviewed`, `review_started`, `reviewed`, `approved`, `requested_changes` (the enum also has `UNAPPROVED`) `[INFERENCE: REST spelling]`.
- ADE reads it with a 404/403 meaning "this instance has no such endpoint" (`FeatureReviewers`, GitLab 14.0 floor): the MR's own `reviewers[]` then stand in, all as requested.
- ADE's states: `unreviewed` → requested, `review_started` → reviewing, `reviewed` → commented, `approved`, `requested_changes` → changes requested.

## Add reviewers

`PUT /projects/:id/merge_requests/:merge_request_iid` with `reviewer_ids` (integer array). Source: https://docs.gitlab.com/api/merge_requests/#update-mr.

- "The ID of the users set as a reviewer to the merge request. Set the value to `0` or provide an empty value to unset all reviewers." So the array **replaces** the set: ADE sends the MR's current reviewer ids (from `GET …/merge_requests/:iid`, field `reviewers[].id`) plus the new ids, so nobody is dropped.
- A user who is in the set already and is sent again is **not** re-requested by this call `[INFERENCE: the docs describe a set, not a request]`. That is why existing reviewers go through the mutation below.

## Resolve a username to an id

`GET /users?username=:username`. Source: https://docs.gitlab.com/api/users/#list-users ("you can lookup users by username"; "Username search is case-insensitive"). Returns an array; ADE takes the entry whose username equals the one asked for (ignoring case), and fails with "no user" when there is none.

## Re-request a review (an existing reviewer)

REST has **no** endpoint for it (the merge request API pages list none). The user doc (https://docs.gitlab.com/user/project/merge_requests/reviews/#re-request-a-review) says the author uses the `/request_review @user` quick action or the "Re-request a review" icon next to the reviewer; "GitLab creates a new to-do item for the reviewer, and sends them a notification email." The GraphQL equivalent is the mutation:

```graphql
mutation ReviewerRereview($projectPath: ID!, $iid: String!, $userId: UserID!) {
  mergeRequestReviewerRereview(input: {projectPath: $projectPath, iid: $iid, userId: $userId}) {
    errors
    mergeRequest { iid }
  }
}
```

- Source: https://docs.gitlab.com/api/graphql/reference/mutations/ (`mergeRequestReviewerRereview`). Input: `projectPath: ID!` (the project), `iid: String!`, `userId: UserID!` ("User ID for the user that has been requested for a new review"), `clientMutationId`, and a Duo Code Review option ADE leaves unset. Payload: `errors: [String!]!`, `mergeRequest`.
- `UserID` is a global id: `gid://gitlab/User/<id>`.
- The document is `internal/forge/gitlab/queries/reviewer_rereview.graphql` and `TestQueriesMatchSchema` validates it against the committed schema. The docs do not say which release introduced the mutation, nor what the reviewer's state becomes; `[INFERENCE]` that it goes back to `unreviewed`, which is what the UI's re-request does. The fake serves it that way.
- An instance whose schema lacks the mutation answers a GraphQL validation error; ADE reports "this instance cannot re-request a review" (`forge.ErrUnavailable`) and has written nothing for that user. It does not fall back to the quick action, which would post a note.
- A user who is not a reviewer of the MR gets an entry in `errors` `[INFERENCE: wording not documented]`. ADE only sends the mutation for users it saw in the MR's reviewer set.
- The mutation is a write: it goes out with the write token.

## Sources

https://docs.gitlab.com/api/merge_requests/ · https://docs.gitlab.com/api/users/ · https://docs.gitlab.com/api/graphql/reference/mutations/ · https://docs.gitlab.com/user/project/merge_requests/reviews/ · https://docs.gitlab.com/user/project/quick_actions/ (`/request_review`, alias `/assign_reviewer`: "Assign one or more users as reviewers, or request a new review from existing reviewers.")
