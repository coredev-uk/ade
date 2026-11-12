#!/usr/bin/env bash
# Round 3 acceptance: no waiting, fresh data. Run from the repo root inside
# `nix develop`. The request-count part needs .env with GITLAB_PAT (GitLab
# sandbox group ade-demo-env, Free tier); it makes one `ade sync` (about 30
# requests of the 100 per minute the account is allowed).
#
# Measured on the sandbox with `ade --debug-http sync` (first sync of a clean
# cache, 13 items, 9 open MRs):
#   before this round: 61 requests (4 REST calls per open MR)
#   after:             31 requests (the MR detail is 2 GraphQL requests, one
#                      per project; what is left is the issue lists and one
#                      links call per tracker issue)
# The threshold below leaves room for the sandbox to grow by a few issues.
. scripts/verify/lib.sh

MAX_SYNC_REQUESTS=40

step "batched merge-request reads (GraphQL query validated against the committed schema)"
check "query validates; batch equals MR on both tiers; REST fallback; chunking; unlicensed approvals" \
  go test ./internal/forge/gitlab/ -run 'TestQueriesMatchSchema|TestEmbeddedQueryIsValidated|TestMRDetails'
check "rate-limit budget shared between clients; sync keeps a reserve; Retry-After-only servers unpaced" \
  go test ./internal/forge/gitlab/ -run 'TestBackgroundRequestsLeaveAReserve|TestRateLimitGateIsSharedBetweenClients|TestNoRateHeadersNoPacing|TestRateLimit'

step "one-item refresh and dialogs that never wait for a sync"
check "forgesync: a refreshed item equals the synced one, shows a new child and MR, adds an uncached item" \
  go test ./internal/forgesync/ -run 'Refresh'
check "weight/startwork: identity and capabilities from the cache; closed-issue history never awaited" \
  go test ./internal/weight/... ./internal/startwork/...
check "TUI: a dialog opens in < 1s while a slow sync runs (fake forge with latency)" \
  go test ./internal/tui/ -run 'TestDialogsOpenWhileASlowSyncRuns' -count=1
check "TUI: classify/weight/start work refresh one item, not a full sync; start work writes the cache" \
  go test ./internal/tui/ -run 'TestClassifyApplyRefreshesOnlyItsItem|TestStartWorkWritesTheItemToTheCache|TestWeightSuggestionUsesClosedIssuesFromTheCache|TestNotesDialog' -count=1

step "fsnotify, with a poll fallback"
check "watch: signal within 1s, bursts coalesce, missing directory, forced poll fallback" go test ./internal/watch/... -count=1
check "overview updates within 1s of a cache write (fsnotify and poll fallback)" \
  go test ./internal/overview/ -run 'TestOverviewFollowsACacheWriteWithinASecond' -count=1
check "dashboard follows a cache write by another process within 1s; the tick no longer reads files" \
  go test ./internal/tui/ -run 'TestCacheWriteByAnotherHandleUpdatesTheDashboardWithinASecond|TestTickDoesNotPollTheCacheOrTheAuditLog' -count=1
check "header AI spend updates right after a call" go test ./internal/tui/ -run 'TestAIChipUpdatesRightAfterACall' -count=1

step "request count per sync on the live sandbox (GraphQL batch, threshold ${MAX_SYNC_REQUESTS})"
build_ade
sandbox_gitlab
"$ADE" --debug-http sync >"$VERIFY_TMP/sync.out" 2>"$VERIFY_TMP/http.log" || {
  cat "$VERIFY_TMP/http.log" >&2
  fail "ade sync failed"
}
REQS=$(grep -c '^http →' "$VERIFY_TMP/http.log" || true)
PER_MR=$(grep '^http →' "$VERIFY_TMP/http.log" | grep -cE 'merge_requests/[0-9]+(/(discussions|reviewers))?( |\?|$)' || true)
GQL=$(grep '^http →' "$VERIFY_TMP/http.log" | grep -c 'POST .*graphql' || true)
printf '  sync: %s requests (%s GraphQL, %s per-MR REST reads)\n' "$REQS" "$GQL" "$PER_MR"
[[ $REQS -gt 0 && $REQS -le $MAX_SYNC_REQUESTS ]] && pass "$REQS requests ≤ $MAX_SYNC_REQUESTS" || fail "$REQS requests, want 1..$MAX_SYNC_REQUESTS"
[[ $PER_MR -eq 0 ]] && pass "no per-MR REST detail reads (merge request, discussions, reviewers)" || fail "$PER_MR per-MR REST reads"
[[ $GQL -ge 2 ]] && pass "MR detail went through GraphQL" || fail "no GraphQL MR detail request"
"$ADE" items --json >"$VERIFY_TMP/items.json"
expect_json "the batched sync still reads tracker#1: MR api!1 with 2 unresolved threads in review" "$VERIFY_TMP/items.json" \
  '[.[] | select(.ref == "ade-demo-env/tracker#1")] | .[0] | (.mrs[0].ref == "ade-demo-env/api!1") and .mrs[0].unresolved == 2 and .court == "you"'

printf '\nround 3 freshness: all checks passed\n'
