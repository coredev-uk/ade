#!/usr/bin/env bash
# Phase 6 acceptance (docs/PLAN.md §8): agents. Plan -> approve -> run -> review
# -> push completes on a sandbox issue within agent_run_usd, and the transcript
# lands only in the item record.
# Run from the repo root inside `nix develop`. Needs .env with GITLAB_PAT and
# omp logged in (the only paid call is omp with the cheapest model; every run
# is capped at $0.50 and the script prints the total it spent).
#
# Sandbox state it leaves behind (synthetic, like phase3): the issue "Add
# /version endpoint to api" (created once), its child issue, branch and draft
# MR, and one agent commit per run on that branch. A re-run is safe: each run
# adds a reviewer comment with a fresh stamp that the plan must honour, so the
# agent always has a change to make.
. scripts/verify/lib.sh

MODEL=${ADE_AGENT_MODEL:-anthropic/claude-haiku-5-5}
CAP=0.50

step "unit tests: drivers, state machine, cost cap kill, run store, TUI, CLI"
check "agent: claude/codex/omp command construction + recorded fixtures, lifecycle, cap hard stop, cancel, worktree guards, drafts" \
  go test -count=1 ./internal/agent/...
check "agent TUI: menu, run list, plan approve, review/push confirm, glyph sets, widths" \
  go test -count=1 ./internal/tui/ -run 'Agent'
check "ade agent CLI: list, show, approve, cancel, refusals, audit" go test -count=1 ./internal/cli/ -run 'Agent'
check "doctor reports missing agent drivers as unavailable" go test -count=1 ./internal/doctor/ -run 'Agent|Tools'
check "config: agent keys validate" go test -count=1 ./internal/config/...

step "live sandbox (gitlab.com/ade-demo-env, Free tier) + omp ($MODEL)"
command -v omp >/dev/null || fail "omp not on PATH"
build_ade
sandbox_gitlab
export ADE_SANDBOX_GROUP=ade-demo-env
cat >>"$ADE_CONFIG" <<EOF
[ai]
mode = "auto"
profile = "sub"
[ai.profiles.sub]
driver = "omp"
models = { explain = "$MODEL", draft = "$MODEL", plan = "$MODEL", implement = "$MODEL", review = "$MODEL" }
[budget]
agent_run_usd = $CAP
EOF
API="https://gitlab.com/api/v4"
GRP=ade-demo-env
enc() { jq -rn --arg v "$1" '$v|@uri'; }
gl() {
  local path=$1 status attempt wait
  shift
  for attempt in 1 2 3 4 5 6; do
    status=$(curl -sS -o "$VERIFY_TMP/gl.body" -D "$VERIFY_TMP/gl.hdr" -w '%{http_code}' -H "PRIVATE-TOKEN: $GITLAB_PAT" "$@" "$API$path")
    [[ $status != 429 ]] && break
    wait=$(awk 'tolower($1)=="retry-after:" {print $2+0}' "$VERIFY_TMP/gl.hdr")
    sleep "${wait:-30}"
  done
  ((status < 400)) || {
    printf 'GitLab %s -> %s: %s\n' "$path" "$status" "$(cat "$VERIFY_TMP/gl.body")" >&2
    return 1
  }
  cat "$VERIFY_TMP/gl.body"
}
ade_json() { # FILE ARGS...: run ade with --json into FILE; show stderr on failure
  local out=$1
  shift
  "$ADE" "$@" --json >"$out" 2>"$VERIFY_TMP/err" || {
    cat "$VERIFY_TMP/err" "$out" >&2
    fail "ade $*"
  }
}
SPEND=0
add_spend() { SPEND=$(jq -n --argjson a "$SPEND" --argjson b "$1" '$a + $b'); }

check "ade project add clones api and web" "$ADE" project add "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"

step "the sandbox issue: Add /version endpoint to api (created once)"
TITLE="Add /version endpoint to api"
TRK=$(enc "$GRP/tracker")
ME_ID=$(gl /user | jq -r .id)
MS_ID=$(gl "/groups/$GRP/milestones?title=$(enc 'Sprint 42')&include_ancestors=false" | jq -r '.[0].id')
[[ $MS_ID != null && -n $MS_ID ]] || fail "Sprint 42 milestone missing: run scripts/sandbox/gitlab-seed.sh"
IID=$(gl "/projects/$TRK/issues?state=all&in=title&search=$(enc "$TITLE")&per_page=100" | jq -r --arg t "$TITLE" '[.[] | select(.title == $t)][0].iid // empty')
if [[ -z $IID ]]; then
  IID=$(gl "/projects/$TRK/issues" -X POST -H 'Content-Type: application/json' --data "$(jq -n --arg t "$TITLE" \
    --argjson m "$MS_ID" --argjson a "$ME_ID" '{title:$t, labels:"type::story,app::api,weight::1", milestone_id:$m, assignee_ids:[$a],
    description:"Add a GET /version endpoint to the api service. It returns the JSON object {\"version\": \"dev\"}. Keep the change tiny: one new Go file for the handler and, if the repo already has route registration, one line to register it."}')" | jq -r .iid)
  pass "created $GRP/tracker#$IID"
else
  pass "found $GRP/tracker#$IID"
fi
REF="$GRP/tracker#$IID"
gl "/projects/$TRK/issues/$IID" >"$VERIFY_TMP/issue.json"
expect_json "issue is weight 1, app::api, Sprint 42, assigned to the token user" "$VERIFY_TMP/issue.json" \
  ".state == \"opened\" and (.labels | index(\"weight::1\") != null and index(\"app::api\") != null) and .milestone.title == \"Sprint 42\" and ([.assignees[].id] | index($ME_ID) != null)"
# A fresh requirement per run, in the issue description: the agent must produce
# a change every time, even when an earlier run already pushed the endpoint.
STAMP=$(date -u +%Y%m%d%H%M%S)
BASE_DESC='Add a GET /version endpoint to the api service. It returns the JSON object {"version": "dev"}. Keep the change tiny: one new Go file for the handler and, if the repo already has route registration, one line to register it.'
gl "/projects/$TRK/issues/$IID" -X PUT -H 'Content-Type: application/json' \
  --data "$(jq -n --arg d "$BASE_DESC

REQUIRED for this change: also add the file notes/version-$STAMP.md containing exactly the single line $STAMP." '{description:$d}')" >/dev/null
pass "the issue now requires notes/version-$STAMP.md"

step "start work (no workspace): child, branch, draft MR, worktree"
ade_json "$VERIFY_TMP/start.json" start "$REF" --repo api --no-workspace
WT=$(jq -r '.targets[0].worktree' "$VERIFY_TMP/start.json")
BR=$(jq -r '.targets[0].branch' "$VERIFY_TMP/start.json")
MR=$(jq -r '.targets[0].mr' "$VERIFY_TMP/start.json")
[[ -d $WT && -n $MR && $MR != null ]] && pass "worktree $WT on $BR, MR $MR" || fail "start work left no worktree/MR"
MR_IID=${MR##*!}
PROJ_API=$(enc "$GRP/api")

step "pre-flight: the cap is checked before any money is spent"
"$ADE" agent plan "$REF" --repo api --max-usd 0.0001 --yes >"$VERIFY_TMP/refuse.out" 2>&1 && fail "a \$0.0001 cap must refuse the run" || true
grep -q "over the run cap" "$VERIFY_TMP/refuse.out" && pass "refused: $(tail -1 "$VERIFY_TMP/refuse.out" | cut -c1-110)" || { cat "$VERIFY_TMP/refuse.out" >&2; fail "no cap refusal"; }
[[ $("$ADE" agent list --all --json | jq length) == 0 ]] && pass "a refused run is not created" || fail "refused run exists"
"$ADE" audit --action budget.stop --json | jq -e 'length >= 1' >/dev/null && pass "the refusal is audited as budget.stop" || fail "no budget.stop audit entry"
"$ADE" agent plan "$REF" --repo api --dry-run --json >"$VERIFY_TMP/dry.json" 2>&1 || { cat "$VERIFY_TMP/dry.json" >&2; fail "agent plan --dry-run"; }
expect_json "dry run shows the target and estimate, starts nothing" "$VERIFY_TMP/dry.json" '.dry_run == true and .target.branch != null and .preflight.priced == true'
[[ $("$ADE" agent list --all --json | jq length) == 0 ]] && pass "dry run created no run" || fail "dry run created a run"

step "plan (read-only) -> approval -> run (retries a run that changed nothing, at most 3 times)"
plan_to_review() {
ade_json "$VERIFY_TMP/plan.json" agent plan "$REF" --repo api --yes
P="$VERIFY_TMP/plan.json"
RUN=$(jq -r .id "$P")
expect_json "run $RUN is awaiting approval, under the cap, with one reported plan call" "$P" \
  ".state == \"awaiting-approval\" and .cap_usd == $CAP and .cost_usd > 0 and .cost_usd <= $CAP and (.calls | length) == 1 and .calls[0].kind == \"plan\" and .calls[0].cost_source == \"reported\" and .calls[0].exit == 0"
add_spend "$(jq .cost_usd "$P")"
RUNDIR=$(find "$XDG_DATA_HOME" -type d -path "*/items/*/runs/$RUN" | head -1)
[[ -n $RUNDIR ]] && pass "session dir: ${RUNDIR#"$XDG_DATA_HOME"/}" || fail "no run directory"
case $RUNDIR in */items/sandbox-gitlab/ade-demo-env/tracker/"$IID"/runs/*) pass "it is inside the item record of $REF" ;; *) fail "run dir is not in the item record: $RUNDIR" ;; esac
for f in plan.md transcript.jsonl cost.json run.json run.md; do
  [[ -s $RUNDIR/$f ]] && pass "$f written" || fail "$f missing in the run directory"
done
grep -q "$STAMP" "$RUNDIR/plan.md" && pass "the plan honours the reviewer comment (stamp $STAMP)" || fail "plan does not mention $STAMP: the issue comments were not in its context"
[[ -z $(git -C "$WT" status --porcelain) ]] && pass "a plan is read-only: the worktree is untouched" || fail "plan changed the worktree"

"$ADE" agent run "$RUN" >/dev/null 2>"$VERIFY_TMP/err" && fail "run started without approval" || true
grep -q "approve" "$VERIFY_TMP/err" && pass "run before approve is refused" || fail "unexpected refusal text: $(cat "$VERIFY_TMP/err")"
ade_json "$VERIFY_TMP/approve.json" agent approve "$RUN"
expect_json "approved; still awaiting (approving does not run)" "$VERIFY_TMP/approve.json" '.approved == true and .state == "awaiting-approval"'

if ! "$ADE" agent run "$RUN" --json >"$VERIFY_TMP/run.json" 2>"$VERIFY_TMP/err"; then
  if grep -q "made no changes" "$VERIFY_TMP/err"; then
    echo "  note  the agent made no change this time (a model can skip a small step); planning again" >&2
    add_spend "$(jq '.calls[1].cost_usd // 0' "$VERIFY_TMP/run.json" 2>/dev/null || echo 0)"
    return 1
  fi
  cat "$VERIFY_TMP/err" "$VERIFY_TMP/run.json" >&2
  fail "ade agent run"
fi
R="$VERIFY_TMP/run.json"
expect_json "state review; two calls (plan, run); total cost within the cap" "$R" \
  ".state == \"review\" and (.calls | map(.kind)) == [\"plan\",\"run\"] and .cost_usd <= $CAP and .base_sha != null"
add_spend "$(jq '.calls[1].cost_usd' "$R")"
jq -e --argjson cap "$CAP" '.total_usd <= $cap and .cap_usd == $cap and (((.calls | map(.cost_usd) | add) - .total_usd) | fabs) < 1e-9' "$RUNDIR/cost.json" >/dev/null \
  && pass "cost.json: total \$$(jq -r .total_usd "$RUNDIR/cost.json") of the \$$CAP cap" || fail "cost.json does not add up or exceeds the cap"

}
ATTEMPT=0
until plan_to_review; do
  ((++ATTEMPT < 3)) || fail "the agent changed nothing in 3 runs"
done

step "diff: what changed, before anything is committed"
"$ADE" agent diff "$RUN" >"$VERIFY_TMP/diff.txt" || fail "ade agent diff"
grep -q "notes/version-$STAMP.md" "$VERIFY_TMP/diff.txt" && pass "diff stat lists notes/version-$STAMP.md" || { cat "$VERIFY_TMP/diff.txt" >&2; fail "the requested file is not in the diff"; }
ade_json "$VERIFY_TMP/diff.json" agent diff "$RUN" --patch
expect_json "JSON diff: files and a unified patch" "$VERIFY_TMP/diff.json" '(.files | length) >= 1 and (.patch | contains("+++ b/"))'
[[ -z $(git -C "$WT" diff --cached --name-only) ]] && pass "reviewing staged nothing" || fail "diff touched the index"
[[ $(git -C "$WT" log -1 --format=%H) == $(jq -r .base_sha "$R") ]] && pass "nothing committed before push" || fail "a commit exists before push"
remote_head() { gl "/projects/$PROJ_API/repository/branches/$(enc "$BR")" | jq -r .commit.id; }
REMOTE_BEFORE=$(remote_head)

step "push: commit with a synthetic author, push the branch, the MR updates"
ade_json "$VERIFY_TMP/push.json" agent push "$RUN"
expect_json "run is pushed with one commit" "$VERIFY_TMP/push.json" '.state == "pushed" and (.commits | length) == 1'
SHA=$(jq -r '.commits[0]' "$VERIFY_TMP/push.json")
# GitLab computes an MR's commit list asynchronously after a push: wait for it.
for _ in $(seq 30); do
  gl "/projects/$PROJ_API/merge_requests/$MR_IID/commits?per_page=100" >"$VERIFY_TMP/mrcommits.json"
  [[ $(jq --arg s "$SHA" '[.[] | select(.id == $s)] | length' "$VERIFY_TMP/mrcommits.json") == 1 ]] && break
  sleep 2
done
expect_json "MR $MR has the agent commit ${SHA:0:8}, authored \"... (ade agent)\"" "$VERIFY_TMP/mrcommits.json" \
  "[.[] | select(.id == \"$SHA\")] | length == 1 and (.[0].author_name | endswith(\"(ade agent)\"))"
[[ $(remote_head) == "$SHA" ]] && pass "origin/$BR is $SHA (was ${REMOTE_BEFORE:0:8})" || fail "origin branch is not the pushed commit"
"$ADE" agent push "$RUN" >/dev/null 2>&1 && fail "a pushed run was pushed again" || pass "a pushed run cannot be pushed twice"

step "audit: brackets, cost, no transcript content"
"$ADE" audit --item "$REF" --json >"$VERIFY_TMP/audit.json"
expect_json "agent.run.start/end bracket both calls; the plan approval, commit and push are audited" "$VERIFY_TMP/audit.json" \
  '[.[].action] as $a | ([$a[] | select(. == "agent.run.start")] | length) == 2 and ([$a[] | select(. == "agent.run.end")] | length) == 2 and ($a | index("agent.plan.approve") != null and index("git.commit") != null and index("git.push") != null)'
expect_json "ai.agent-plan entries carry the reported cost (the ledger feed), actor agent:$RUN" "$VERIFY_TMP/audit.json" \
  "[.[] | select(.action == \"ai.agent-plan\" and .actor == \"agent:$RUN\")] | length == 2 and all(.ai.cost_usd > 0 and .ai.cost_source == \"reported\" and .ai.artifact == null)"
LEDGER=$(jq '[.[] | select(.action | startswith("ai.agent")) | .ai.cost_usd] | add' "$VERIFY_TMP/audit.json")
[[ $(jq -n --argjson l "$LEDGER" --argjson r "$(jq .cost_usd "$VERIFY_TMP/push.json")" '(($l - $r) | fabs) < 1e-9') == true ]] \
  && pass "audited cost \$$LEDGER equals the run's cost" || fail "audit cost $LEDGER != run cost"

step "privacy: the transcript is in the run directory and nowhere else"
MARK=$(jq -rs '[.[] | select(.type == "session")][0].id // empty' "$RUNDIR/transcript.jsonl")
[[ -n $MARK ]] || fail "no session marker in the transcript"
PLANWORD=$(grep -o "version-$STAMP" "$RUNDIR/plan.md" | head -1)
grep -q "$MARK" "$RUNDIR/transcript.jsonl" && pass "marker $MARK is in the transcript" || fail "marker not in transcript"
CACHE_DIR="$XDG_CACHE_HOME/ade"
sqlite3 "$CACHE_DIR/cache.db" 'PRAGMA wal_checkpoint(TRUNCATE)' >/dev/null 2>&1 || true
AUDIT_DIR=$(find "$XDG_DATA_HOME" -type d -name audit | head -1)
if grep -rl "$MARK" "$CACHE_DIR" "$AUDIT_DIR" 2>/dev/null | grep -q .; then fail "the transcript marker leaked into cache.db or the audit log"; else pass "grep -r for the marker finds nothing in cache.db or the audit log"; fi
if grep -rl "$MARK" "$XDG_DATA_HOME" 2>/dev/null | grep -v "/runs/$RUN/" | grep -q .; then fail "the marker is outside the run directory: $(grep -rl "$MARK" "$XDG_DATA_HOME" | grep -v "/runs/$RUN/")"; else pass "inside the data dir it appears only under runs/$RUN/"; fi
[[ -n $PLANWORD ]] && ! grep -rq "$PLANWORD" "$CACHE_DIR" "$AUDIT_DIR" 2>/dev/null && pass "plan text is not in cache.db or the audit log either" || fail "plan text leaked"
# Other omp sessions (an editor, this script's caller) write there too, so look
# for the run's own session id, not merely a recently modified file.
SESS=$(grep -rl --include='*.jsonl' "$MARK" "$HOME/.omp" 2>/dev/null | head -1 || true)
[[ -z $SESS ]] && pass "omp ran with --no-session: no session file carries the run's id" || fail "omp left a session file: $SESS"

step "run list and show"
"$ADE" agent list --all >"$VERIFY_TMP/list.txt"
grep -q "$RUN.*pushed" "$VERIFY_TMP/list.txt" && pass "ade agent list shows $RUN pushed with its spend" || { cat "$VERIFY_TMP/list.txt" >&2; fail "run missing from the list"; }
"$ADE" agent show "$RUN" | grep -q "cost" && pass "ade agent show" || fail "agent show"

step "address comments: MR threads in, code edit and pending reply drafts out, nothing posted"
T1="$GRP/tracker#1"
ade_json "$VERIFY_TMP/start1.json" start "$T1" --repo api --no-workspace
WT1=$(jq -r '.targets[0].worktree' "$VERIFY_TMP/start1.json")
MR1=$(jq -r '.targets[0].mr' "$VERIFY_TMP/start1.json")
MR1_IID=${MR1##*!}
gl "/projects/$PROJ_API/merge_requests/$MR1_IID/discussions?per_page=100" >"$VERIFY_TMP/disc-before.json"
UNRES=$(jq '[.[] | select(.notes[0].resolvable == true and .notes[0].resolved == false)] | length' "$VERIFY_TMP/disc-before.json")
if ((UNRES > 0)); then
  ade_json "$VERIFY_TMP/addr.json" agent address "$T1" --repo api --yes
  A="$VERIFY_TMP/addr.json"
  expect_json "address run reached review within the cap" "$A" ".kind == \"address\" and .state == \"review\" and .cost_usd <= $CAP and .cost_usd > 0"
  add_spend "$(jq .cost_usd "$A")"
  "$ADE" review list "$MR1" --json >"$VERIFY_TMP/drafts.json" 2>/dev/null || ade_json "$VERIFY_TMP/drafts.json" review list "$MR1"
  expect_json "replies are pending agent drafts (not selected for submit)" "$VERIFY_TMP/drafts.json" \
    'length >= 1 and all(.source == "agent" and .state == "pending")'
  gl "/projects/$PROJ_API/merge_requests/$MR1_IID/discussions?per_page=100" >"$VERIFY_TMP/disc-after.json"
  [[ $(jq length "$VERIFY_TMP/disc-before.json") == $(jq length "$VERIFY_TMP/disc-after.json") && $(jq '[.[].notes | length] | add' "$VERIFY_TMP/disc-before.json") == $(jq '[.[].notes | length] | add' "$VERIFY_TMP/disc-after.json") ]] \
    && pass "nothing was posted to $MR1: same threads and notes as before" || fail "the address run posted to the forge"
  gl "/projects/$PROJ_API/merge_requests/$MR1_IID/discussions?per_page=100" | jq -e '[.[] | select(.notes[0].resolvable == true and .notes[0].resolved == false)] | length == '"$UNRES" >/dev/null \
    && pass "no thread was resolved" || fail "a thread changed state"
  ADDR=$(jq -r .id "$A")
  "$ADE" agent cancel "$ADDR" >/dev/null && pass "a run in review can be discarded (cancel)" || fail "cancel failed"
  git -C "$WT1" checkout -q -- . 2>/dev/null || true
  git -C "$WT1" clean -fdq 2>/dev/null || true
else
  pass "tracker#1's MR has no unresolved threads right now: address check skipped (sandbox state)"
fi

step "drivers: claude and codex are unavailable here, and doctor says so"
"$ADE" doctor --json >"$VERIFY_TMP/doctor.json" || true
for d in claude codex; do
  if command -v "$d" >/dev/null; then
    pass "$d is installed here: not asserting unavailable"
  else
    expect_json "doctor: $d unavailable" "$VERIFY_TMP/doctor.json" "[.[] | select(.name == \"$d\")][0].detail | startswith(\"unavailable\")"
  fi
done
"$ADE" agent plan "$REF" --repo api --driver codex --yes >"$VERIFY_TMP/codex.out" 2>&1 && fail "codex ran although it is not installed" || true
grep -qiE "unavailable|no price is known" "$VERIFY_TMP/codex.out" && pass "--driver codex refuses cleanly: $(tail -1 "$VERIFY_TMP/codex.out" | cut -c1-100)" || { cat "$VERIFY_TMP/codex.out" >&2; fail "no clean refusal for codex"; }

step "spend"
printf '  total omp spend this run (reported by omp, list price): $%s across the runs above (each capped at $%s)\n' "$SPEND" "$CAP"
[[ $(jq -n --argjson s "$SPEND" '$s < 1.5') == true ]] && pass "under the \$1.50 budget" || fail "spent \$$SPEND"
