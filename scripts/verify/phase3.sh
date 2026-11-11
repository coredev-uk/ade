#!/usr/bin/env bash
# Phase 3 acceptance (docs/PLAN.md §8): start work + workspaces.
# Run from the repo root inside `nix develop`. Needs .env with GITLAB_PAT.
#
# The live steps leave their issues and MRs in the sandbox (tracker#3 → api child
# + draft MR, tracker#4 → api child + branch, then the MR from `ade mr create`):
# they are sandbox state now, and a re-run proves idempotency instead of
# creating more. Zellij tabs are never opened here (--no-workspace); the
# workspace checks live in phase3-workspaces.sh, sourced at the end.
. scripts/verify/lib.sh

step "unit and fake-server tests (Free and Premium tiers)"
check "forge write side: GitLab driver + fake; the issueSetIteration document validates against the schema" \
  go test ./internal/forge ./internal/forge/gitlab/...
check "start work: plan, idempotent execute, resume, modes, template quick actions, label sync" \
  go test ./internal/startwork/...

step "live sandbox (gitlab.com/ade-demo-env, Free tier)"
build_ade
sandbox_gitlab
export ADE_SANDBOX_GROUP=ade-demo-env
check "sandbox api has an MR template with /label and /assign_reviewer quick actions" bash scripts/sandbox/gitlab-templates.sh
check "ade project add clones api and web (bare + baseline tree)" \
  "$ADE" project add "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"

API="https://gitlab.com/api/v4"
GRP=ade-demo-env
enc() { jq -rn --arg v "$1" '$v|@uri'; }
# gl PATH [curl args...]: request against the sandbox API, body on stdout. Retries
# 429s (GitLab.com rate-limits per endpoint, and sandbox runs can overlap).
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
# ade_json FILE ARGS...: run ade with --json into FILE; show stderr on failure.
ade_json() {
  local out=$1
  shift
  "$ADE" "$@" --json >"$out" 2>"$VERIFY_TMP/err" || {
    cat "$VERIFY_TMP/err" "$out" >&2
    fail "ade $*"
  }
}
forge_writes() { "$ADE" audit --action 'forge.*' --json | jq 'length'; }
PROJ_API=$(enc "$GRP/api")
PROJ_TRK=$(enc "$GRP/tracker")
SLUG3=retry-failed-webhook-deliveries
SLUG4=remove-the-legacy-csv-export-path

step "dry run: every write and the MR body, nothing changed"
W0=$(forge_writes)
ade_json "$VERIFY_TMP/dry.json" start "$GRP/tracker#8" --dry-run --strip-actions=label
D="$VERIFY_TMP/dry.json"
expect_json "tracker#8 has two app labels: a child is planned in each repo" "$D" \
  '[.steps[] | select(.kind == "child") | .repo] | sort == ["api","web"]'
expect_json "the MR body is previewed with Closes for the not-yet-created child" "$D" \
  '[.previews[] | select(.repo == "api")][0] | (.body | contains("Closes <child issue, created first>")) and (.body | contains("Part of ade-demo-env/tracker#8"))'
expect_json "the template's /label quick action is listed and marked for stripping" "$D" \
  '[.previews[] | select(.repo == "api")][0] | (.quick_actions | map(.name) | contains(["label"])) and .stripped == ["label"]'
expect_json "stripped, the body no longer carries the quick action" "$D" \
  '[.previews[] | select(.repo == "api")][0].body | contains("/label") | not'
expect_json "the template's /assign_reviewer is listed too, and kept (only /label was stripped)" "$D" \
  '[.previews[] | select(.repo == "api")][0] | (.quick_actions | map(.name) | contains(["assign_reviewer"])) and .stripped == ["label"] and (.body | contains("/assign_reviewer"))'
[[ $(forge_writes) == "$W0" ]] && pass "dry run wrote nothing to the forge (audit)" || fail "dry run audited forge writes"

step "start work on tracker#3 (repo api): linked child + draft MR + worktree"
ade_json "$VERIFY_TMP/s3a.json" start "$GRP/tracker#3" --repo api --no-workspace
A="$VERIFY_TMP/s3a.json"
expect_json "child, branch, MR and worktree are reported" "$A" \
  '.targets[0] | .child != null and .mr != null and .worktree != null and (.branch | endswith("-'"$SLUG3"'"))'
CHILD3=$(jq -r '.targets[0].child' "$A")
CHILD3_IID=${CHILD3##*#}
MR3=$(jq -r '.targets[0].mr' "$A")
MR3_IID=${MR3##*!}
BR3=$(jq -r '.targets[0].branch' "$A")
WT3=$(jq -r '.targets[0].worktree' "$A")
gl "/projects/$PROJ_API/issues/$CHILD3_IID" >"$VERIFY_TMP/child3.json"
expect_json "child $CHILD3 names its parent and copies the type label, assignee and milestone" "$VERIFY_TMP/child3.json" \
  '(.description | startswith("Parent: ade-demo-env/tracker#3")) and (.labels | index("type::story") != null) and ([.assignees[].username] | length > 0) and .milestone.title == "Sprint 42"'
gl "/projects/$PROJ_TRK/issues/3/links" >"$VERIFY_TMP/links3.json"
expect_json "tracker#3 relates_to the child (link on the forge)" "$VERIFY_TMP/links3.json" \
  "map(select(.iid == $CHILD3_IID and .link_type == \"relates_to\")) | length == 1"
gl "/projects/$PROJ_TRK/issues/3" >"$VERIFY_TMP/tracker3.json"
expect_json "tracker#3 workflow label moved to \"in progress\"" "$VERIFY_TMP/tracker3.json" \
  '.labels | index("workflow::in progress") != null'
gl "/projects/$PROJ_API/merge_requests/$MR3_IID" >"$VERIFY_TMP/mr3.json"
expect_json "MR $MR3 is a draft from $BR3 with Closes, the parent mention and the template" "$VERIFY_TMP/mr3.json" \
  ".draft == true and .source_branch == \"$BR3\" and (.description | contains(\"Closes #$CHILD3_IID\")) and (.description | contains(\"Part of ade-demo-env/tracker#3\")) and (.description | contains(\"## Testing\"))"
expect_json "the template's /label quick action ran on create and left the body" "$VERIFY_TMP/mr3.json" \
  '(.labels | index("type::story") != null) and (.description | contains("/label") | not)'
[[ -d $WT3 ]] && pass "worktree $WT3" || fail "no worktree at $WT3"
[[ $(git -C "$WT3" rev-parse --abbrev-ref --symbolic-full-name '@{u}') == "origin/$BR3" ]] \
  && pass "worktree tracks origin/$BR3" || fail "worktree has no upstream"
if jq -e '[.steps[] | select(.outcome == "created" and .write == true)] | length > 0' "$A" >/dev/null; then
  "$ADE" audit --item "$GRP/tracker#3" --json >"$VERIFY_TMP/audit3.json"
  expect_json "every forge write was audited" "$VERIFY_TMP/audit3.json" \
    '[.[].action] as $a | ["forge.issue.create","forge.issue.labels","forge.issue.link","forge.branch.create","forge.mr.create"] | all(. as $x | $a | index($x) != null)'
else
  pass "work was already started by an earlier run (sandbox state): audit check skipped"
fi

step "re-run is a no-op with the same IDs"
W1=$(forge_writes)
ade_json "$VERIFY_TMP/s3b.json" start "$GRP/tracker#3" --repo api --no-workspace
B="$VERIFY_TMP/s3b.json"
expect_json "nothing created; child, MR, branch and worktree unchanged" "$B" \
  "([.steps[] | select(.outcome == \"created\")] | length == 0) and .targets[0].child == \"$CHILD3\" and .targets[0].mr == \"$MR3\" and .targets[0].branch == \"$BR3\" and .targets[0].worktree == \"$WT3\""
[[ $(forge_writes) == "$W1" ]] && pass "no forge write audited" || fail "re-run wrote to the forge"
gl "/projects/$PROJ_API/issues?state=all&per_page=100" >"$VERIFY_TMP/api-issues.json"
expect_json "exactly one api issue names tracker#3 as parent" "$VERIFY_TMP/api-issues.json" \
  '[.[] | select(.description | startswith("Parent: ade-demo-env/tracker#3"))] | length == 1'

step "Branch only on tracker#4: no MR, then \`ade mr create\` from the template"
# A stale workflow label proves the old one is removed explicitly (Free keeps
# every ::-label). Only seeded while the item is unstarted, so re-runs stay quiet.
gl "/projects/$PROJ_TRK/issues/4/links" >"$VERIFY_TMP/links4.json"
if [[ $(jq length "$VERIFY_TMP/links4.json") == 0 ]]; then
  gl "/projects/$PROJ_TRK/issues/4" -X PUT -H 'Content-Type: application/json' \
    --data '{"add_labels":"workflow::backlog"}' >/dev/null
  pass "seeded workflow::backlog on tracker#4 (unstarted)"
fi
ade_json "$VERIFY_TMP/s4.json" start "$GRP/tracker#4" --repo api --mode branch --no-workspace
S4="$VERIFY_TMP/s4.json"
expect_json "child, branch and worktree reported; no MR step in the plan" "$S4" \
  '(.targets[0] | .child != null and .branch != null and .worktree != null) and ([.steps[] | select(.kind == "mr")] | length == 0)'
CHILD4=$(jq -r '.targets[0].child' "$S4")
BR4=$(jq -r '.targets[0].branch' "$S4")
[[ $BR4 == *-"$SLUG4" ]] && pass "branch follows {child_iid}-{slug}: $BR4" || fail "branch name $BR4"
gl "/projects/$PROJ_TRK/issues/4" >"$VERIFY_TMP/tracker4.json"
expect_json "tracker#4: workflow label moved, the old one removed explicitly" "$VERIFY_TMP/tracker4.json" \
  '(.labels | index("workflow::in progress") != null) and (.labels | index("workflow::backlog") == null)'
gl "/projects/$PROJ_API/merge_requests?source_branch=$(enc "$BR4")&state=all" >"$VERIFY_TMP/mrs4.json"
if jq -e 'length == 0' "$VERIFY_TMP/mrs4.json" >/dev/null; then
  pass "Branch only created no MR on $BR4"
  FIRST_MR=1
else
  pass "an earlier run already created the MR (sandbox state)"
  FIRST_MR=0
fi
ade_json "$VERIFY_TMP/m4a.json" mr create "$GRP/tracker#4" --repo api
M="$VERIFY_TMP/m4a.json"
MR4=$(jq -r '.targets[0].mr' "$M")
MR4_IID=${MR4##*!}
expect_json "ade mr create opens the MR for $BR4 (only the MR is created)" "$M" \
  '.targets[0].mr != null and ([.steps[] | select(.outcome == "created") | .kind] | all(. == "mr"))'
gl "/projects/$PROJ_API/merge_requests/$MR4_IID" >"$VERIFY_TMP/mr4.json"
expect_json "MR $MR4 is a draft built from the template with Closes for the child" "$VERIFY_TMP/mr4.json" \
  ".draft == true and (.description | contains(\"Closes #${CHILD4##*#}\")) and (.description | contains(\"## Testing\")) and (.labels | index(\"type::story\") != null)"
if [[ $FIRST_MR == 1 ]]; then
  expect_json "the MR was created by this run" "$M" '[.steps[] | select(.kind == "mr" and .outcome == "created")] | length == 1'
fi
ade_json "$VERIFY_TMP/m4b.json" mr create "$GRP/tracker#4" --repo api
expect_json "a second mr create finds the same MR and creates nothing" "$VERIFY_TMP/m4b.json" \
  ".targets[0].mr == \"$MR4\" and ([.steps[] | select(.outcome == \"created\")] | length == 0)"
if out=$("$ADE" mr create "$GRP/tracker#6" --repo api 2>&1); then
  fail "mr create succeeded for work that was never started"
fi
grep -q 'ade start' <<<"$out" && pass "mr create without a branch fails and says to run ade start" || fail "unhelpful error: $out"

step "/assign_reviewer: listed, and stripped it leaves the MR without reviewers"
# Two untouched tracker issues (created once): one is started with the quick action stripped,
# the other with it kept. The sandbox token's user is the template's reviewer; GitLab Free
# lets an author request their own review, so both outcomes are visible on real MRs.
SEEDED=$(bash scripts/sandbox/gitlab-reviewer-issues.sh) || fail "gitlab-reviewer-issues.sh failed"
eval "$SEEDED"
ME=$(gl "/user" | jq -r .username)
# review_start IID [FLAGS...]: start work on tracker#IID in api (no workspace); JSON to $VERIFY_TMP/rv-IID.json.
review_start() {
  local iid=$1
  shift
  ade_json "$VERIFY_TMP/rv-$iid.json" start "$GRP/tracker#$iid" --repo api --no-workspace "$@"
}
ade_json "$VERIFY_TMP/rv-dry.json" start "$GRP/tracker#$STRIP_IID" --repo api --no-workspace --dry-run --strip-actions=assign_reviewer
if jq -e '.previews | length > 0' "$VERIFY_TMP/rv-dry.json" >/dev/null; then
  expect_json "dry run on untouched tracker#$STRIP_IID lists /assign_reviewer @$ME as stripped (and /label as kept)" "$VERIFY_TMP/rv-dry.json" \
    "[.previews[] | select(.repo == \"api\")][0] | (.quick_actions | map(select(.name == \"assign_reviewer\") | .line) | any(contains(\"@$ME\"))) and .stripped == [\"assign_reviewer\"] and (.body | contains(\"/assign_reviewer\") | not) and (.body | contains(\"/label\"))"
else
  pass "tracker#$STRIP_IID was started by an earlier run (sandbox state): the listing is checked on tracker#8 above"
fi
review_start "$STRIP_IID" --strip-actions=assign_reviewer
RMR=$(jq -r '.targets[0].mr' "$VERIFY_TMP/rv-$STRIP_IID.json")
gl "/projects/$PROJ_API/merge_requests/${RMR##*!}" >"$VERIFY_TMP/rv-strip-mr.json"
expect_json "stripped: MR $RMR has no reviewers, and the line is gone from its body" "$VERIFY_TMP/rv-strip-mr.json" \
  '(.reviewers | length) == 0 and (.description | contains("/assign_reviewer") | not)'
expect_json "stripped: the template's other quick action (/label) still ran" "$VERIFY_TMP/rv-strip-mr.json" \
  '.labels | index("type::story") != null'
review_start "$KEEP_IID"
RMK=$(jq -r '.targets[0].mr' "$VERIFY_TMP/rv-$KEEP_IID.json")
gl "/projects/$PROJ_API/merge_requests/${RMK##*!}" >"$VERIFY_TMP/rv-keep-mr.json"
expect_json "kept: MR $RMK requests review from @$ME, the template's reviewer" "$VERIFY_TMP/rv-keep-mr.json" \
  "([.reviewers[].username] == [\"$ME\"]) and (.description | contains(\"/assign_reviewer\") | not)"
W3=$(forge_writes)
review_start "$STRIP_IID" --strip-actions=assign_reviewer
review_start "$KEEP_IID"
expect_json "re-run finds the same MRs and creates nothing" "$VERIFY_TMP/rv-$KEEP_IID.json" \
  ".targets[0].mr == \"$RMK\" and ([.steps[] | select(.outcome == \"created\")] | length == 0)"
[[ $(forge_writes) == "$W3" ]] && pass "no forge write audited by the re-runs" || fail "re-runs wrote to the forge"

step "Browse makes no forge write"
W2=$(forge_writes)
ade_json "$VERIFY_TMP/browse.json" start "$GRP/tracker#6" --repo api --mode browse --no-workspace
expect_json "worktree on the default branch; no child, branch or MR" "$VERIFY_TMP/browse.json" \
  '(.targets[0].worktree | endswith("/api/main")) and .targets[0].child == null and .targets[0].mr == null'
[[ $(forge_writes) == "$W2" ]] && pass "audit shows no forge write for Browse" || fail "Browse audited a forge write"
gl "/projects/$PROJ_TRK/issues/6" >"$VERIFY_TMP/tracker6.json"
expect_json "tracker#6 was not assigned or labelled" "$VERIFY_TMP/tracker6.json" \
  '(.assignees | length) == 0 and (.labels | map(select(startswith("workflow::"))) | length) == 0'

if [[ ${ADE_PHASE3_SKIP_WORKSPACES:-0} == 1 ]]; then
  pass "workspace checks skipped (ADE_PHASE3_SKIP_WORKSPACES=1)"
elif [[ -f scripts/verify/phase3-workspaces.sh ]]; then
  # shellcheck source=/dev/null
  . scripts/verify/phase3-workspaces.sh
else
  pass "workspace checks not present yet (scripts/verify/phase3-workspaces.sh)"
fi

printf '\nPhase 3: all checks passed\n'
