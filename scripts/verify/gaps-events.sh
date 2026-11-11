#!/usr/bin/env bash
# Gap round, events and worktree lifecycle (docs/PLAN.md 6.8, 2.2):
#   ci.canceled, mr.changes_requested, mr.comment on replies, no echo of my own
#   actions, budget.warn, agent.plan.ready / agent.run.end / agent.run.failed,
#   worktree.removable, `ade gc`, `ade project remove`.
#
# Run from the repo root inside `nix develop`. Needs .env with GITLAB_PAT for
# the live part; everything before it runs against the fake GitLab.
. scripts/verify/lib.sh

step "unit tests: every event over the fake GitLab, the guard, the budget warning"
check "forgesync: ci.canceled, mr.changes_requested, replies, own actions, worktree.removable" \
  go test ./internal/forgesync/... -run 'TestEvent|TestEventsDiff|TestRecovery'
check "forge drivers: reviewer verdicts and merged_by (GitLab fake, GitHub fake)" go test ./internal/forge/...
check "ai: budget.warn once per period at 80%" go test ./internal/ai/...
check "agent: agent.plan.ready, agent.run.end, agent.run.failed" go test ./internal/agent/...
check "project: gc guard and removal, project remove guard and config edit" \
  go test ./internal/project/... -run 'GC|Remove|TreeOf'

step "fake GitLab through the real binary: ade sync diffs land in the inbox"
build_ade
go build -o "$VERIFY_TMP/fakeevents" ./scripts/verify/fakeevents
trap 'kill ${FAKE_PID:-} 2>/dev/null || true; rm -rf "$VERIFY_TMP"' EXIT

# fake_start NAME: serve the fake, sync once as a baseline in an isolated XDG.
fake_start() {
  local d=$VERIFY_TMP/$1
  mkdir -p "$d"
  mkfifo "$d/in" "$d/out"
  exec {FIN}<>"$d/in" {FOUT}<>"$d/out"
  "$VERIFY_TMP/fakeevents" <&"$FIN" >&"$FOUT" 2>"$d/err" &
  FAKE_PID=$!
  read -r -t 10 -u "$FOUT" FAKE_URL || fail "the fake GitLab did not start: $(cat "$d/err")"
  export XDG_DATA_HOME="$d/data" XDG_CACHE_HOME="$d/cache" ADE_CONFIG="$d/config.toml" FAKE_GITLAB_TOKEN=fake-token
  cat >"$ADE_CONFIG" <<EOF
[user]
handle = "sandbox"
[forges.sandbox-gitlab]
kind = "gitlab"
url = "$FAKE_URL"
token_env = "FAKE_GITLAB_TOKEN"
[update]
on_start = false
[paths]
root = "$d/projects"
[[projects]]
source = "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
EOF
  "$ADE" sync >/dev/null || fail "baseline sync"
  "$ADE" inbox read --all >/dev/null
}

fake_stop() {
  kill "$FAKE_PID" 2>/dev/null || true
  wait "$FAKE_PID" 2>/dev/null || true
  exec {FIN}>&- {FOUT}>&-
}

# expect_events STEP KINDS TITLE_GREP: apply STEP in the fake, sync, and expect
# exactly the sorted, space-separated KINDS to be newly unread in the inbox
# (and, when given, TITLE_GREP to match a title).
expect_events() {
  local stepname=$1 want=$2 title=${3:-} ans
  echo "$stepname" >&"$FIN"
  read -r -t 10 -u "$FOUT" ans || fail "$stepname: the fake did not answer"
  [[ $ans == ok ]] || fail "$stepname: fake said $ans"
  "$ADE" sync >/dev/null || fail "$stepname: sync"
  "$ADE" inbox --unread --json >"$VERIFY_TMP/new.json"
  "$ADE" inbox read --all >/dev/null
  local got
  got=$(jq -r '[.[].kind] | sort | join(" ")' "$VERIFY_TMP/new.json")
  [[ $got == "$want" ]] || {
    jq -c '.[] | {kind, title}' "$VERIFY_TMP/new.json" >&2
    fail "$stepname raised [$got], want [$want]"
  }
  if [[ -n $title ]]; then
    jq -e --arg t "$title" 'any(.[]; .title | test($t))' "$VERIFY_TMP/new.json" >/dev/null ||
      fail "$stepname: no title matching '$title'"
  fi
  pass "$stepname -> [${want:-nothing}]${title:+ ($title)}"
}

fake_start a
expect_events canceled "ci.canceled" "pipeline canceled on api!1"
expect_events changes "mr.changes_requested" "changes requested on api!1 by reviewer-rae"
expect_events thread "mr.comment" "new thread on api!1"
expect_events reply "mr.comment" "new reply on api!1"
expect_events mine "" # my own comment and approval are not echoed
expect_events approve-rae "mr.approved" "api!1 approved by reviewer-rae"
expect_events merge-rae "mr.merged" "api!1 merged by reviewer-rae"
fake_stop

fake_start b
expect_events merge-me "" # my own merge is not echoed
fake_stop

step "live sandbox: ade gc and ade project remove on the clone layout"
sandbox_gitlab
check "ade project add clones the sandbox repos" "$ADE" project add "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
ROOT="$VERIFY_TMP/projects/demo-gl"
[[ -d $ROOT/api/.bare && -d $ROOT/web/.bare ]] || fail "clone layout missing under $ROOT"

# Feature worktrees the way the live MRs leave them:
#  - api!1 is open (its branch exists): kept, "still open"
#  - web!2 is merged and its branch is gone from the remote; the local tree sits on commits origin has: removable
#  - web!3 is merged too, but its tree has an uncommitted file: kept
#  - a branch with no MR at all: kept
git -C "$ROOT/api/.bare" worktree add --quiet --track -b 1-rate-limit-the-export-endpoint \
  "$ROOT/api/1-rate-limit-the-export-endpoint" origin/1-rate-limit-the-export-endpoint
git -C "$ROOT/web/.bare" worktree add --quiet -b 2-fix-truncated-column-headers-in-export-preview \
  "$ROOT/web/2-fix-truncated-column-headers-in-export-preview" origin/main
git -C "$ROOT/web/.bare" worktree add --quiet -b throwaway-merge-1794410489 "$ROOT/web/throwaway-merge-1794410489" origin/main
echo scratch >"$ROOT/web/throwaway-merge-1794410489/notes.txt"
git -C "$ROOT/web/.bare" worktree add --quiet -b idea-without-mr "$ROOT/web/idea-without-mr" origin/main

"$ADE" gc --dry-run --json >"$VERIFY_TMP/gc.json"
G="$VERIFY_TMP/gc.json"
row() { printf '.[] | select(.branch == "%s")' "$1"; }
expect_json "api!1's tree is kept: the MR is still open" "$G" \
  "[$(row 1-rate-limit-the-export-endpoint)] | .[0] | .action == \"keep\" and (.keep | join(\";\") | contains(\"still open\"))"
expect_json "web!2's tree is removable (merged, clean, pushed)" "$G" \
  "[$(row 2-fix-truncated-column-headers-in-export-preview)] | .[0] | .action == \"remove\" and .mr == \"web!2 merged\" and (.keep // []) == []"
expect_json "web!3's tree is kept: uncommitted changes" "$G" \
  "[$(row throwaway-merge-1794410489)] | .[0] | .action == \"keep\" and (.keep | join(\";\") | contains(\"uncommitted changes\"))"
expect_json "a branch with no MR is kept and says why" "$G" \
  "[$(row idea-without-mr)] | .[0] | .action == \"keep\" and (.keep | join(\";\") | contains(\"no merge request\"))"
[[ -d $ROOT/web/2-fix-truncated-column-headers-in-export-preview ]] && pass "--dry-run removed nothing" || fail "--dry-run removed a tree"

if "$ADE" gc </dev/null >"$VERIFY_TMP/out" 2>&1; then fail "gc without --yes and without a terminal removed things"; fi
grep -q -- "--yes" "$VERIFY_TMP/out" && pass "unattended gc refuses and names --yes" || fail "no hint about --yes"

check "ade gc --yes" "$ADE" gc --yes
[[ ! -d $ROOT/web/2-fix-truncated-column-headers-in-export-preview ]] && pass "the removable tree is gone" || fail "removable tree still there"
for keep in api/1-rate-limit-the-export-endpoint web/throwaway-merge-1794410489 web/idea-without-mr; do
  [[ -d $ROOT/$keep ]] && pass "kept: $keep" || fail "gc removed $keep"
done
[[ -z $(git -C "$ROOT/web/.bare" branch --list 2-fix-truncated-column-headers-in-export-preview) ]] &&
  pass "its merged local branch went with it" || fail "branch survived"
"$ADE" audit --action worktree.remove --json >"$VERIFY_TMP/audit.json"
expect_json "the removal is audited" "$VERIFY_TMP/audit.json" 'length == 1 and .[0].outcome == "ok"'

"$ADE" project remove demo-gl --dry-run --json >"$VERIFY_TMP/rm.json" && fail "dry-run of a blocked removal should exit non-zero" || true
expect_json "project remove names the dirty tree" "$VERIFY_TMP/rm.json" \
  '.blockers | any(.where == "web/throwaway-merge-1794410489" and (.reasons | join(";") | contains("uncommitted changes")))'
if "$ADE" project remove demo-gl --yes >/dev/null 2>&1; then fail "project remove without --force deleted a dirty project"; fi
[[ -d $ROOT ]] && pass "refused without --force; nothing deleted" || fail "project dir deleted"
"$ADE" project remove demo-gl --dry-run --force >/dev/null && [[ -d $ROOT ]] && pass "--dry-run --force deletes nothing" || fail "dry-run"
check "ade project remove --force --yes" "$ADE" project remove demo-gl --force --yes
[[ ! -d $ROOT ]] && pass "project directory deleted" || fail "project dir still there"
grep -q "gitlab.ade.toml" "$ADE_CONFIG" && fail "config still registers the project" || pass "config entry removed"

printf '\nGap round (events): all checks passed\n'
