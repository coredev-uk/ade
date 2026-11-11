#!/usr/bin/env bash
# Seed GitHub sandbox repos with synthetic data for ADE development.
#
# Idempotent: re-running finds existing objects instead of duplicating them.
# All content is fictional. Needs: gh (authenticated, `repo` scope), jq.
# Projects v2 (iteration field) additionally needs: gh auth refresh -s project
#
# Env:
#   ADE_SANDBOX_PREFIX   repo name prefix, default ade-sandbox
set -euo pipefail

PREFIX="${ADE_SANDBOX_PREFIX:-ade-sandbox}"
OWNER=$(gh api user --jq .login)
log() { printf '  %s\n' "$*" >&2; }
echo "Seeding github.com/$OWNER/$PREFIX-{tracker,api,web}"

repo() { # name description
  local full="$OWNER/$PREFIX-$1"
  if ! gh repo view "$full" >/dev/null 2>&1; then
    gh api -X POST user/repos -f name="$PREFIX-$1" -f description="$2" -F private=true -F auto_init=true >/dev/null
    log "repo $full"
    sleep 2
  fi
  echo "$full"
}
TRACKER=$(repo tracker "Planning tracker for the Acme export service (fictional sandbox for ADE).")
API_R=$(repo api "Acme export service backend (fictional sandbox for ADE).")
WEB_R=$(repo web "Acme export service frontend (fictional sandbox for ADE).")

label() { # repo name color description
  gh api "repos/$1/labels/$(jq -rn --arg v "$2" '$v|@uri')" >/dev/null 2>&1 && return 0
  gh api -X POST "repos/$1/labels" -f name="$2" -f color="$3" -f description="$4" >/dev/null
}
for r in "$TRACKER" "$API_R" "$WEB_R"; do
  label "$r" "svc:api" "1f75cb" "Work lands in the api repo"
  label "$r" "svc:web" "6699cc" "Work lands in the web repo"
  label "$r" "type:feature" "009966" "New behaviour"
  label "$r" "type:bug" "dc143c" "Defect"
  label "$r" "priority:1" "ed9121" "Highest"
  label "$r" "priority:2" "c39953" "Normal"
  label "$r" "priority:3" "a8d695" "Low"
  label "$r" "blocked" "36454f" "Waiting on something outside the team"
done
log "labels"

milestone() { # repo title due state -> number
  local n
  n=$(gh api "repos/$1/milestones?state=all&per_page=100" --jq "map(select(.title==\"$2\"))[0].number // empty")
  if [[ -z $n ]]; then
    n=$(gh api -X POST "repos/$1/milestones" -f title="$2" -f due_on="${3}T17:00:00Z" -f state="$4" --jq .number)
    log "milestone $1 $2"
  fi
  echo "$n"
}
declare -A M
for r in "$TRACKER" "$API_R" "$WEB_R"; do
  M[$r,prev]=$(milestone "$r" "Sprint 41" 2026-10-30 closed)
  M[$r,cur]=$(milestone "$r" "Sprint 42" 2026-11-13 open)
  M[$r,next]=$(milestone "$r" "Sprint 43" 2026-11-27 open)
done

# issue REPO TITLE BODY LABELS(csv) MILESTONE_NUMBER ASSIGN(yes|no) -> "number id"
issue() {
  local found
  found=$(gh api "repos/$1/issues?state=all&per_page=100" --jq "map(select(.pull_request|not) | select(.title==\"$2\"))[0] | select(.) | \"\(.number) \(.id)\"")
  if [[ -n $found ]]; then
    echo "$found"
    return
  fi
  local args=(-X POST "repos/$1/issues" -f title="$2" -f body="$3" -F milestone="$5")
  local l
  IFS=, read -ra ls <<<"$4"
  for l in "${ls[@]}"; do args+=(-f "labels[]=$l"); done
  [[ $6 == yes ]] && args+=(-f "assignees[]=$OWNER")
  gh api "${args[@]}" --jq '"\(.number) \(.id)"'
  log "issue $1: $2"
}
num() { cut -d' ' -f1 <<<"$1"; }
id() { cut -d' ' -f2 <<<"$1"; }

T_RATE=$(issue "$TRACKER" "Rate-limit the export endpoint" \
  $'Tenants can trigger unbounded CSV exports and starve the worker pool.\n\n## Acceptance\n- [x] Agree limits with support (50 req/min per tenant)\n- [ ] Token-bucket limiter in api\n- [ ] 429 response with Retry-After' \
  "svc:api,type:feature,priority:1" "${M[$TRACKER,cur]}" yes)
T_AUDIT=$(issue "$TRACKER" "Paginate the audit log view" \
  $'The audit log page renders every row and freezes for large tenants.\n\n## Acceptance\n- [ ] Cursor pagination in the UI' \
  "svc:web,type:bug,priority:2" "${M[$TRACKER,cur]}" yes)
T_WEBHOOK=$(issue "$TRACKER" "Retry failed webhook deliveries" \
  "Webhook deliveries that fail are dropped." "svc:api,type:feature,priority:2" "${M[$TRACKER,cur]}" yes)
T_TZ=$(issue "$TRACKER" "Export timestamps ignore tenant timezone" \
  "Exports always use UTC." "svc:api,type:bug,priority:2,blocked" "${M[$TRACKER,cur]}" yes)
T_BILL=$(issue "$TRACKER" "Billing export times out for large tenants" \
  "Exports over 200k rows hit the 30s gateway timeout." "svc:api,type:bug,priority:1" "${M[$TRACKER,cur]}" no)
T_DARK=$(issue "$TRACKER" "Dark mode for the export dashboard" \
  "Follow the existing design tokens." "svc:web,type:feature,priority:3" "${M[$TRACKER,next]}" no)

child() { # repo parent_number title labels -> "number id"
  issue "$1" "$3" "$(printf 'Parent: %s#%s' "$TRACKER" "$2")" "$4" "${M[$1,cur]}" yes
}
sub_issue() { # parent_number child_id
  local have
  have=$(gh api "repos/$TRACKER/issues/$1/sub_issues" --jq "map(select(.id==$2))|length")
  [[ $have != 0 ]] && return 0
  gh api -X POST "repos/$TRACKER/issues/$1/sub_issues" -F sub_issue_id="$2" >/dev/null
  log "sub-issue tracker#$1 <- $2"
}
C_RATE=$(child "$API_R" "$(num "$T_RATE")" "Rate-limit the export endpoint" "type:feature")
sub_issue "$(num "$T_RATE")" "$(id "$C_RATE")"
C_AUDIT=$(child "$WEB_R" "$(num "$T_AUDIT")" "Paginate the audit log view" "type:bug")
sub_issue "$(num "$T_AUDIT")" "$(id "$C_AUDIT")"

branch_with_commit() { # repo branch path content message
  gh api "repos/$1/branches/$2" >/dev/null 2>&1 && return 0
  local sha
  sha=$(gh api "repos/$1/git/ref/heads/main" --jq .object.sha)
  gh api -X POST "repos/$1/git/refs" -f ref="refs/heads/$2" -f sha="$sha" >/dev/null
  gh api -X PUT "repos/$1/contents/$3" -f message="$5" -f content="$(printf '%s' "$4" | base64 -w0)" -f branch="$2" >/dev/null
  log "branch $1 $2"
}
pr() { # repo branch title body draft(true|false) -> number
  local n
  n=$(gh api "repos/$1/pulls?state=all&head=$OWNER:$2" --jq '.[0].number // empty')
  if [[ -z $n ]]; then
    n=$(gh api -X POST "repos/$1/pulls" -f head="$2" -f base=main -f title="$3" -f body="$4" -F draft="$5" --jq .number)
    log "PR $1 #$n"
  fi
  echo "$n"
}

B_RATE="$(num "$C_RATE")-rate-limit-the-export-endpoint"
branch_with_commit "$API_R" "$B_RATE" "limit/bucket.go" \
  $'package limit\n\n// Bucket is a per-tenant token bucket.\ntype Bucket struct {\n\tRate  int\n\tBurst int\n}\n' "Add per-tenant token bucket"
PR_RATE=$(pr "$API_R" "$B_RATE" "Rate-limit the export endpoint" \
  "$(printf 'Closes #%s\n\nPart of %s#%s.' "$(num "$C_RATE")" "$TRACKER" "$(num "$T_RATE")")" false)

B_AUDIT="$(num "$C_AUDIT")-paginate-the-audit-log-view"
branch_with_commit "$WEB_R" "$B_AUDIT" "src/audit/pager.ts" \
  $'export interface Page<T> {\n  items: T[];\n  nextCursor?: string;\n}\n' "Add cursor page type"
PR_AUDIT=$(pr "$WEB_R" "$B_AUDIT" "Paginate the audit log view" \
  "$(printf 'Closes #%s\n\nPart of %s#%s.' "$(num "$C_AUDIT")" "$TRACKER" "$(num "$T_AUDIT")")" true)

# Review threads on the ready PR (line comments via a COMMENT review).
if [[ $(gh api "repos/$API_R/pulls/$PR_RATE/comments" --jq length) == 0 ]]; then
  gh api -X POST "repos/$API_R/pulls/$PR_RATE/reviews" --input - >/dev/null <<EOF
{"event":"COMMENT","body":"First pass.","comments":[
  {"path":"limit/bucket.go","line":6,"body":"Is a burst of 50 too high for small tenants?"},
  {"path":"limit/bucket.go","line":5,"body":"Nit: rename Rate to PerMinute."}]}
EOF
  log "PR review threads"
fi

cat <<EOF
Done.
  tracker  github.com/$TRACKER  #$(num "$T_RATE") #$(num "$T_AUDIT") #$(num "$T_WEBHOOK") #$(num "$T_TZ") #$(num "$T_BILL") #$(num "$T_DARK")
  api      child #$(num "$C_RATE")  PR #$PR_RATE (ready, 2 threads)
  web      child #$(num "$C_AUDIT") PR #$PR_AUDIT (draft)
EOF
