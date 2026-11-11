#!/usr/bin/env bash
# Phase 2 acceptance (docs/PLAN.md §8): MR page + manual review.
# Run from the repo root inside `nix develop`. Needs .env with GITLAB_PAT.
#
# Live part: a full review round trip on the sandbox with the CLI, no web UI.
# It leaves state behind: five more (resolved) review threads on api!2 per run.
# Ready/draft and approval are restored by the end (and on failure).
. scripts/verify/lib.sh

step "unit and fake-server tests (Free and Premium tiers)"
check "forge write side (GitLab driver + fake): diff, jobs, reply, resolve, approve, draft, merge, draft notes + bulk publish" \
  go test ./internal/forge ./internal/forge/gitlab/...
check "review: drafts persist, one submit, resume after failure, denied never sent, positions on renamed files" \
  go test ./internal/review/...
check "ade mr / ade review commands" go test ./internal/cli/...
# The TUI is driven by key presses against the fake GitLab (see internal/tui/mrpage_test.go).
check "TUI MR page: two drafts + submit = one publish, resolve, merge confirm and refusals, denied agent draft never published" \
  go test ./internal/tui/... -run 'TestPage|TestItemWithoutMR|TestResolve|TestReply|TestEmptyReply|TestApprove|TestMerge|TestRetry|TestTwoDrafts|TestSubmit|TestDenied|TestAcceptAll|TestCommentOnRemoved|TestDraftsPersist|TestFailedSubmit|TestEditor|TestNarrow|TestFooter|TestState|TestHistory'

step "live sandbox (gitlab.com/ade-demo-env, Free tier)"
build_ade
sandbox_gitlab
GRP=ade-demo-env
RENAME=$GRP/api!2 # limit/bucket.go -> limit/token_bucket.go, stacked on api!1
DRAFTMR=$GRP/web!1
TAG="p2-$$-$(date +%s)"

ade_json() { # FILE ARGS...: run ade with --json into FILE; show stderr on failure
  local out=$1
  shift
  "$ADE" "$@" --json >"$out" 2>"$VERIFY_TMP/err" || {
    cat "$VERIFY_TMP/err" "$out" >&2
    fail "ade $*"
  }
}
phase2_cleanup() {
  # Leave the sandbox as found: web!1 a draft, api!2 not approved by us.
  if [[ -n ${ADE:-} && -x ${ADE:-} ]]; then
    "$ADE" mr draft "$DRAFTMR" >/dev/null 2>&1 || true
    "$ADE" mr unapprove "$RENAME" >/dev/null 2>&1 || true
  fi
  rm -rf "$VERIFY_TMP"
}
trap phase2_cleanup EXIT
audit_count() { "$ADE" audit --action "$1" --json | jq 'length'; }

check "ade sync exits 0 (the sandbox is reachable)" "$ADE" sync

step "read the MR: diff with the rename resolved, threads"
ade_json "$VERIFY_TMP/show0.json" mr show "$RENAME"
N0=$(jq '.threads | length' "$VERIFY_TMP/show0.json")
ade_json "$VERIFY_TMP/diff.json" mr diff "$RENAME"
D="$VERIFY_TMP/diff.json"
expect_json "api!2's diff is one renamed file: limit/bucket.go -> limit/token_bucket.go" "$D" \
  '(.files | length) == 1 and .files[0].renamed == true and .files[0].old_path == "limit/bucket.go" and .files[0].new_path == "limit/token_bucket.go"'
expect_json "the diff carries the version SHAs a comment position needs" "$D" \
  '(.base_sha | length) == 40 and (.head_sha | length) == 40'

step "five line comments on the renamed file: drafts first, nothing posted"
ade_json "$VERIFY_TMP/list0.json" review list "$RENAME"
expect_json "no leftover drafts in the isolated cache" "$VERIFY_TMP/list0.json" 'length == 0'
F=limit/token_bucket.go
check "draft 1: unchanged line, new side"    "$ADE" review add "$RENAME" --path $F --line 1  --severity question   --body "[$TAG] What is this package for?"
check "draft 2: added line"                  "$ADE" review add "$RENAME" --path $F --line 3  --severity nit        --body "[$TAG] The doc comment says continuously, but nothing refills."
check "draft 3: added line (new field)"      "$ADE" review add "$RENAME" --path $F --line 7  --severity suggestion --body "[$TAG] Use an int64 count of milli-tokens to avoid float drift."
check "draft 4: added line (Allow)"          "$ADE" review add "$RENAME" --path $F --line 10 --severity blocking   --body "[$TAG] Allow never consumes a token."
check "draft 5: removed line, old side"      "$ADE" review add "$RENAME" --path $F --line 3 --old --severity question --body "[$TAG] Why drop the old doc line?"
ade_json "$VERIFY_TMP/list1.json" review list "$RENAME"
expect_json "five drafts are saved locally" "$VERIFY_TMP/list1.json" 'length == 5'
ade_json "$VERIFY_TMP/show1.json" mr show "$RENAME"
expect_json "none reached the forge before submit" "$VERIFY_TMP/show1.json" ".threads | length == $N0"
check "a comment outside the diff is refused before any request" bash -c "! $ADE review add '$RENAME' --path $F --line 99 --body x"
W0=$(audit_count 'forge.draftnote.publish')

step "submit: one batch"
ade_json "$VERIFY_TMP/dry.json" review submit "$RENAME" --dry-run
expect_json "dry run validates the five positions and posts nothing" "$VERIFY_TMP/dry.json" '.dry_run == true and .uploaded == 5'
ade_json "$VERIFY_TMP/show2.json" mr show "$RENAME"
expect_json "still nothing on the forge after the dry run" "$VERIFY_TMP/show2.json" ".threads | length == $N0"
ade_json "$VERIFY_TMP/submit.json" review submit "$RENAME"
expect_json "five comments uploaded and published together" "$VERIFY_TMP/submit.json" '.uploaded == 5 and .published == 5 and .approved == false'
[[ $(audit_count 'forge.draftnote.publish') == $((W0 + 1)) ]] && pass "exactly one bulk publish in the audit log (one notification)" || fail "publish count"
ade_json "$VERIFY_TMP/list2.json" review list "$RENAME"
expect_json "published drafts are gone from the local store" "$VERIFY_TMP/list2.json" 'length == 0'

step "read back: five discussions, positions on the renamed file"
ade_json "$VERIFY_TMP/show3.json" mr show "$RENAME"
S="$VERIFY_TMP/show3.json"
expect_json "five new discussions" "$S" ".threads | length == $N0 + 5"
mine() { printf '[.threads[] | select(.notes[0].body | contains("[%s] %s"))][0]' "$TAG" "$1"; }
for spec in "What is this package|1|0" "The doc comment says continuously|3|0" "Use an int64|7|0" "Allow never consumes|10|0" "Why drop the old doc line|0|3"; do
  IFS='|' read -r needle nl ol <<<"$spec"
  expect_json "'$needle…': new line $nl (removed line: old line $ol), on the renamed file" "$S" \
    "$(mine "$needle") | .position.path == \"limit/token_bucket.go\" and (.position.new_line // 0) == $nl and ($nl > 0 or (.position.old_line // 0) == $ol)"
done
expect_json "the renamed file's old path is on every position" "$S" \
  "[.threads[] | select(.notes[0].body | contains(\"[$TAG]\"))] | length == 5 and all(.position.old_path == \"limit/bucket.go\")"
expect_json "severity leads the body (**nit:** ...)" "$S" "$(mine 'The doc comment') | .notes[0].body | startswith(\"**nit:** \")"

step "reply, resolve, reopen"
TID=$(jq -r "$(mine 'Allow never consumes') | .id" "$S")
check "reply in the thread" "$ADE" mr reply "$RENAME" "$TID" "[$TAG] Agreed, fixing."
check "resolve" "$ADE" mr resolve "$RENAME" "$TID"
ade_json "$VERIFY_TMP/show4.json" mr show "$RENAME"
expect_json "reply is in the thread and it is resolved" "$VERIFY_TMP/show4.json" \
  "[.threads[] | select(.id == \"$TID\")][0] | .resolved == true and (.notes | length) == 2"
check "reopen" "$ADE" mr resolve "$RENAME" "$TID" --undo
ade_json "$VERIFY_TMP/show5.json" mr show "$RENAME"
expect_json "reopened" "$VERIFY_TMP/show5.json" "[.threads[] | select(.id == \"$TID\")][0].resolved == false"
for id in $(jq -r "[.threads[] | select(.notes[0].body | contains(\"[$TAG]\")) | .id][]" "$VERIFY_TMP/show5.json"); do
  "$ADE" mr resolve "$RENAME" "$id" >/dev/null # leave this run's threads resolved
done
ade_json "$VERIFY_TMP/show6.json" mr show "$RENAME"
expect_json "this run's five threads are resolved again" "$VERIFY_TMP/show6.json" \
  "[.threads[] | select(.notes[0].body | contains(\"[$TAG]\")) | .resolved] | length == 5 and all"

step "ready/draft and approve/unapprove"
check "web!1 marked ready" "$ADE" mr ready "$DRAFTMR"
ade_json "$VERIFY_TMP/web1.json" mr show "$DRAFTMR"
expect_json "web!1 is ready" "$VERIFY_TMP/web1.json" '.mr.draft == false'
check "web!1 back to draft" "$ADE" mr draft "$DRAFTMR"
ade_json "$VERIFY_TMP/web1.json" mr show "$DRAFTMR"
expect_json "web!1 is a draft again" "$VERIFY_TMP/web1.json" '.mr.draft == true'
check "approve api!2 at the reviewed head" "$ADE" mr approve "$RENAME"
ade_json "$VERIFY_TMP/appr.json" mr show "$RENAME"
expect_json "api!2 has our approval" "$VERIFY_TMP/appr.json" '(.mr.approvals.approved_by | length) == 1'
check "unapprove" "$ADE" mr unapprove "$RENAME"
ade_json "$VERIFY_TMP/appr.json" mr show "$RENAME"
expect_json "approval withdrawn" "$VERIFY_TMP/appr.json" '(.mr.approvals.approved_by | length) == 0'

step "every forge write is audited"
for a in forge.draftnote.add forge.draftnote.publish forge.thread.reply forge.thread.resolve forge.mr.draft forge.approve forge.unapprove review.submit; do
  [[ $(audit_count "$a") -ge 1 ]] && pass "audit has $a" || fail "audit lacks $a"
done

printf '\nPhase 2: all checks passed\n'
