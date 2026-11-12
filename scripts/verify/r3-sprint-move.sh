#!/usr/bin/env bash
# Round 3 acceptance: `ade issue move`, carrying an issue (and its child) to the
# next sprint. Run from the repo root inside `nix develop`. The live part needs
# .env with GITLAB_PAT (GitLab sandbox group ade-demo-env, Free tier: sprints are
# milestones); the unit part runs the fake forge in both tiers (milestones on
# Free, iterations on Premium) and the TUI dialog.
#
# Live: makes sure the milestone `Sprint 43` (2026-11-16..2026-11-27) exists,
# creates a NEW synthetic tracker issue in Sprint 42, starts work on it
# (`ade start --no-workspace`: a child issue and branch), moves it to the next
# sprint with its child, checks the forge, moves it back, then closes the
# synthetic issues and deletes the branch (the script does, through the API:
# ADE never closes issues). "next" is relative to today, so run it while Sprint 42
# is current or just over (it is Sprint 43's turn after 2026-11-13; the script
# says so when today is past Sprint 43's start).
. scripts/verify/lib.sh

step "unit and fake-server tests (Free milestones, Premium iterations, label sprints)"
check "forge: MilestoneWriter on the GitLab and GitHub drivers and their fakes" \
  go test ./internal/forge/gitlab/... ./internal/forge/github/...
check "sprintmove: plan, children, no next sprint, audit, event, both tiers" go test ./internal/sprintmove/...
check "forgesync: a recorded move is not announced again by the next sync" go test ./internal/forgesync/ -run 'RecordSprintMove'
check "TUI: Home > dialog, the Sprint review page > and M" go test ./internal/tui/ -run 'SprintMove|SprintReview'
check "cli: ade issue move registered" go test ./internal/cli/ -run 'IssueMove'

step "live sandbox (gitlab.com/ade-demo-env, Free tier)"
build_ade
sandbox_gitlab
API="https://gitlab.com/api/v4"
GRP=ade-demo-env
enc() { jq -rn --arg v "$1" '$v|@uri'; }
# gl PATH [curl args...]: request against the sandbox API, body on stdout. Retries 429s.
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
ade_json() {
  local out=$1
  shift
  "$ADE" "$@" --json >"$out" 2>"$VERIFY_TMP/err" || {
    cat "$VERIFY_TMP/err" "$out" >&2
    fail "ade $*"
  }
}
PROJ_TRK=$(enc "$GRP/tracker")
milestone_of() { # PROJECT_PATH IID -> milestone title ("" for none)
  gl "/projects/$(enc "$1")/issues/$2" | jq -r '.milestone.title // ""'
}

check "ade project add clones api and web (bare + baseline tree)" \
  "$ADE" project add "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"

step "milestones: Sprint 42 exists; Sprint 43 (2026-11-16..2026-11-27) is created when missing"
find_milestone() { # TITLE -> the milestone object, or empty
  gl "/groups/$GRP/milestones?title=$(enc "$1")&include_ancestors=false" | jq -c '.[0] // empty'
}
M42=$(find_milestone "Sprint 42")
[[ -n $M42 ]] || fail "the sandbox has no Sprint 42 milestone: run scripts/sandbox/gitlab-seed.sh"
M43=$(find_milestone "Sprint 43")
if [[ -z $M43 ]]; then
  M43=$(gl "/groups/$GRP/milestones" -X POST -H 'Content-Type: application/json' \
    -d '{"title":"Sprint 43","start_date":"2026-11-16","due_date":"2026-11-27"}' | jq -c .)
  pass "created milestone Sprint 43"
else
  pass "milestone Sprint 43 is there"
fi
ID42=$(jq -r .id <<<"$M42")
ID43=$(jq -r .id <<<"$M43")
[[ $(jq -r '.start_date + " " + .due_date' <<<"$M43") == "2026-11-16 2026-11-27" ]] && pass "Sprint 43 runs 2026-11-16..2026-11-27" ||
  fail "Sprint 43 has other dates: $M43"
[[ $(date -u +%F) < 2026-11-16 ]] || echo "  note: today is past Sprint 43's start: 'next' from Sprint 42 is then Sprint 43 only while it has not ended"

step "a NEW synthetic tracker issue in Sprint 42, started with a child issue"
ME=$(gl /user | jq -r .id)
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
gl "/projects/$PROJ_TRK/issues" -X POST -H 'Content-Type: application/json' -d "$(jq -n \
  --arg t "r3 sprint move $STAMP (synthetic)" --argjson m "$ID42" --argjson me "$ME" \
  '{title:$t, description:"Synthetic issue for scripts/verify/r3-sprint-move.sh.", labels:"type::story,app::api", milestone_id:$m, assignee_ids:[$me]}')" \
  >"$VERIFY_TMP/issue.json"
IID=$(jq -r .iid "$VERIFY_TMP/issue.json")
TRACKER="$GRP/tracker#$IID"
pass "created $TRACKER in Sprint 42"
ade_json "$VERIFY_TMP/start.json" start "$TRACKER" --repo api --mode branch --no-workspace
CHILD=$(jq -r '.targets[0].child' "$VERIFY_TMP/start.json")
BRANCH=$(jq -r '.targets[0].branch' "$VERIFY_TMP/start.json")
[[ $CHILD == "$GRP/api#"* ]] && pass "start work made the child $CHILD (branch $BRANCH)" || fail "no child in $(cat "$VERIFY_TMP/start.json")"
CIID=${CHILD##*#}
[[ $(milestone_of "$GRP/tracker" "$IID") == "Sprint 42" ]] && pass "$TRACKER is in Sprint 42" || fail "$TRACKER is not in Sprint 42"
[[ $(milestone_of "$GRP/api" "$CIID") == "Sprint 42" ]] && pass "$CHILD carries Sprint 42 too" || fail "$CHILD has no Sprint 42 milestone"

step "dry run: the writes are listed, nothing is changed"
AUDIT0=$("$ADE" audit --action forge.issue.sprint --json | jq 'length')
ade_json "$VERIFY_TMP/dry.json" issue move "$TRACKER" --dry-run
D="$VERIFY_TMP/dry.json"
expect_json "tracker issue and child are planned, Sprint 42 -> Sprint 43, by milestone" "$D" \
  '.dry_run == true and .to.title == "Sprint 43" and (.results | map(.plan.mode) | unique == ["milestone"]) and (.results | length == 2) and (.results[1].plan.child == true) and (.results[0].plan.from.title == "Sprint 42")'
expect_json "nothing was applied" "$D" '[.results[].applied] | any | not'
[[ $(milestone_of "$GRP/tracker" "$IID") == "Sprint 42" ]] && pass "dry run wrote nothing to the forge" || fail "dry run moved the issue"
[[ $("$ADE" audit --action forge.issue.sprint --json | jq 'length') == "$AUDIT0" ]] && pass "dry run audited nothing" || fail "dry run audited a write"

step "move to the next sprint with the child"
ade_json "$VERIFY_TMP/move.json" issue move "$TRACKER"
M="$VERIFY_TMP/move.json"
expect_json "both writes applied to Sprint 43" "$M" '.to.title == "Sprint 43" and ([.results[].applied] | all) and (.results | length == 2)'
[[ $(milestone_of "$GRP/tracker" "$IID") == "Sprint 43" ]] && pass "API: $TRACKER is in Sprint 43" || fail "API: $TRACKER not moved"
[[ $(milestone_of "$GRP/api" "$CIID") == "Sprint 43" ]] && pass "API: $CHILD followed to Sprint 43" || fail "API: child not moved"
"$ADE" audit --action forge.issue.sprint --json >"$VERIFY_TMP/audit.json"
expect_json "two forge.issue.sprint audit entries, from Sprint 42 to Sprint 43, the second for the child" "$VERIFY_TMP/audit.json" \
  '(length == '"$((AUDIT0 + 2))"') and ([.[] | select(.target == "'"$TRACKER"'")][0].params | .from == "Sprint 42" and .to == "Sprint 43" and .mode == "milestone")'
"$ADE" inbox --json >"$VERIFY_TMP/inbox.json"
expect_json "issue.iteration_changed for $TRACKER is in the inbox right away (no sync ran)" "$VERIFY_TMP/inbox.json" \
  '[.[] | select(.kind == "issue.iteration_changed" and .subject == "'"$TRACKER"'")] | length >= 1'

step "moving again is a no-op; there is no sprint after Sprint 43"
ade_json "$VERIFY_TMP/again.json" issue move "$TRACKER" --to "Sprint 43"
expect_json "already in Sprint 43: nothing written" "$VERIFY_TMP/again.json" '[.results[].applied] | any | not'
if "$ADE" issue move "$TRACKER" >"$VERIFY_TMP/nonext.out" 2>&1; then
  fail "a move past the last sprint succeeded: $(cat "$VERIFY_TMP/nonext.out")"
else
  grep -q "no sprint starts after Sprint 43" "$VERIFY_TMP/nonext.out" && grep -q "create the next milestone in group $GRP" "$VERIFY_TMP/nonext.out" &&
    pass "no next sprint: the error says what to create, ADE created nothing" || fail "unhelpful error: $(cat "$VERIFY_TMP/nonext.out")"
fi
[[ $(gl "/groups/$GRP/milestones?include_ancestors=false&per_page=100" | jq '[.[] | select(.title | test("^Sprint 4[4-9]"))] | length') == 0 ]] &&
  pass "no milestone was created by the move" || fail "a Sprint 44+ milestone appeared"

step "move it back"
ade_json "$VERIFY_TMP/back.json" issue move "$TRACKER" --to "Sprint 42"
[[ $(milestone_of "$GRP/tracker" "$IID") == "Sprint 42" && $(milestone_of "$GRP/api" "$CIID") == "Sprint 42" ]] &&
  pass "API: $TRACKER and $CHILD are back in Sprint 42" || fail "API: not back in Sprint 42"
ade_json "$VERIFY_TMP/leave.json" issue move "$TRACKER" --no-children
expect_json "--no-children moves the tracker issue alone" "$VERIFY_TMP/leave.json" '(.results | length == 1)'
[[ $(milestone_of "$GRP/tracker" "$IID") == "Sprint 43" && $(milestone_of "$GRP/api" "$CIID") == "Sprint 42" ]] &&
  pass "API: the child stayed in Sprint 42" || fail "API: --no-children moved the child"
ade_json "$VERIFY_TMP/all.json" issue move "$TRACKER" --to "Sprint 42" --with-children
[[ $(milestone_of "$GRP/tracker" "$IID") == "Sprint 42" ]] && pass "back in Sprint 42" || fail "not back"

step "clean up the synthetic issues (the script closes them through the API; ADE never closes issues)"
gl "/projects/$(enc "$GRP/api")/issues/$CIID" -X PUT -d state_event=close >/dev/null
gl "/projects/$PROJ_TRK/issues/$IID" -X PUT -d state_event=close >/dev/null
gl "/projects/$(enc "$GRP/api")/repository/branches/$(enc "$BRANCH")" -X DELETE >/dev/null || true
pass "closed $TRACKER and $CHILD, deleted $BRANCH"

printf '\nr3-sprint-move: all checks passed\n'
