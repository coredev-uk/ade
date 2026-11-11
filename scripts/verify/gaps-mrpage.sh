#!/usr/bin/env bash
# Gap round, MR page and review (docs/PLAN.md §6.2, §6.9): the AI summary on
# Overview, thread summary and AI draft reply, the agent review pass (A), the
# agent menu on P, D for delta, and `ade review <ref>` opening a review
# workspace tab.
#
# Run from the repo root inside `nix develop`. Needs .env with GITLAB_PAT, a
# logged-in `omp`, zellij, jq, git and `script` (util-linux).
#
# Real AI spend is one omp call (claude-haiku-4-5, a few thousandths of a
# dollar, capped at $0.05 by budget.call_usd): the MR summary of the sandbox's
# api!2; the repeat is a cache hit at $0. The zellij part runs only in a
# throwaway `ade-test-review` session, deleted at the end.
. scripts/verify/lib.sh

step "unit and fake-server tests"
check "MR page AI: summary + d toggle, cache, ask/off/budget stop, thread summary, draft reply never auto-posted, agent pass, notes, P/A keys, D" \
  go test ./internal/tui/... -run 'TestOverviewShows|TestSummary|TestAIOff|TestAskMode|TestBudgetStop|TestThreadSummary|TestDraftReply|TestAgentReview|TestAgentMenu|TestPatchOf|TestPagerLine|TestD|TestAgentKey|TestMRPage'
check "ai: thread summary job, MR loading and the closed-issue parse" go test ./internal/ai/...
check "ade review <ref>: worktree from the source branch, --no-zellij hint, review layout tab, --dry-run" \
  go test ./internal/cli/... -run 'TestReviewOpen|TestReviewSubcommands'
check "review layout starts no run graph" go test ./internal/workspace/... -run 'TestRenderLayoutOfAReviewTab|TestLayout'

step "tools"
for t in omp zellij jq git script; do
  command -v "$t" >/dev/null && pass "$t on PATH" || fail "$t not found"
done

build_ade
sandbox_gitlab
GRP=ade-demo-env
MR=$GRP/api!2 # rename-bucket-to-token-bucket -> 1-rate-limit-the-export-endpoint

step "live: the MR page summary on $MR (real omp, one paid call, then a free cache hit)"
go test -tags live -run TestLiveMRPageSummary -count=1 -timeout 15m -v ./internal/tui >"$VERIFY_TMP/live.out" 2>&1 || {
  cat "$VERIFY_TMP/live.out" >&2
  fail "live MR page summary"
}
grep -E '^\s+mrpage_live_test.go.*first summary' "$VERIFY_TMP/live.out" | sed 's/^/    /'
spent=$(sed -n 's/.*first summary: \$\([0-9.]*\),.*/\1/p' "$VERIFY_TMP/live.out")
[[ -n $spent ]] && awk -v s="$spent" 'BEGIN { exit !(s > 0 && s <= 0.05) }' && pass "the summary was one paid call of \$$spent (cap \$0.05)" || fail "summary cost '$spent' is not in (0, 0.05]"
grep -q -- '--- PASS: TestLiveMRPageSummary' "$VERIFY_TMP/live.out" && pass "a fresh session showed the same summary from the cache at \$0 (audited ai.summarize, cache_hit)" || fail "no cache hit"

step "ade review <ref>: a worktree and a review tab in a throwaway zellij session"
(
  set -euo pipefail
  # The sandbox config, plus a host that is zellij; its own state directories.
  export XDG_CONFIG_HOME="$VERIFY_TMP/config"
  unset ZELLIJ ZELLIJ_SESSION_NAME ZELLIJ_PANE_ID TERM_PROGRAM
  export EDITOR=cat
  SESSION=ade-test-review
  cat >>"$ADE_CONFIG" <<EOF
[host]
mode = "zellij"
EOF
  PROJECTS="$VERIFY_TMP/projects/demo-gl"
  API="$PROJECTS/api"
  BRANCH=rename-bucket-to-token-bucket
  Z() { env -u ZELLIJ -u ZELLIJ_SESSION_NAME -u ZELLIJ_PANE_ID zellij "$@"; }
  cleanup() {
    set +e
    pkill -f "zellij attach $SESSION" 2>/dev/null
    Z delete-session --force "$SESSION" >/dev/null 2>&1
    return 0
  }
  trap cleanup EXIT
  wait_for() {
    local s=$1 i
    shift
    for ((i = 0; i < s; i++)); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done
    return 1
  }

  check "ade project add (clones api and web)" "$ADE" project add "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
  [[ -d $API/main ]] || fail "no baseline tree"

  "$ADE" review "$MR" --no-zellij --dry-run --json >"$VERIFY_TMP/rv-dry.json"
  expect_json "--dry-run says it would create the worktree and creates nothing" "$VERIFY_TMP/rv-dry.json" \
    ".dry_run == true and .created == true and .branch == \"$BRANCH\" and .worktree == \"$API/$BRANCH\""
  [[ ! -e $API/$BRANCH ]] && pass "no worktree after --dry-run" || fail "--dry-run made $API/$BRANCH"

  "$ADE" review "$MR" --no-zellij --json >"$VERIFY_TMP/rv1.json"
  expect_json "--no-zellij: worktree created for the MR's source branch, base is the target branch, no tab" "$VERIFY_TMP/rv1.json" \
    ".created == true and .repo == \"api\" and .project == \"demo-gl\" and .worktree == \"$API/$BRANCH\" and .base == \"origin/1-rate-limit-the-export-endpoint\" and (.tab // \"\") == \"\" and (.hint | startswith(\"cd '$API/$BRANCH' && git diff 'origin/1-rate-limit-the-export-endpoint'...HEAD\"))"
  [[ $(git -C "$API/$BRANCH" branch --show-current) == "$BRANCH" ]] && pass "the worktree is on $BRANCH" || fail "wrong branch in the worktree"
  [[ $(git -C "$API/$BRANCH" rev-parse HEAD) == "$(git -C "$API/$BRANCH" rev-parse "origin/$BRANCH")" ]] && pass "at the MR's head" || fail "worktree is not at origin/$BRANCH"
  "$ADE" review "$MR" --no-zellij >"$VERIFY_TMP/rv1.txt"
  grep -q "git diff 'origin/1-rate-limit-the-export-endpoint'...HEAD" "$VERIFY_TMP/rv1.txt" && pass "the hint is the command that shows the diff" || fail "no diff command printed"
  # The hint works: it shows the file the MR renames.
  (cd "$API/$BRANCH" && git diff --stat 'origin/1-rate-limit-the-export-endpoint'...HEAD | grep -q token_bucket) && pass "the printed diff covers limit/token_bucket.go" || fail "diff does not show the rename"
  [[ $("$ADE" audit --action git.worktree.add --json | jq 'length') == 1 ]] && pass "the worktree was audited once (git.worktree.add)" || fail "worktree audit"

  Z delete-session --force "$SESSION" >/dev/null 2>&1 || true
  Z attach --create-background "$SESSION" >/dev/null 2>&1
  setsid -f sh -c "sleep 300 | env -u ZELLIJ -u ZELLIJ_SESSION_NAME script -qfc 'stty rows 50 cols 160; zellij attach $SESSION' /dev/null >/dev/null 2>&1"
  wait_for 20 bash -c "env -u ZELLIJ -u ZELLIJ_SESSION_NAME -u ZELLIJ_PANE_ID zellij --session $SESSION action list-panes >/dev/null" || fail "zellij session did not start"
  sleep 2
  "$ADE" review "$MR" --session "$SESSION" --json >"$VERIFY_TMP/rv2.json"
  expect_json "ade review opens tab api/review-2 with the review layout in $SESSION" "$VERIFY_TMP/rv2.json" \
    '.host == "zellij" and .tab == "api/review-2" and .layout == "review" and .session == "ade-test-review" and .tab_created == true'
  Z --session "$SESSION" action query-tab-names | grep -qx 'api/review-2' && pass "zellij lists the tab" || fail "tab missing"
  wait_for 20 bash -c "env -u ZELLIJ -u ZELLIJ_SESSION_NAME -u ZELLIJ_PANE_ID zellij --session $SESSION action list-panes --json | jq -e '[.[] | select(.tab_name == \"api/review-2\" and .is_plugin == false)] | length >= 2' >/dev/null" \
    && pass "panes were created in the tab" || fail "tab has no panes"
  Z --session "$SESSION" action list-panes --json >"$VERIFY_TMP/rv-panes.json"
  expect_json "the large pane runs the diff against the target branch" "$VERIFY_TMP/rv-panes.json" \
    '[.[] | select(.tab_name == "api/review-2") | .terminal_command // ""] | any(contains("git diff") and contains("origin/1-rate-limit-the-export-endpoint"))'
  expect_json "every pane's cwd is the worktree" "$VERIFY_TMP/rv-panes.json" \
    "[.[] | select(.tab_name == \"api/review-2\" and .is_plugin == false and .exited == false) | .pane_cwd] | (length >= 2) and all(. == \"$API/$BRANCH\")"
  expect_json "the review tab ran no run-graph task (no 'ade task run' pane)" "$VERIFY_TMP/rv-panes.json" \
    '[.[] | select(.tab_name == "api/review-2") | .terminal_command // ""] | all(contains("task run") | not)'
  "$ADE" review "$MR" --session "$SESSION" --json >"$VERIFY_TMP/rv3.json"
  expect_json "opening again focuses the existing tab" "$VERIFY_TMP/rv3.json" '.tab == "api/review-2" and .tab_created == false'
  [[ $(Z --session "$SESSION" action query-tab-names | grep -cx 'api/review-2') == 1 ]] && pass "still exactly one review tab" || fail "duplicate tab"
  [[ $("$ADE" audit --action workspace.open --json | jq '[.[] | select(.params.layout == "review")] | length') -ge 1 ]] && pass "workspace.open (review) is audited" || fail "no workspace.open audit"

  if "$ADE" review "$MR" --session ade-test-nonexistent >/dev/null 2>&1; then fail "opened a tab in a session that does not exist"; else pass "a missing session is an error"; fi
  Z list-sessions --no-formatting 2>/dev/null | grep -q '^ade-test-nonexistent' && fail "created a stray session" || pass "no stray session created"

  pkill -f "zellij attach $SESSION" || true
  Z delete-session --force "$SESSION" >/dev/null 2>&1
  Z list-sessions --no-formatting 2>/dev/null | grep -q "^$SESSION " && fail "throwaway session not deleted" || pass "throwaway session deleted"
)

printf '\nGaps (MR page and review): all checks passed\n'
