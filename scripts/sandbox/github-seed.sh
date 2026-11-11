#!/usr/bin/env bash
# Seed the GitHub sandbox repos with synthetic data for ADE development.
#
# Mirrors the shape of gitlab-seed.sh (same labels, sprints, tracker items, children, PRs/threads).
# Idempotent: re-running finds existing objects (by title / branch) and converges them instead of
# duplicating; it never creates or deletes repositories (they must exist). All content is fictional.
# Needs: gh (authenticated, `repo` scope), jq.
#
# GitHub differences from the GitLab seed:
#   - Labels and milestones are per repo (declared in all three repos), label facets use ':' not '::'.
#   - Milestones have no start date: the first description line is `Start: YYYY-MM-DD`.
#   - Tracker -> child links are real sub-issues (parent: coredev-uk/<prefix>-tracker#N in the body too).
#   - Review threads are line comments of a review; one is resolved through GraphQL.
#
# Env:
#   ADE_SANDBOX_OWNER    repo owner, default: the authenticated user
#   ADE_SANDBOX_PREFIX   repo name prefix, default ade-sandbox (repos <prefix>-tracker/-api/-web)
#   ADE_SANDBOX_PRUNE=1  also delete labels the script does not declare (sandbox repos only)
#
# stdout ends with machine-readable `ADE_SEED_KEY=value` lines (issue / PR numbers).
# shellcheck disable=SC2016  # GraphQL documents contain literal $variables
set -euo pipefail

PREFIX="${ADE_SANDBOX_PREFIX:-ade-sandbox}"
OWNER="${ADE_SANDBOX_OWNER:-$(gh api user --jq .login)}"
TRACKER="$OWNER/$PREFIX-tracker"
API_R="$OWNER/$PREFIX-api"
WEB_R="$OWNER/$PREFIX-web"
REPOS=("$TRACKER" "$API_R" "$WEB_R")

# Sprint calendar (two-week milestones, Mon..Fri): title, start, due.
S_PREV=("Sprint 41" 2026-10-19 2026-10-30 closed)
S_CUR=("Sprint 42" 2026-11-02 2026-11-13 open)
S_NEXT=("Sprint 43" 2026-11-16 2026-11-27 open)

log() { printf '  %s\n' "$*" >&2; }
enc() { jq -rn --arg v "$1" '$v|@uri'; }

# api METHOD PATH [JSON [gh-args...]]: prints the response body.
# Retries rate limits (429 / secondary 403) and 5xx with backoff; sleeps 1s after mutations to stay
# clear of GitHub's secondary rate limits. ALLOW_404=1: return 1 on 404; ALLOW_ERR=1: return 1 on any error.
api() {
  local method=$1 path=$2 body=${3:-} out err status attempt=0
  shift 3 2>/dev/null || shift $#
  out=$(mktemp)
  err=$(mktemp)
  while :; do
    attempt=$((attempt + 1))
    if [[ -n $body ]]; then
      gh api -X "$method" "$path" "$@" --input - <<<"$body" >"$out" 2>"$err" && status=0 || status=$?
    else
      gh api -X "$method" "$path" "$@" >"$out" 2>"$err" && status=0 || status=$?
    fi
    if ((status == 0)); then
      break
    fi
    if ((attempt < 6)) && grep -qiE 'HTTP 429|rate limit|secondary|abuse|HTTP 50[0-9]' "$err" "$out"; then
      log "rate limited / transient on $method $path; retry $attempt in $((attempt * 20))s"
      sleep $((attempt * 20))
      continue
    fi
    if grep -q 'HTTP 404' "$err" && [[ ${ALLOW_404:-0} == 1 ]]; then
      rm -f "$out" "$err"
      return 1
    fi
    if [[ ${ALLOW_ERR:-0} == 1 ]]; then
      rm -f "$out" "$err"
      return 1
    fi
    printf 'GitHub %s %s failed: %s %s\n' "$method" "$path" "$(cat "$err")" "$(cat "$out")" >&2
    rm -f "$out" "$err"
    exit 1
  done
  cat "$out"
  rm -f "$out" "$err"
  if [[ $method != GET && ${NOSLEEP:-0} != 1 ]]; then
    sleep 1
  fi
}

# list PATH: every item of a paginated array endpoint, as one JSON array.
list() { api GET "$1" "" --paginate | jq -s 'add // []'; }

# gql QUERY [VARS_JSON]: GraphQL call; only mutations pace themselves.
gql() {
  local ns=1 vars=${2:-}
  [[ -z $vars ]] && vars='{}'
  [[ $1 == mutation* ]] && ns=0
  NOSLEEP=$ns api POST graphql "$(jq -n --arg q "$1" --argjson v "$vars" '{query:$q, variables:$v}')" |
    jq 'if .errors then error(.errors|tostring) else .data end'
}

for r in "${REPOS[@]}"; do
  gh api "repos/$r" >/dev/null 2>&1 || {
    echo "repo $r not found (this script does not create repositories)" >&2
    exit 1
  }
done
echo "Seeding github.com/$OWNER/$PREFIX-{tracker,api,web} as $OWNER"

# --- labels -----------------------------------------------------------------
# Three facets: type, app (routes to a repo), workflow (board column); weights ride on labels
# (tracker.weight.source = label). Declared in every repo (labels are per repo on GitHub).
declare -A LABELS
label() { # repo name color description
  local repo=$1 name=$2 color=${3#\#} desc=$4 have
  have=$(jq -c --arg n "$name" 'map(select(.name|ascii_downcase==($n|ascii_downcase)))[0] // empty' <<<"${LABELS[$repo]}")
  if [[ -z $have ]]; then
    api POST "repos/$repo/labels" "$(jq -n --arg n "$name" --arg c "$color" --arg d "$desc" '{name:$n,color:$c,description:$d}')" >/dev/null
    log "label $repo $name"
  elif [[ $(jq -r '.color + "|" + (.description // "")' <<<"$have") != "$color|$desc" ]]; then
    api PATCH "repos/$repo/labels/$(enc "$(jq -r .name <<<"$have")")" "$(jq -n --arg n "$name" --arg c "$color" --arg d "$desc" '{new_name:$n,color:$c,description:$d}')" >/dev/null
    log "label $repo $name updated"
  fi
}
declare_labels() {
  local repo=$1 w
  LABELS[$repo]=$(list "repos/$repo/labels?per_page=100")
  label "$repo" "type:story" "#009966" "User-facing change"
  label "$repo" "type:bug" "#dc143c" "Defect"
  label "$repo" "app:api" "#1f75cb" "Routes work to the api repo"
  label "$repo" "app:web" "#6699cc" "Routes work to the web repo"
  label "$repo" "workflow:in progress" "#428bca" "Being worked on"
  label "$repo" "workflow:in review" "#9400d3" "PR open for review"
  label "$repo" "workflow:blocked" "#36454f" "Waiting on something outside the team"
  label "$repo" "workflow:done" "#2e8b57" "Merged and verified"
  for w in 1 2 3 5 8; do label "$repo" "weight:$w" "#7f8c8d" "Story points ($w)"; done
}
declared_names=$(printf '%s\n' "type:story" "type:bug" "app:api" "app:web" "workflow:in progress" \
  "workflow:in review" "workflow:blocked" "workflow:done" weight:{1,2,3,5,8})
for r in "${REPOS[@]}"; do declare_labels "$r"; done

# Optional cleanup of labels from earlier seed versions (svc:*, priority:*, GitHub's defaults, ...).
# Deletes every label the script does not declare, so it only runs when explicitly asked for.
if [[ ${ADE_SANDBOX_PRUNE:-0} == 1 ]]; then
  for r in "${REPOS[@]}"; do
    while IFS= read -r name; do
      [[ -z $name ]] && continue
      grep -qxF "$name" <<<"$declared_names" && continue
      api DELETE "repos/$r/labels/$(enc "$name")" >/dev/null
      log "pruned label $r $name"
    done < <(list "repos/$r/labels?per_page=100" | jq -r '.[].name')
  done
fi

# --- milestones as sprints --------------------------------------------------
# GitHub milestones have no start date: ADE reads `Start: YYYY-MM-DD` from the first description line.
declare -A M
milestone() { # repo title start due state -> number
  local repo=$1 title=$2 start=$3 due=$4 state=$5 m desc patch
  desc=$(printf 'Start: %s\n\n%s' "$start" "$title")
  m=$(list "repos/$repo/milestones?state=all&per_page=100" | jq -c --arg t "$title" 'map(select(.title==$t))[0] // empty')
  if [[ -z $m ]]; then
    m=$(api POST "repos/$repo/milestones" "$(jq -n --arg t "$title" --arg d "$desc" --arg due "${due}T12:00:00Z" --arg s "$state" \
      '{title:$t, description:$d, due_on:$due, state:$s}')")
    log "milestone $repo $title"
  else
    patch=$(jq -n --argjson m "$m" --arg d "$desc" --arg due "$due" --arg s "$state" '
      {}
      + (if ($m.description // "" | gsub("\r\n"; "\n")) != $d then {description:$d} else {} end)
      + (if ($m.due_on // "")[0:10] != $due then {due_on:($due + "T12:00:00Z")} else {} end)
      + (if $m.state != $s then {state:$s} else {} end)')
    if [[ $patch != '{}' ]]; then
      m=$(api PATCH "repos/$repo/milestones/$(jq -r .number <<<"$m")" "$patch")
      log "milestone $repo $title converged"
    fi
  fi
  if [[ $(jq -r '.due_on[0:10]' <<<"$m") != "$due" ]]; then
    echo "milestone $repo $title: API echoes due_on $(jq -r .due_on <<<"$m"), want $due" >&2
    exit 1
  fi
  jq -r .number <<<"$m"
}
for r in "${REPOS[@]}"; do
  M[$r,prev]=$(milestone "$r" "${S_PREV[@]}")
  M[$r,cur]=$(milestone "$r" "${S_CUR[@]}")
  M[$r,next]=$(milestone "$r" "${S_NEXT[@]}")
done

# --- issues -----------------------------------------------------------------
# issue REPO TITLE BODY LABELS(csv) MILESTONE_NUMBER ASSIGN(yes|no) -> "number id"
# Existing issues (matched by title) are converged: labels, body, milestone, assignee.
issue() {
  local repo=$1 title=$2 body=$3 labels=$4 mid=$5 assign=$6 found want_labels want_assignees patch n
  want_labels=$(jq -nc --arg l "$labels" '$l | split(",") | map(select(. != ""))')
  want_assignees='[]'
  [[ $assign == yes ]] && want_assignees=$(jq -nc --arg o "$OWNER" '[$o]')
  found=$(list "repos/$repo/issues?state=all&per_page=100" | jq -c --arg t "$title" \
    'map(select(.pull_request|not) | select(.title==$t))[0] // empty')
  if [[ -z $found ]]; then
    found=$(api POST "repos/$repo/issues" "$(jq -n --arg t "$title" --arg b "$body" --argjson l "$want_labels" \
      --argjson m "$mid" --argjson a "$want_assignees" '{title:$t, body:$b, labels:$l, milestone:$m, assignees:$a}')")
    log "issue $repo#$(jq -r .number <<<"$found"): $title"
  else
    patch=$(jq -n --argjson f "$found" --arg b "$body" --argjson l "$want_labels" --argjson m "$mid" --argjson a "$want_assignees" '
      {}
      + (if ($f.body // "" | gsub("\r\n"; "\n")) != $b then {body:$b} else {} end)
      + (if ($f.labels | map(.name) | sort) != ($l | sort) then {labels:$l} else {} end)
      + (if $f.milestone.number != $m then {milestone:$m} else {} end)
      + (if ($f.assignees | map(.login) | sort) != $a then {assignees:$a} else {} end)')
    if [[ $patch != '{}' ]]; then
      n=$(jq -r .number <<<"$found")
      found=$(api PATCH "repos/$repo/issues/$n" "$patch")
      log "issue $repo#$n converged ($(jq -r 'keys | join(",")' <<<"$patch")): $title"
    fi
  fi
  jq -r '"\(.number) \(.id)"' <<<"$found"
}
num() { cut -d' ' -f1 <<<"$1"; }
id() { cut -d' ' -f2 <<<"$1"; }
close_issue() { # repo number
  [[ $(api GET "repos/$1/issues/$2" | jq -r .state) == closed ]] && return 0
  api PATCH "repos/$1/issues/$2" '{"state":"closed","state_reason":"completed"}' >/dev/null
  log "closed $1#$2"
}

CUR=${M[$TRACKER,cur]}
NEXT=${M[$TRACKER,next]}
PREV=${M[$TRACKER,prev]}
T_RATE=$(issue "$TRACKER" "Rate-limit the export endpoint" \
  $'Tenants can trigger unbounded CSV exports and starve the worker pool.\n\n## Acceptance\n- [x] Agree limits with support (50 req/min per tenant)\n- [ ] Token-bucket limiter in api\n- [ ] 429 response with Retry-After\n- [ ] Dashboard shows remaining quota' \
  "type:story,app:api,workflow:in review,weight:5" "$CUR" yes)
T_AUDIT=$(issue "$TRACKER" "Paginate the audit log view" \
  $'The audit log page renders every row and freezes for large tenants.\n\n## Acceptance\n- [ ] Cursor pagination in the UI\n- [ ] Keep current filters when paging' \
  "type:bug,app:web,workflow:in review,weight:3" "$CUR" yes) # label ahead of the draft PR: drift
T_WEBHOOK=$(issue "$TRACKER" "Retry failed webhook deliveries" \
  $'Webhook deliveries that fail are dropped.\n\n## Acceptance\n- [ ] Exponential backoff, max 6 attempts\n- [ ] Dead-letter list visible to admins' \
  "type:story,app:api,weight:5" "$CUR" yes)
T_CSV=$(issue "$TRACKER" "Remove the legacy CSV export path" \
  "The v1 CSV path is unused since the new exporter shipped. Remove it and its feature flag." \
  "type:story,app:api,weight:2" "$CUR" yes)
T_TZ=$(issue "$TRACKER" "Export timestamps ignore tenant timezone" \
  "Exports always use UTC. Tenants with a configured timezone expect local time with offset." \
  "type:bug,app:api,workflow:blocked,weight:3" "$CUR" yes)
T_BILL=$(issue "$TRACKER" "Billing export times out for large tenants" \
  "Exports over 200k rows hit the 30s gateway timeout." \
  "type:bug,app:api" "$CUR" no)
T_DARK=$(issue "$TRACKER" "Dark mode for the export dashboard" \
  "Follow the design tokens already used by the settings pages." \
  "type:story,app:web,weight:3" "$NEXT" no)
T_SCHED=$(issue "$TRACKER" "Scheduled exports" \
  "Let tenants schedule a daily or weekly export delivered by email. Needs an api scheduler and a web settings form." \
  "type:story,app:api,app:web,weight:8" "$NEXT" yes)
T_FONT=$(issue "$TRACKER" "Fix truncated column headers in export preview" \
  "Long column names are cut off without a tooltip." \
  "type:bug,app:web,workflow:done,weight:1" "$PREV" yes)
T_DEPS=$(issue "$TRACKER" "Upgrade export worker dependencies" \
  "Routine upgrade of the worker's HTTP and CSV libraries." \
  "type:story,app:api,workflow:done,weight:2" "$CUR" yes)
T_STALE=$(issue "$TRACKER" "Exports page sometimes shows stale totals" \
  "Reported by support: totals lag behind the table after a filter change. No labels yet (needs triage)." \
  "" "$CUR" yes)

# --- child issues + links (tracker-repo pattern) ----------------------------
child() { # repo parent_number title labels -> "number id"
  local body
  body=$(printf 'Parent: %s#%s\n\nImplementation issue for the tracker item above.' "$TRACKER" "$2")
  issue "$1" "$3" "$body" "$4" "${M[$1,cur]}" yes
}
sub_issue() { # tracker_number child_global_id
  local have
  have=$(api GET "repos/$TRACKER/issues/$1/sub_issues" | jq --argjson i "$2" 'map(select(.id==$i))|length')
  [[ $have != 0 ]] && return 0
  api POST "repos/$TRACKER/issues/$1/sub_issues" "$(jq -n --argjson i "$2" '{sub_issue_id:$i}')" >/dev/null
  log "sub-issue tracker#$1 <- $2"
}

C_RATE=$(child "$API_R" "$(num "$T_RATE")" "Rate-limit the export endpoint" "type:story")
sub_issue "$(num "$T_RATE")" "$(id "$C_RATE")"
C_AUDIT=$(child "$WEB_R" "$(num "$T_AUDIT")" "Paginate the audit log view" "type:bug")
sub_issue "$(num "$T_AUDIT")" "$(id "$C_AUDIT")"
C_FONT=$(child "$WEB_R" "$(num "$T_FONT")" "Fix truncated column headers in export preview" "type:bug")
sub_issue "$(num "$T_FONT")" "$(id "$C_FONT")"
C_DEPS=$(child "$API_R" "$(num "$T_DEPS")" "Upgrade export worker dependencies" "type:story")
sub_issue "$(num "$T_DEPS")" "$(id "$C_DEPS")"

# --- pull request template (mirrors gitlab-templates.sh) --------------------
ensure_file() { # repo path content message
  local repo=$1 path=$2 content=$3 msg=$4 cur sha payload
  if cur=$(ALLOW_404=1 api GET "repos/$repo/contents/$path?ref=main"); then
    [[ $(jq -r .content <<<"$cur" | base64 -d) == "${content%$'\n'}" ]] && return 0
    sha=$(jq -r .sha <<<"$cur")
  fi
  payload=$(jq -n --arg m "$msg" --arg c "$(printf '%s' "$content" | base64 -w0)" --arg s "${sha:-}" \
    '{message:$m, content:$c, branch:"main"} + (if $s != "" then {sha:$s} else {} end)')
  api PUT "repos/$repo/contents/$path" "$payload" >/dev/null
  log "file $repo $path"
}
PR_TEMPLATE=$'## Summary\n\nWhat changes, and why.\n\n## Checklist\n\n- [ ] Tests added or updated\n- [ ] Checked locally\n'
for r in "$API_R" "$WEB_R"; do
  ensure_file "$r" ".github/pull_request_template.md" "$PR_TEMPLATE" "Add PR template for ADE start-work sandbox"
done

# --- branches, commits, PRs -------------------------------------------------
slug() { tr '[:upper:]' '[:lower:]' <<<"$1" | sed -E 's/[^a-z0-9]+/-/g; s/^-|-$//g'; }

# pr_json REPO BRANCH: the PR (any state) whose source branch is BRANCH. A merged PR keeps head.ref
# after its branch is deleted, so it is found by listing rather than by `head=owner:branch`.
pr_json() { list "repos/$1/pulls?state=all&per_page=100" | jq -c --arg b "$2" 'map(select(.head.ref==$b))[0] // empty'; }

branch_with_commit() { # repo branch path content message
  # A merged PR deletes its source branch; the PR is the proof the work exists.
  [[ -n $(pr_json "$1" "$2") ]] && return 0
  ALLOW_404=1 api GET "repos/$1/branches/$(enc "$2")" >/dev/null && return 0
  local sha
  sha=$(api GET "repos/$1/git/ref/heads/main" | jq -r .object.sha)
  api POST "repos/$1/git/refs" "$(jq -n --arg r "refs/heads/$2" --arg s "$sha" '{ref:$r, sha:$s}')" >/dev/null
  api PUT "repos/$1/contents/$3" "$(jq -n --arg m "$5" --arg c "$(printf '%s' "$4" | base64 -w0)" --arg b "$2" \
    '{message:$m, content:$c, branch:$b}')" >/dev/null
  log "branch $1 $2"
}

# pr REPO BRANCH TITLE BODY DRAFT(true|false) [BASE=main] -> number
# Existing PRs are converged: title/body/draft state while open, assignee + milestone always.
pr() {
  local repo=$1 branch=$2 title=$3 body=$4 draft=$5 base=${6:-main} found n patch nid
  found=$(pr_json "$repo" "$branch")
  if [[ -z $found ]]; then
    found=$(api POST "repos/$repo/pulls" "$(jq -n --arg h "$branch" --arg b "$base" --arg t "$title" --arg d "$body" --argjson dr "$draft" \
      '{head:$h, base:$b, title:$t, body:$d, draft:$dr}')")
    log "PR $repo#$(jq -r .number <<<"$found"): $title"
  fi
  n=$(jq -r .number <<<"$found")
  if [[ $(jq -r .state <<<"$found") == open ]]; then
    patch=$(jq -n --argjson f "$found" --arg t "$title" --arg b "$body" '
      {}
      + (if $f.title != $t then {title:$t} else {} end)
      + (if ($f.body // "" | gsub("\r\n"; "\n")) != $b then {body:$b} else {} end)')
    if [[ $patch != '{}' ]]; then
      api PATCH "repos/$repo/pulls/$n" "$patch" >/dev/null
      log "PR $repo#$n converged ($(jq -r 'keys | join(",")' <<<"$patch"))"
    fi
    if [[ $(jq -r .draft <<<"$found") != "$draft" ]]; then
      nid=$(jq -r .node_id <<<"$found")
      if [[ $draft == true ]]; then
        gql 'mutation($id:ID!){convertPullRequestToDraft(input:{pullRequestId:$id}){pullRequest{isDraft}}}' "$(jq -nc --arg id "$nid" '{id:$id}')" >/dev/null
      else
        gql 'mutation($id:ID!){markPullRequestReadyForReview(input:{pullRequestId:$id}){pullRequest{isDraft}}}' "$(jq -nc --arg id "$nid" '{id:$id}')" >/dev/null
      fi
      log "PR $repo#$n draft=$draft"
    fi
  fi
  patch=$(jq -n --argjson f "$found" --argjson m "${M[$repo,cur]}" --arg o "$OWNER" '
    {}
    + (if ($f.assignees | map(.login)) != [$o] then {assignees:[$o]} else {} end)
    + (if $f.milestone.number != $m then {milestone:$m} else {} end)')
  if [[ $patch != '{}' ]]; then
    api PATCH "repos/$repo/issues/$n" "$patch" >/dev/null
    log "PR $repo#$n assignee/milestone"
  fi
  echo "$n"
}

B_RATE="$(num "$C_RATE")-$(slug "Rate-limit the export endpoint")"
branch_with_commit "$API_R" "$B_RATE" "limit/bucket.go" \
  $'package limit\n\n// Bucket is a per-tenant token bucket.\ntype Bucket struct {\n\tRate  int // tokens per minute\n\tBurst int\n}\n' \
  "Add per-tenant token bucket"
MR_RATE=$(pr "$API_R" "$B_RATE" "Rate-limit the export endpoint" \
  "$(printf 'Closes #%s\n\nPart of %s#%s.\n\nAdds a per-tenant token bucket in front of the export handler.' "$(num "$C_RATE")" "$TRACKER" "$(num "$T_RATE")")" false)

B_AUDIT="$(num "$C_AUDIT")-$(slug "Paginate the audit log view")"
branch_with_commit "$WEB_R" "$B_AUDIT" "src/audit/pager.ts" \
  $'export interface Page<T> {\n  items: T[];\n  nextCursor?: string;\n}\n' \
  "Add cursor page type"
MR_AUDIT=$(pr "$WEB_R" "$B_AUDIT" "Paginate the audit log view" \
  "$(printf 'Closes #%s\n\nPart of %s#%s.' "$(num "$C_AUDIT")" "$TRACKER" "$(num "$T_AUDIT")")" true)

# A stacked PR whose diff holds a RENAMED file: it targets the rate-limit branch and moves
# limit/bucket.go to limit/token_bucket.go with edits (git data API: old path sha:null + new path).
# Built from the branch's current file so git's rename detection (>= 50% similar) always sees a rename.
# Never merged.
B_TOKEN="rename-bucket-to-token-bucket"
if [[ -z $(pr_json "$API_R" "$B_TOKEN") ]] && ! ALLOW_404=1 api GET "repos/$API_R/branches/$B_TOKEN" >/dev/null; then
  base_sha=$(api GET "repos/$API_R/git/ref/heads/$B_RATE" | jq -r .object.sha)
  base_tree=$(api GET "repos/$API_R/git/commits/$base_sha" | jq -r .tree.sha)
  old=$(api GET "repos/$API_R/contents/limit/bucket.go?ref=$B_RATE" | jq -r .content | base64 -d)
  new=${old/$'\tBurst int\n'/$'\tBurst int\n\n\tTokens float64 // currently available\n'}$'\n' # $(...) dropped the final newline
  tree=$(api POST "repos/$API_R/git/trees" "$(jq -n --arg b "$base_tree" --arg c "$new" \
    '{base_tree:$b, tree:[{path:"limit/bucket.go", mode:"100644", type:"blob", sha:null}, {path:"limit/token_bucket.go", mode:"100644", type:"blob", content:$c}]}')" | jq -r .sha)
  commit=$(api POST "repos/$API_R/git/commits" "$(jq -n --arg t "$tree" --arg p "$base_sha" \
    '{message:"Rename Bucket to its own file and track tokens", tree:$t, parents:[$p]}')" | jq -r .sha)
  api POST "repos/$API_R/git/refs" "$(jq -n --arg r "refs/heads/$B_TOKEN" --arg s "$commit" '{ref:$r, sha:$s}')" >/dev/null
  log "branch $API_R $B_TOKEN"
fi
MR_TOKEN=$(pr "$API_R" "$B_TOKEN" "Rename bucket to token bucket" \
  "Stacked on #$MR_RATE: renames Bucket's file and tracks tokens explicitly." false "$B_RATE")

# Review threads on the ready PR: one resolved, two open (one with a reply).
# Threads from the old seed are reused: their first comment is edited to the current wording.
TH_A='Is a burst of 50 too high for small tenants? Support suggested 20.'
TH_B='Please add a test for the 429 path, including the Retry-After header.'
TH_C='Nit: rename Bucket.Rate to PerMinute for clarity.'
TH_A_REPLY='Keeping 50 for now; it matches the support agreement. Open to revisit.'
# threads REPO PR: [{id, resolved, first:{id,body}, count}] of the PR's review threads.
threads() {
  local o r
  o=${1%%/*}
  r=${1#*/}
  gql 'query($o:String!,$r:String!,$n:Int!){repository(owner:$o,name:$r){pullRequest(number:$n){reviewThreads(first:100){nodes{id isResolved comments(first:50){totalCount nodes{databaseId body}}}}}}}' \
    "$(jq -nc --arg o "$o" --arg r "$r" --argjson n "$2" '{o:$o,r:$r,n:$n}')" |
    jq -c '[.repository.pullRequest.reviewThreads.nodes[] | {id, resolved:.isResolved, count:.comments.totalCount, first:.comments.nodes[0]}]'
}
find_thread() { jq -c --arg b "$2" 'map(select(.first.body==$b))[0] // empty' <<<"$1"; }
# Old-seed wording -> current wording (edited in place, so the thread is reused).
revise() { # threads_json old new
  local t
  t=$(find_thread "$1" "$2")
  [[ -z $t || -n $(find_thread "$1" "$3") ]] && return 0
  api PATCH "repos/$API_R/pulls/comments/$(jq -r .first.databaseId <<<"$t")" "$(jq -n --arg b "$3" '{body:$b}')" >/dev/null
  log "PR thread reworded: $3"
}
T=$(threads "$API_R" "$MR_RATE")
revise "$T" 'Is a burst of 50 too high for small tenants?' "$TH_A"
revise "$T" 'Nit: rename Rate to PerMinute.' "$TH_C"
T=$(threads "$API_R" "$MR_RATE")
missing=$(jq -nc --argjson t "$T" --arg a "$TH_A" --arg b "$TH_B" --arg c "$TH_C" '
  [{line:6, body:$a}, {line:4, body:$b}, {line:5, body:$c}]
  | map(. as $w | select($t | map(.first.body == $w.body) | any | not))
  | map(. + {path:"limit/bucket.go", side:"RIGHT"})')
if [[ $missing != '[]' ]]; then
  api POST "repos/$API_R/pulls/$MR_RATE/reviews" "$(jq -nc --argjson c "$missing" '{event:"COMMENT", body:"Review pass.", comments:$c}')" >/dev/null
  log "PR review threads: $(jq -r 'length' <<<"$missing") created"
  T=$(threads "$API_R" "$MR_RATE")
fi
ta=$(find_thread "$T" "$TH_A")
if [[ $(jq -r .count <<<"$ta") == 1 ]]; then
  api POST "repos/$API_R/pulls/$MR_RATE/comments/$(jq -r .first.databaseId <<<"$ta")/replies" "$(jq -n --arg b "$TH_A_REPLY" '{body:$b}')" >/dev/null
  log "PR thread reply"
fi
tc=$(find_thread "$T" "$TH_C")
if [[ $(jq -r .resolved <<<"$tc") != true ]]; then
  gql 'mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{isResolved}}}' "$(jq -nc --arg id "$(jq -r .id <<<"$tc")" '{id:$id}')" >/dev/null
  log "PR thread resolved: $TH_C"
fi

# --- finished work (sprint review data) -------------------------------------
B_FONT="$(num "$C_FONT")-$(slug "Fix truncated column headers in export preview")"
branch_with_commit "$WEB_R" "$B_FONT" "src/preview/header.css" \
  $'.preview th {\n  overflow: hidden;\n  text-overflow: ellipsis;\n}\n' "Ellipsize long headers"
MR_FONT=$(pr "$WEB_R" "$B_FONT" "Fix truncated column headers in export preview" \
  "$(printf 'Closes #%s\n\nPart of %s#%s.' "$(num "$C_FONT")" "$TRACKER" "$(num "$T_FONT")")" false)
if [[ $(api GET "repos/$WEB_R/pulls/$MR_FONT" | jq -r .state) == open ]]; then
  for _ in 1 2 3 4 5 6 7 8 9 10; do # mergeability is computed asynchronously
    sleep 3
    if ALLOW_ERR=1 api PUT "repos/$WEB_R/pulls/$MR_FONT/merge" '{"merge_method":"merge"}' >/dev/null 2>&1; then
      log "merged web#$MR_FONT"
      break
    fi
  done
fi
if [[ $(api GET "repos/$WEB_R/pulls/$MR_FONT" | jq -r .merged) == true ]] &&
  ALLOW_404=1 api GET "repos/$WEB_R/branches/$B_FONT" >/dev/null; then
  api DELETE "repos/$WEB_R/git/refs/heads/$B_FONT" >/dev/null
  log "deleted merged branch $B_FONT"
fi
close_issue "$TRACKER" "$(num "$T_FONT")"
close_issue "$WEB_R" "$(num "$C_FONT")"
close_issue "$TRACKER" "$(num "$T_DEPS")"
close_issue "$API_R" "$(num "$C_DEPS")"

# --- summary ----------------------------------------------------------------
cat <<EOF
Done.
  repos     github.com/$TRACKER  $API_R  $WEB_R
  tracker   #$(num "$T_RATE") #$(num "$T_AUDIT") #$(num "$T_WEBHOOK") #$(num "$T_CSV") #$(num "$T_TZ") #$(num "$T_BILL") #$(num "$T_DARK") #$(num "$T_SCHED") #$(num "$T_FONT") #$(num "$T_DEPS")
            needs triage #$(num "$T_STALE")   multi-app #$(num "$T_SCHED")
  api       child #$(num "$C_RATE")  PR #$MR_RATE (ready, 2 open + 1 resolved threads)   child #$(num "$C_DEPS") (closed)
  web       child #$(num "$C_AUDIT") PR #$MR_AUDIT (draft)                               child #$(num "$C_FONT")  PR #$MR_FONT (merged)
  api       PR #$MR_TOKEN (stacked on #$MR_RATE; renames limit/bucket.go -> limit/token_bucket.go)
  title -> number
    tracker #$(num "$T_RATE")  Rate-limit the export endpoint
    tracker #$(num "$T_AUDIT")  Paginate the audit log view
    tracker #$(num "$T_WEBHOOK")  Retry failed webhook deliveries
    tracker #$(num "$T_CSV")  Remove the legacy CSV export path
    tracker #$(num "$T_TZ")  Export timestamps ignore tenant timezone
    tracker #$(num "$T_BILL")  Billing export times out for large tenants
    tracker #$(num "$T_DARK")  Dark mode for the export dashboard
    tracker #$(num "$T_SCHED")  Scheduled exports
    tracker #$(num "$T_FONT")  Fix truncated column headers in export preview
    tracker #$(num "$T_DEPS")  Upgrade export worker dependencies
    tracker #$(num "$T_STALE")  Exports page sometimes shows stale totals
ADE_SEED_T_RATE=$(num "$T_RATE")
ADE_SEED_T_AUDIT=$(num "$T_AUDIT")
ADE_SEED_T_WEBHOOK=$(num "$T_WEBHOOK")
ADE_SEED_T_CSV=$(num "$T_CSV")
ADE_SEED_T_TZ=$(num "$T_TZ")
ADE_SEED_T_BILL=$(num "$T_BILL")
ADE_SEED_T_DARK=$(num "$T_DARK")
ADE_SEED_T_SCHED=$(num "$T_SCHED")
ADE_SEED_T_FONT=$(num "$T_FONT")
ADE_SEED_T_DEPS=$(num "$T_DEPS")
ADE_SEED_T_STALE=$(num "$T_STALE")
ADE_SEED_C_RATE=$(num "$C_RATE")
ADE_SEED_C_AUDIT=$(num "$C_AUDIT")
ADE_SEED_C_FONT=$(num "$C_FONT")
ADE_SEED_C_DEPS=$(num "$C_DEPS")
ADE_SEED_MR_RATE=$MR_RATE
ADE_SEED_MR_AUDIT=$MR_AUDIT
ADE_SEED_MR_TOKEN=$MR_TOKEN
ADE_SEED_MR_FONT=$MR_FONT
EOF
