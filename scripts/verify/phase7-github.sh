#!/usr/bin/env bash
# Phase 7, GitHub half (docs/PLAN.md §8 row 7): the same Home / MR / Start flows on the GitHub
# sandbox (github.com/coredev-uk/ade-sandbox-{tracker,api,web}, seeded by
# scripts/sandbox/github-seed.sh), through the CLI.
#
# Sourced by phase7.sh; also runs standalone from the repo root inside `nix develop`:
#   bash scripts/verify/phase7-github.sh
# Needs `gh` logged in with the repo scope (ADE reads its token with `gh auth token`), git with
# SSH access to the sandbox repos, and jq. Never touches ~/.config/ade: state is isolated.
#
# The live steps leave sandbox state behind, as the GitLab scripts do: tracker "Remove the legacy
# CSV export path" gets its child issue, branch, draft PR and worktree (a re-run proves idempotency
# instead of creating more) and every run adds one review of five resolved threads to the rename PR.
# The draft PR is restored to draft by the end (and on failure).
if ! declare -F step >/dev/null; then
  . scripts/verify/lib.sh
  GH_STANDALONE=1
fi

OWNER=${ADE_SANDBOX_OWNER:-coredev-uk}
PREFIX=${ADE_SANDBOX_PREFIX:-ade-sandbox}
TRK=$OWNER/$PREFIX-tracker
API=$OWNER/$PREFIX-api
WEB=$OWNER/$PREFIX-web

step "unit and fake-server tests"
check "GitHub driver against the fake server: reads, review batching (one pending review), writes, rate limits" \
  go test ./internal/forge/github/...
check "sync against the GitHub fake: sprints from the tracker repo's milestones, sub-issue children, court shapes" \
  go test ./internal/forgesync/ -run 'TestSyncAgainstGitHub|TestOpenFactory'
check "start work: plan, idempotent execute, GitHub template discovery" go test ./internal/startwork/...
check "review: drafts persist, one submit, positions on renamed files" go test ./internal/review/...

step "live sandbox ($OWNER/$PREFIX-*, GitHub)"
command -v gh >/dev/null || fail "gh is not installed"
gh auth status >/dev/null 2>&1 || fail "gh is not logged in (gh auth login)"
if [[ -z ${ADE:-} || ! -x ${ADE:-} ]]; then build_ade; fi

GH_DIR="$VERIFY_TMP/github"
mkdir -p "$GH_DIR"
# The isolated config: one forge (token from `gh auth token`), the GitHub sandbox project only.
sandbox_github() {
  export XDG_DATA_HOME="$GH_DIR/data" XDG_CACHE_HOME="$GH_DIR/cache"
  export ADE_CONFIG="$GH_DIR/config.toml"
  cat >"$ADE_CONFIG" <<EOF
[user]
handle = "sandbox"
[forges.sandbox-github]
kind = "github"
token_cmd = "gh auth token"
git_protocol = "ssh"
[paths]
root = "$GH_DIR/projects"
[[projects]]
source = "$REPO/scripts/sandbox/manifests/github.ade.toml"
EOF
}
sandbox_github

ade_json() { # FILE ARGS...: run ade with --json into FILE; show stderr on failure
  local out=$1
  shift
  "$ADE" "$@" --json >"$out" 2>"$GH_DIR/err" || {
    cat "$GH_DIR/err" "$out" >&2
    fail "ade $*"
  }
}
audit_count() { "$ADE" audit --action "$1" --json | jq 'length'; }
forge_writes() { "$ADE" audit --action 'forge.*' --json | jq 'length'; }
# pr_number REPO TITLE: the number of the PR with that title.
pr_number() {
  gh api "repos/$1/pulls?state=all&per_page=100" --jq "map(select(.title == \"$2\"))[0].number // empty"
}
issue_number() { # REPO TITLE
  gh api "repos/$1/issues?state=all&per_page=100" --jq "map(select(.pull_request | not) | select(.title == \"$2\"))[0].number // empty"
}

RENAME_N=$(pr_number "$API" "Rename bucket to token bucket")
RATE_N=$(pr_number "$API" "Rate-limit the export endpoint")
DRAFT_N=$(pr_number "$WEB" "Paginate the audit log view")
[[ -n $RENAME_N && -n $RATE_N && -n $DRAFT_N ]] || fail "the sandbox is not seeded (run scripts/sandbox/github-seed.sh)"
RENAME=$API!$RENAME_N
RATE=$API!$RATE_N
DRAFTPR=$WEB!$DRAFT_N
gh_cleanup() {
  # Leave the sandbox as found: the web PR a draft, no approval of ours (own PRs cannot be approved).
  if [[ -n ${ADE:-} && -x ${ADE:-} ]]; then
    ADE_CONFIG="$GH_DIR/config.toml" XDG_DATA_HOME="$GH_DIR/data" XDG_CACHE_HOME="$GH_DIR/cache" \
      "$ADE" mr draft "$DRAFTPR" >/dev/null 2>&1 || true
  fi
}
if [[ -n ${GH_STANDALONE:-} ]]; then
  trap 'gh_cleanup; rm -rf "$VERIFY_TMP"' EXIT
else
  prev=$(trap -p EXIT | sed -E "s/^trap -- '(.*)' EXIT\$/\1/")
  # shellcheck disable=SC2064
  trap "gh_cleanup; $prev" EXIT
fi

step "Home: sync and the work items"
check "ade sync exits 0 against GitHub" "$ADE" sync
"$ADE" items --json >"$GH_DIR/items.json"
"$ADE" sync --json >"$GH_DIR/sync.json"
I="$GH_DIR/items.json"
item() { printf '.[] | select(.issue.title == "%s")' "$1"; }
expect_json "me is the gh user; no iterations, no weight field (free tier)" "$GH_DIR/sync.json" \
  '(.[0].me.username | length > 0) and .[0].caps.iterations == false and .[0].caps.weight == false'
expect_json "Rate-limit: child in api through a sub-issue, its PR, review, you · 2 unresolved threads" "$I" \
  "[$(item 'Rate-limit the export endpoint')] | .[0] | (.children[0].ref | startswith(\"$API#\")) and (.mrs[0].ref == \"$RATE\") and .stage == 5 and .court == \"you\" and .reason == \"2 unresolved threads\""
expect_json "Rate-limit: type story, app api, weight 5 from the label" "$I" \
  "[$(item 'Rate-limit the export endpoint')] | .[0] | .type == \"story\" and .apps == [\"api\"] and .weight == 5"
expect_json "Paginate: draft PR, label drift (in review vs in progress)" "$I" \
  "[$(item 'Paginate the audit log view')] | .[0] | .stage == 3 and .drift == true and .workflow == \"in review\" and .want_workflow == \"in progress\" and (.mrs[0].ref == \"$DRAFTPR\") and .mrs[0].draft == true"
expect_json "Retry webhooks: assigned, you · start work" "$I" \
  "[$(item 'Retry failed webhook deliveries')] | .[0] | .stage == 0 and .reason == \"start work\""
expect_json "timezone: blocked by the workflow label" "$I" "[$(item 'Export timestamps ignore tenant timezone')] | .[0].court == \"blocked\""
expect_json "Billing: unweighted is null, never 0" "$I" "[$(item 'Billing export times out for large tenants')] | .[0].weight == null"
expect_json "stale totals: needs triage (type, app, weight)" "$I" \
  "[$(item 'Exports page sometimes shows stale totals')] | .[0].triage == [\"type\",\"app\",\"weight\"]"
expect_json "Upgrade deps: done" "$I" "[$(item 'Upgrade export worker dependencies')] | .[0].stage == 7"
expect_json "iteration window from milestones: Sprint 42, 2–13 Nov (due date + Start line in the description)" "$GH_DIR/sync.json" \
  '.[0].window.current.title == "Sprint 42" and .[0].window.current.start == "2026-11-02" and .[0].window.current.due == "2026-11-13"'
expect_json "sprint weight totals: 20w across Sprint 42's weighted items" "$I" \
  '[.[] | select(.sprint.title == "Sprint 42" and .weight != null) | .weight] | add == 20'

step "MR: read the PR, the rename resolved"
ade_json "$GH_DIR/show0.json" mr show "$RENAME"
N0=$(jq '.threads | length' "$GH_DIR/show0.json")
ade_json "$GH_DIR/diff.json" mr diff "$RENAME"
D="$GH_DIR/diff.json"
expect_json "the PR's diff is one renamed file: limit/bucket.go -> limit/token_bucket.go" "$D" \
  '(.files | length) == 1 and .files[0].renamed == true and .files[0].old_path == "limit/bucket.go" and .files[0].new_path == "limit/token_bucket.go"'
expect_json "the diff carries the head SHA a comment position needs" "$D" '(.head_sha | length) == 40'
ade_json "$GH_DIR/showrate.json" mr show "$RATE"
expect_json "Rate-limit PR: 2 unresolved threads and 1 resolved, on limit/bucket.go; ready" "$GH_DIR/showrate.json" \
  '([.threads[] | select(.resolvable and (.resolved | not))] | length) == 2 and ([.threads[] | select(.resolved)] | length) == 1 and .mr.draft == false and ([.threads[] | select(.resolvable) | .position.path] | unique == ["limit/bucket.go"])'

step "review: five line comments on the renamed file, one pending review"
TAG="p7-$$-$(date +%s)"
F=limit/token_bucket.go
# The seeded rename hunk is "@@ -4,4 +4,6 @@": new lines 4-6 and 9 are context, 7 and 8 added.
ade_json "$GH_DIR/list0.json" review list "$RENAME"
expect_json "no leftover drafts in the isolated cache" "$GH_DIR/list0.json" 'length == 0'
check "draft 1: unchanged line"   "$ADE" review add "$RENAME" --path $F --line 5 --severity question   --body "[$TAG] Why Rate and not PerMinute?"
check "draft 2: added blank line" "$ADE" review add "$RENAME" --path $F --line 7 --severity nit        --body "[$TAG] Blank line before the new field."
check "draft 3: added field"      "$ADE" review add "$RENAME" --path $F --line 8 --severity suggestion --body "[$TAG] Use an int64 count of milli-tokens to avoid float drift."
check "draft 4: closing brace"    "$ADE" review add "$RENAME" --path $F --line 9 --severity blocking   --body "[$TAG] Nothing consumes a token yet."
check "draft 5: old side"         "$ADE" review add "$RENAME" --path $F --line 6 --old --severity question --body "[$TAG] Is Burst the right name?"
ade_json "$GH_DIR/list1.json" review list "$RENAME"
expect_json "five drafts are saved locally" "$GH_DIR/list1.json" 'length == 5'
ade_json "$GH_DIR/show1.json" mr show "$RENAME"
expect_json "none reached the forge before submit" "$GH_DIR/show1.json" ".threads | length == $N0"
check "a comment outside the diff is refused before any request" bash -c "! $ADE review add '$RENAME' --path $F --line 99 --body x"
REV0=$(gh api "repos/$API/pulls/$RENAME_N/reviews?per_page=100" --jq 'length')
W0=$(audit_count 'forge.draftnote.publish')

step "submit: one review, one notification"
ade_json "$GH_DIR/dry.json" review submit "$RENAME" --dry-run
expect_json "dry run validates the five positions and posts nothing" "$GH_DIR/dry.json" '.dry_run == true and .uploaded == 5'
[[ $(gh api "repos/$API/pulls/$RENAME_N/reviews?per_page=100" --jq 'length') == "$REV0" ]] && pass "no review exists after the dry run" || fail "the dry run created a review"
ade_json "$GH_DIR/submit.json" review submit "$RENAME" --summary "[$TAG] Five notes on the rename."
expect_json "five comments uploaded and published together, plus the summary" "$GH_DIR/submit.json" '.uploaded == 6 and .published == 6 and .approved == false'
[[ $(audit_count 'forge.draftnote.publish') == $((W0 + 1)) ]] && pass "exactly one publish in the audit log (one notification)" || fail "publish count"
REVS=$(gh api "repos/$API/pulls/$RENAME_N/reviews?per_page=100")
[[ $(jq length <<<"$REVS") == $((REV0 + 1)) ]] && pass "one new review on the PR for six drafts" || fail "reviews: $(jq length <<<"$REVS"), was $REV0"
NEW=$(jq -r "map(select(.body | contains(\"[$TAG]\")))[0].id // empty" <<<"$REVS")
[[ -n $NEW ]] || fail "the summary is not the new review's body"
[[ $(jq -r "map(select(.id == $NEW))[0].state" <<<"$REVS") == COMMENTED ]] && pass "submitted as a COMMENT review carrying the summary" || fail "review state"
[[ $(gh api "repos/$API/pulls/$RENAME_N/reviews/$NEW/comments?per_page=100" --jq length) == 5 ]] && pass "the review holds the five comments" || fail "comments in the review"
ade_json "$GH_DIR/list2.json" review list "$RENAME"
expect_json "published drafts are gone from the local store" "$GH_DIR/list2.json" 'length == 0'

step "read back: five threads, positions on the renamed file"
ade_json "$GH_DIR/show2.json" mr show "$RENAME"
S="$GH_DIR/show2.json"
expect_json "five new threads and the review summary" "$S" ".threads | length == $N0 + 6"
mine() { printf '[.threads[] | select(.notes[0].body | contains("[%s] %s"))][0]' "$TAG" "$1"; }
# Old line 6 is an unchanged (context) line, so it is the same line on both sides: GitHub anchors it
# on the new side (there is no removed line in the seeded rename hunk).
for spec in "Why Rate|5|0" "Blank line before|7|0" "Use an int64|8|0" "Nothing consumes|9|0" "Is Burst the right|6|0"; do
  IFS='|' read -r needle nl ol <<<"$spec"
  expect_json "'$needle…': new line $nl (old line $ol), on limit/token_bucket.go" "$S" \
    "$(mine "$needle") | .position.path == \"$F\" and (.position.new_line // 0) == $nl and (.position.old_line // 0) == $ol"
done
expect_json "severity leads the body (**nit:** ...)" "$S" "$(mine 'Blank line before') | .notes[0].body | startswith(\"**nit:** \")"

step "reply, resolve, reopen"
TID=$(jq -r "$(mine 'Nothing consumes') | .id" "$S")
check "reply in the thread" "$ADE" mr reply "$RENAME" "$TID" "[$TAG] Agreed, fixing."
check "resolve" "$ADE" mr resolve "$RENAME" "$TID"
ade_json "$GH_DIR/show3.json" mr show "$RENAME"
expect_json "reply is in the thread and it is resolved" "$GH_DIR/show3.json" \
  "[.threads[] | select(.id == \"$TID\")][0] | .resolved == true and (.notes | length) == 2"
check "reopen" "$ADE" mr resolve "$RENAME" "$TID" --undo
ade_json "$GH_DIR/show4.json" mr show "$RENAME"
expect_json "reopened" "$GH_DIR/show4.json" "[.threads[] | select(.id == \"$TID\")][0].resolved == false"
for id in $(jq -r "[.threads[] | select(.notes[0].body | contains(\"[$TAG]\")) | select(.resolvable) | .id][]" "$GH_DIR/show4.json"); do
  "$ADE" mr resolve "$RENAME" "$id" >/dev/null # leave this run's threads resolved
done
ade_json "$GH_DIR/show5.json" mr show "$RENAME"
expect_json "this run's five threads are resolved again" "$GH_DIR/show5.json" \
  "[.threads[] | select(.resolvable and (.notes[0].body | contains(\"[$TAG]\"))) | .resolved] | length == 5 and all"

step "ready/draft"
check "the draft PR marked ready" "$ADE" mr ready "$DRAFTPR"
ade_json "$GH_DIR/web.json" mr show "$DRAFTPR"
expect_json "it is ready" "$GH_DIR/web.json" '.mr.draft == false'
check "back to draft" "$ADE" mr draft "$DRAFTPR"
ade_json "$GH_DIR/web.json" mr show "$DRAFTPR"
expect_json "it is a draft again" "$GH_DIR/web.json" '.mr.draft == true'
if "$ADE" mr approve "$RENAME" >"$GH_DIR/approve.out" 2>&1; then
  fail "approved our own PR (GitHub refuses that)"
fi
grep -qi 'own pull request' "$GH_DIR/approve.out" && pass "approving our own PR is refused with GitHub's reason" || { cat "$GH_DIR/approve.out" >&2; fail "unhelpful approve error"; }

step "every forge write is audited"
for a in forge.draftnote.add forge.draftnote.publish forge.thread.reply forge.thread.resolve forge.mr.draft review.submit; do
  [[ $(audit_count "$a") -ge 1 ]] && pass "audit has $a" || fail "audit lacks $a"
done

step "Start: clone, then start work on 'Remove the legacy CSV export path'"
check "ade project add clones api and web (bare + baseline tree)" "$ADE" project add "$REPO/scripts/sandbox/manifests/github.ade.toml"
CSV_N=$(issue_number "$TRK" "Remove the legacy CSV export path")
[[ -n $CSV_N ]] || fail "tracker issue 'Remove the legacy CSV export path' is missing"
CSV=$TRK#$CSV_N
SLUG=remove-the-legacy-csv-export-path

# The dry run uses an item this script never starts, so the plan always has work to preview.
WEBHOOK_N=$(issue_number "$TRK" "Retry failed webhook deliveries")
[[ -n $WEBHOOK_N ]] || fail "tracker issue 'Retry failed webhook deliveries' is missing"
W1=$(forge_writes)
ade_json "$GH_DIR/dry.json" start "$TRK#$WEBHOOK_N" --repo api --dry-run
expect_json "dry run: a child is planned in api and the PR body is previewed with the repo's template" "$GH_DIR/dry.json" \
  '([.steps[] | select(.kind == "child") | .repo] == ["api"]) and ([.previews[] | select(.repo == "api")][0] | (.body | contains("Checklist")) and (.body | contains("Part of '"$TRK"'#'"$WEBHOOK_N"'")))'
[[ $(forge_writes) == "$W1" ]] && pass "dry run wrote nothing to the forge (audit)" || fail "dry run audited forge writes"

ade_json "$GH_DIR/s1.json" start "$CSV" --repo api --no-workspace
A="$GH_DIR/s1.json"
expect_json "child, branch, PR and worktree are reported" "$A" \
  '.targets[0] | .child != null and .mr != null and .worktree != null and (.branch | endswith("-'"$SLUG"'"))'
CHILD=$(jq -r '.targets[0].child' "$A")
CHILD_N=${CHILD##*#}
PRREF=$(jq -r '.targets[0].mr' "$A")
PR_N=${PRREF##*!}
BR=$(jq -r '.targets[0].branch' "$A")
WT=$(jq -r '.targets[0].worktree' "$A")
gh api "repos/$API/issues/$CHILD_N" >"$GH_DIR/child.json"
expect_json "child $CHILD names its parent, copies the type label and milestone, and is assigned" "$GH_DIR/child.json" \
  '(.body | startswith("Parent: '"$TRK"'#'"$CSV_N"'")) and ([.labels[].name] | index("type:story") != null) and (.assignees | length > 0) and .milestone.title == "Sprint 42"'
gh api "repos/$TRK/issues/$CSV_N/sub_issues" >"$GH_DIR/subs.json"
expect_json "the child is a sub-issue of the tracker issue (the child link)" "$GH_DIR/subs.json" \
  "map(select(.number == $CHILD_N and (.repository_url | endswith(\"/$API\")))) | length == 1"
gh api "repos/$TRK/issues/$CSV_N" >"$GH_DIR/tracker.json"
expect_json "the tracker's workflow label moved to \"in progress\"" "$GH_DIR/tracker.json" '[.labels[].name] | index("workflow:in progress") != null'
gh api "repos/$API/pulls/$PR_N" >"$GH_DIR/pr.json"
expect_json "PR $PRREF is a draft from $BR with Closes, the parent mention and the template" "$GH_DIR/pr.json" \
  ".draft == true and .head.ref == \"$BR\" and (.body | contains(\"Closes #$CHILD_N\")) and (.body | contains(\"Part of $TRK#$CSV_N\")) and (.body | contains(\"Checklist\"))"
[[ -d $WT ]] && pass "worktree $WT" || fail "no worktree at $WT"
[[ $(git -C "$WT" rev-parse --abbrev-ref --symbolic-full-name '@{u}') == "origin/$BR" ]] &&
  pass "worktree tracks origin/$BR" || fail "worktree has no upstream"
if jq -e '[.steps[] | select(.outcome == "created" and (.kind == "child" or .kind == "branch" or .kind == "mr"))] | length == 3' "$A" >/dev/null; then
  "$ADE" audit --item "$CSV" --json >"$GH_DIR/audit1.json"
  expect_json "every forge write was audited" "$GH_DIR/audit1.json" \
    '[.[].action] as $a | ["forge.issue.create","forge.issue.labels","forge.issue.link","forge.branch.create","forge.mr.create"] | all(. as $x | $a | index($x) != null)'
else
  pass "work was already started by an earlier run (sandbox state): audit check skipped"
fi

step "re-run is a no-op with the same IDs"
W2=$(forge_writes)
ade_json "$GH_DIR/s2.json" start "$CSV" --repo api --no-workspace
expect_json "nothing created; child, PR, branch and worktree unchanged" "$GH_DIR/s2.json" \
  "([.steps[] | select(.outcome == \"created\")] | length == 0) and .targets[0].child == \"$CHILD\" and .targets[0].mr == \"$PRREF\" and .targets[0].branch == \"$BR\" and .targets[0].worktree == \"$WT\""
[[ $(forge_writes) == "$W2" ]] && pass "no forge write audited" || fail "re-run wrote to the forge"
[[ $(gh api "repos/$API/issues?state=all&per_page=100" --jq "[.[] | select((.pull_request | not) and (.body // \"\" | startswith(\"Parent: $TRK#$CSV_N\")))] | length") == 1 ]] &&
  pass "exactly one api issue names the tracker issue as parent" || fail "duplicate child issues"
[[ $(gh api "repos/$API/pulls?state=all&head=$OWNER:$BR&per_page=100" --jq length) == 1 ]] && pass "exactly one PR for the branch" || fail "duplicate PRs"
check "ade sync after the start" "$ADE" sync
ade_json "$GH_DIR/after.json" items
expect_json "Home now shows the started item with its child and draft PR" "$GH_DIR/after.json" \
  "[$(item 'Remove the legacy CSV export path')] | .[0] | (.children[0].ref == \"$CHILD\") and (.mrs[0].ref == \"$PRREF\") and .mrs[0].draft == true"

gh_cleanup
if [[ -n ${GH_STANDALONE:-} ]]; then
  printf '\nPhase 7 (GitHub): all checks passed\n'
fi
