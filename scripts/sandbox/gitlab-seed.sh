#!/usr/bin/env bash
# Seed a GitLab sandbox with synthetic data for ADE development.
#
# Idempotent: re-running finds existing objects instead of duplicating them.
# All content is fictional. Needs: curl, jq.
#
# Env:
#   GITLAB_PAT          personal access token with `api` scope (required)
#   GITLAB_URL          default https://gitlab.com
#   ADE_SANDBOX_GROUP   group path, default ade-sandbox-<username>
set -euo pipefail

: "${GITLAB_PAT:?set GITLAB_PAT (e.g. via .env)}"
GITLAB_URL="${GITLAB_URL:-https://gitlab.com}"
API="$GITLAB_URL/api/v4"

# Sprint calendar (two-week milestones, Mon..Fri).
S_PREV=("Sprint 41" 2026-10-19 2026-10-30)
S_CUR=("Sprint 42" 2026-11-02 2026-11-13)
S_NEXT=("Sprint 43" 2026-11-16 2026-11-27)

log() { printf '  %s\n' "$*" >&2; }

# gl METHOD PATH [JSON]: prints body; fails on HTTP >= 400 unless ALLOW_404=1 and status is 404.
gl() {
  local method=$1 path=$2 body=${3:-} out status
  out=$(mktemp)
  local args=(-sS -o "$out" -w '%{http_code}' -X "$method" -H "PRIVATE-TOKEN: $GITLAB_PAT")
  [[ -n $body ]] && args+=(-H 'Content-Type: application/json' --data "$body")
  status=$(curl "${args[@]}" "$API$path")
  if [[ $status == 404 && ${ALLOW_404:-0} == 1 ]]; then
    rm -f "$out"
    return 1
  fi
  if ((status >= 400)); then
    printf 'GitLab %s %s -> %s: %s\n' "$method" "$path" "$status" "$(cat "$out")" >&2
    rm -f "$out"
    exit 1
  fi
  cat "$out"
  rm -f "$out"
}

enc() { jq -rn --arg v "$1" '$v|@uri'; }

ME=$(gl GET /user)
ME_ID=$(jq -r .id <<<"$ME")
ME_NAME=$(jq -r .username <<<"$ME")
GROUP="${ADE_SANDBOX_GROUP:-ade-sandbox-$ME_NAME}"
echo "Seeding $GITLAB_URL/$GROUP as $ME_NAME"

# --- group + projects -------------------------------------------------------
if G=$(ALLOW_404=1 gl GET "/groups/$(enc "$GROUP")"); then
  log "group exists"
else
  if ! G=$(gl POST /groups "$(jq -n --arg p "$GROUP" '{name:$p, path:$p, visibility:"private",
    description:"Synthetic sandbox for ADE development. All content is fictional."}')" 2>/dev/null); then
    echo "Cannot create group '$GROUP' via the API (GitLab.com blocks top-level group creation for API tokens)." >&2
    echo "Create it once in the UI ($GITLAB_URL/groups/new, private), then re-run." >&2
    exit 1
  fi
  log "group created"
fi
GID=$(jq -r .id <<<"$G")

project() { # path description -> id
  local p
  if p=$(ALLOW_404=1 gl GET "/projects/$(enc "$GROUP/$1")"); then :; else
    p=$(gl POST /projects "$(jq -n --arg n "$1" --argjson ns "$GID" --arg d "$2" \
      '{name:$n, path:$n, namespace_id:$ns, description:$d, visibility:"private", initialize_with_readme:true}')")
    log "project $1 created"
  fi
  jq -r .id <<<"$p"
}
TRACKER=$(project tracker "Planning tracker: issues and boards for the Acme export service.")
API_P=$(project api "Acme export service backend (fictional).")
WEB_P=$(project web "Acme export service frontend (fictional).")

# --- labels -----------------------------------------------------------------
EXISTING_LABELS=$(gl GET "/groups/$GID/labels?per_page=100" | jq -r '.[].name')
label() { # name color description
  grep -qxF "$1" <<<"$EXISTING_LABELS" && return 0
  gl POST "/groups/$GID/labels" "$(jq -n --arg n "$1" --arg c "$2" --arg d "$3" '{name:$n,color:$c,description:$d}')" >/dev/null
  log "label $1"
}
label "svc::api" "#1f75cb" "Work lands in the api project"
label "svc::web" "#6699cc" "Work lands in the web project"
label "type::feature" "#009966" "New behaviour"
label "type::bug" "#dc143c" "Defect"
label "priority::1" "#ed9121" "Highest"
label "priority::2" "#c39953" "Normal"
label "priority::3" "#a8d695" "Low"
label "workflow::ready" "#8fbc8f" "Ready to pick up"
label "workflow::in progress" "#428bca" "Being worked on"
label "workflow::review" "#9400d3" "In review"
label "blocked" "#36454f" "Waiting on something outside the team"
label "Sprint::42" "#5843ad" "Label-based sprint marker (tests the labels iteration source)"
label "Sprint::43" "#5843ad" "Label-based sprint marker (tests the labels iteration source)"

# --- milestones as sprints --------------------------------------------------
milestone() { # title start due -> id
  local m
  m=$(gl GET "/groups/$GID/milestones?title=$(enc "$1")&include_ancestors=false" | jq '.[0] // empty')
  if [[ -z $m ]]; then
    m=$(gl POST "/groups/$GID/milestones" "$(jq -n --arg t "$1" --arg s "$2" --arg d "$3" '{title:$t,start_date:$s,due_date:$d}')")
    log "milestone $1"
  fi
  jq -r .id <<<"$m"
}
M_PREV=$(milestone "${S_PREV[@]}")
M_CUR=$(milestone "${S_CUR[@]}")
M_NEXT=$(milestone "${S_NEXT[@]}")

# --- issues -----------------------------------------------------------------
# issue PROJECT_ID TITLE BODY LABELS MILESTONE_ID ASSIGN(yes|no) -> iid
issue() {
  local pid=$1 title=$2 body=$3 labels=$4 mid=$5 assign=$6 found
  found=$(gl GET "/projects/$pid/issues?state=all&in=title&search=$(enc "$title")&per_page=100" |
    jq -r --arg t "$title" 'map(select(.title==$t))[0].iid // empty')
  if [[ -n $found ]]; then
    echo "$found"
    return
  fi
  local assignees='[]'
  [[ $assign == yes ]] && assignees="[$ME_ID]"
  gl POST "/projects/$pid/issues" "$(jq -n --arg t "$title" --arg d "$body" --arg l "$labels" \
    --argjson m "$mid" --argjson a "$assignees" '{title:$t, description:$d, labels:$l, milestone_id:$m, assignee_ids:$a}')" |
    jq -r .iid
  log "issue: $title"
}
close_issue() { gl PUT "/projects/$1/issues/$2" '{"state_event":"close"}' >/dev/null; }

T_RATE=$(issue "$TRACKER" "Rate-limit the export endpoint" \
  $'Tenants can trigger unbounded CSV exports and starve the worker pool.\n\n## Acceptance\n- [x] Agree limits with support (50 req/min per tenant)\n- [ ] Token-bucket limiter in api\n- [ ] 429 response with Retry-After\n- [ ] Dashboard shows remaining quota' \
  "svc::api,type::feature,priority::1,workflow::review" "$M_CUR" yes)
T_AUDIT=$(issue "$TRACKER" "Paginate the audit log view" \
  $'The audit log page renders every row and freezes for large tenants.\n\n## Acceptance\n- [ ] Cursor pagination in the UI\n- [ ] Keep current filters when paging' \
  "svc::web,type::bug,priority::2,workflow::in progress" "$M_CUR" yes)
T_WEBHOOK=$(issue "$TRACKER" "Retry failed webhook deliveries" \
  $'Webhook deliveries that fail are dropped.\n\n## Acceptance\n- [ ] Exponential backoff, max 6 attempts\n- [ ] Dead-letter list visible to admins' \
  "svc::api,type::feature,priority::2,workflow::ready,Sprint::42" "$M_CUR" yes)
T_CSV=$(issue "$TRACKER" "Remove the legacy CSV export path" \
  "The v1 CSV path is unused since the new exporter shipped. Remove it and its feature flag." \
  "svc::api,type::feature,priority::3,workflow::ready" "$M_CUR" yes)
T_TZ=$(issue "$TRACKER" "Export timestamps ignore tenant timezone" \
  "Exports always use UTC. Tenants with a configured timezone expect local time with offset." \
  "svc::api,type::bug,priority::2,blocked" "$M_CUR" yes)
T_BILL=$(issue "$TRACKER" "Billing export times out for large tenants" \
  "Exports over 200k rows hit the 30s gateway timeout." \
  "svc::api,type::bug,priority::1,workflow::ready" "$M_CUR" no)
T_DARK=$(issue "$TRACKER" "Dark mode for the export dashboard" \
  "Follow the design tokens already used by the settings pages." \
  "svc::web,type::feature,priority::3,Sprint::43" "$M_NEXT" no)
T_SCHED=$(issue "$TRACKER" "Scheduled exports" \
  "Let tenants schedule a daily or weekly export delivered by email." \
  "svc::api,svc::web,type::feature,priority::2" "$M_NEXT" yes)
T_FONT=$(issue "$TRACKER" "Fix truncated column headers in export preview" \
  "Long column names are cut off without a tooltip." \
  "svc::web,type::bug,priority::3" "$M_PREV" yes)
T_DEPS=$(issue "$TRACKER" "Upgrade export worker dependencies" \
  "Routine upgrade of the worker's HTTP and CSV libraries." \
  "svc::api,type::feature,priority::3" "$M_CUR" yes)

# --- child issues + links (tracker-project pattern) -------------------------
child() { # code_pid parent_iid title labels -> child iid
  local body
  body=$(printf 'Parent: %s/tracker#%s\n\nImplementation issue for the tracker item above.' "$GROUP" "$2")
  issue "$1" "$3" "$body" "$4" "$M_CUR" yes
}
link() { # tracker_iid code_pid code_iid
  local have
  have=$(gl GET "/projects/$TRACKER/issues/$1/links" | jq --argjson p "$2" --argjson i "$3" 'map(select(.project_id==$p and .iid==$i))|length')
  [[ $have != 0 ]] && return 0
  gl POST "/projects/$TRACKER/issues/$1/links" "$(jq -n --argjson p "$2" --argjson i "$3" \
    '{target_project_id:($p|tostring), target_issue_iid:($i|tostring), link_type:"relates_to"}')" >/dev/null
  log "link tracker#$1 -> $2#$3"
}

C_RATE=$(child "$API_P" "$T_RATE" "Rate-limit the export endpoint" "type::feature")
link "$T_RATE" "$API_P" "$C_RATE"
C_AUDIT=$(child "$WEB_P" "$T_AUDIT" "Paginate the audit log view" "type::bug")
link "$T_AUDIT" "$WEB_P" "$C_AUDIT"
C_FONT=$(child "$WEB_P" "$T_FONT" "Fix truncated column headers in export preview" "type::bug")
link "$T_FONT" "$WEB_P" "$C_FONT"
C_DEPS=$(child "$API_P" "$T_DEPS" "Upgrade export worker dependencies" "type::feature")
link "$T_DEPS" "$API_P" "$C_DEPS"

# --- branches, commits, MRs -------------------------------------------------
slug() { tr '[:upper:]' '[:lower:]' <<<"$1" | sed -E 's/[^a-z0-9]+/-/g; s/^-|-$//g'; }

branch_with_commit() { # pid branch file content message
  # A merged MR deletes its source branch; its MR is the proof the work exists.
  [[ -n $(gl GET "/projects/$1/merge_requests?state=all&source_branch=$(enc "$2")" | jq -r '.[0].iid // empty') ]] && return 0
  if ! ALLOW_404=1 gl GET "/projects/$1/repository/branches/$(enc "$2")" >/dev/null; then
    gl POST "/projects/$1/repository/branches" "$(jq -n --arg b "$2" '{branch:$b, ref:"main"}')" >/dev/null
    gl POST "/projects/$1/repository/commits" "$(jq -n --arg b "$2" --arg f "$3" --arg c "$4" --arg m "$5" \
      '{branch:$b, commit_message:$m, actions:[{action:"create", file_path:$f, content:$c}]}')" >/dev/null
    log "branch $2"
  fi
}

mr() { # pid branch title body draft(yes|no) -> mr iid
  local found t=$3
  found=$(gl GET "/projects/$1/merge_requests?state=all&source_branch=$(enc "$2")" | jq -r '.[0].iid // empty')
  if [[ -n $found ]]; then
    echo "$found"
    return
  fi
  [[ $5 == yes ]] && t="Draft: $t"
  gl POST "/projects/$1/merge_requests" "$(jq -n --arg b "$2" --arg t "$t" --arg d "$4" --argjson a "$ME_ID" --argjson m "$M_CUR" \
    '{source_branch:$b, target_branch:"main", title:$t, description:$d, assignee_id:$a, milestone_id:$m, remove_source_branch:true}')" |
    jq -r .iid
  log "MR $t"
}

B_RATE="$C_RATE-$(slug "Rate-limit the export endpoint")"
branch_with_commit "$API_P" "$B_RATE" "limit/bucket.go" \
  $'package limit\n\n// Bucket is a per-tenant token bucket.\ntype Bucket struct {\n\tRate  int // tokens per minute\n\tBurst int\n}\n' \
  "Add per-tenant token bucket"
MR_RATE=$(mr "$API_P" "$B_RATE" "Rate-limit the export endpoint" \
  "$(printf 'Closes #%s\n\nPart of %s/tracker#%s.\n\nAdds a per-tenant token bucket in front of the export handler.' "$C_RATE" "$GROUP" "$T_RATE")" no)

B_AUDIT="$C_AUDIT-$(slug "Paginate the audit log view")"
branch_with_commit "$WEB_P" "$B_AUDIT" "src/audit/pager.ts" \
  $'export interface Page<T> {\n  items: T[];\n  nextCursor?: string;\n}\n' \
  "Add cursor page type"
MR_AUDIT=$(mr "$WEB_P" "$B_AUDIT" "Paginate the audit log view" \
  "$(printf 'Closes #%s\n\nPart of %s/tracker#%s.' "$C_AUDIT" "$GROUP" "$T_AUDIT")" yes)

# Review threads on the ready MR: one resolved, two open.
if [[ $(gl GET "/projects/$API_P/merge_requests/$MR_RATE/discussions" | jq 'map(select(.individual_note|not))|length') == 0 ]]; then
  d1=$(gl POST "/projects/$API_P/merge_requests/$MR_RATE/discussions" '{"body":"Is a burst of 50 too high for small tenants? Support suggested 20."}' | jq -r .id)
  gl POST "/projects/$API_P/merge_requests/$MR_RATE/discussions" '{"body":"Please add a test for the 429 path, including the Retry-After header."}' >/dev/null
  d3=$(gl POST "/projects/$API_P/merge_requests/$MR_RATE/discussions" '{"body":"Nit: rename Bucket.Rate to PerMinute for clarity."}' | jq -r .id)
  gl POST "/projects/$API_P/merge_requests/$MR_RATE/discussions/$d1/notes" '{"body":"Keeping 50 for now; it matches the support agreement. Open to revisit."}' >/dev/null
  gl PUT "/projects/$API_P/merge_requests/$MR_RATE/discussions/$d3?resolved=true" >/dev/null
  log "MR threads"
fi

# --- finished work (sprint review data) -------------------------------------
B_FONT="$C_FONT-$(slug "Fix truncated column headers in export preview")"
branch_with_commit "$WEB_P" "$B_FONT" "src/preview/header.css" \
  $'.preview th {\n  overflow: hidden;\n  text-overflow: ellipsis;\n}\n' "Ellipsize long headers"
MR_FONT=$(mr "$WEB_P" "$B_FONT" "Fix truncated column headers in export preview" \
  "$(printf 'Closes #%s\n\nPart of %s/tracker#%s.' "$C_FONT" "$GROUP" "$T_FONT")" no)
if [[ $(gl GET "/projects/$WEB_P/merge_requests/$MR_FONT" | jq -r .state) == opened ]]; then
  for _ in 1 2 3 4 5 6 7 8 9 10; do # mergeability is computed asynchronously
    sleep 3
    if (gl PUT "/projects/$WEB_P/merge_requests/$MR_FONT/merge" '{}' >/dev/null 2>&1); then
      log "merged web!$MR_FONT"
      break
    fi
  done
fi
close_issue "$TRACKER" "$T_FONT"
close_issue "$WEB_P" "$C_FONT"
close_issue "$TRACKER" "$T_DEPS"
close_issue "$API_P" "$C_DEPS"

# --- board ------------------------------------------------------------------
BOARD=$(gl GET "/projects/$TRACKER/boards" | jq -r 'map(select(.name=="Delivery"))[0].id // empty')
if [[ -z $BOARD ]]; then
  BOARD=$(gl POST "/projects/$TRACKER/boards" '{"name":"Delivery"}' | jq -r .id)
  for l in "workflow::ready" "workflow::in progress" "workflow::review"; do
    lid=$(gl GET "/groups/$GID/labels?search=$(enc "$l")" | jq -r --arg n "$l" 'map(select(.name==$n))[0].id')
    gl POST "/projects/$TRACKER/boards/$BOARD/lists" "$(jq -n --argjson id "$lid" '{label_id:$id}')" >/dev/null
  done
  log "board Delivery"
fi

cat <<EOF
Done.
  group     $GITLAB_URL/$GROUP
  tracker   #$T_RATE #$T_AUDIT #$T_WEBHOOK #$T_CSV #$T_TZ #$T_BILL #$T_DARK #$T_SCHED #$T_FONT #$T_DEPS
  api       child #$C_RATE  MR !$MR_RATE (ready, 2 open threads)   child #$C_DEPS (closed)
  web       child #$C_AUDIT MR !$MR_AUDIT (draft)                  child #$C_FONT  MR !$MR_FONT (merged)
EOF
