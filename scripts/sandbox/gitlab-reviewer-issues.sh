#!/usr/bin/env bash
# Seed two untouched tracker issues for the Phase 3 "/assign_reviewer" acceptance:
# one is started with the template's quick action stripped, the other with it kept,
# so the sandbox shows both outcomes on real merge requests.
#
# Idempotent: an issue with the same title is left alone. Each issue is app::api,
# weight 1, in Sprint 42 and assigned to the token's user, like the other sandbox
# work. All content is fictional. Needs: curl, jq.
#
# Prints STRIP_IID=<iid> and KEEP_IID=<iid> (the tracker issues) on stdout.
#
# Env:
#   GITLAB_PAT          personal access token with `api` scope (required)
#   GITLAB_URL          default https://gitlab.com
#   ADE_SANDBOX_GROUP   group path, default ade-demo-env
set -euo pipefail

: "${GITLAB_PAT:?set GITLAB_PAT (e.g. via .env)}"
GITLAB_URL="${GITLAB_URL:-https://gitlab.com}"
GROUP="${ADE_SANDBOX_GROUP:-ade-demo-env}"
API="$GITLAB_URL/api/v4"
enc() { jq -rn --arg v "$1" '$v|@uri'; }

BODY=$(mktemp)
HDR=$(mktemp)
trap 'rm -f "$BODY" "$HDR"' EXIT
# gl METHOD PATH [JSON]: response body on stdout; retries 429s (Retry-After).
gl() {
  local args=(-sS -o "$BODY" -D "$HDR" -w '%{http_code}' -X "$1" -H "PRIVATE-TOKEN: $GITLAB_PAT") attempt wait status
  [[ -n ${3:-} ]] && args+=(-H 'Content-Type: application/json' --data "$3")
  for attempt in 1 2 3 4 5 6; do
    status=$(curl "${args[@]}" "$API$2")
    [[ $status != 429 ]] && break
    wait=$(awk 'tolower($1)=="retry-after:" {print $2+0}' "$HDR")
    printf '  429 on %s %s; retry %s in %ss\n' "$1" "$2" "$attempt" "${wait:-30}" >&2
    sleep "${wait:-30}"
  done
  ((status < 400)) || {
    printf 'GitLab %s %s -> %s: %s\n' "$1" "$2" "$status" "$(cat "$BODY")" >&2
    return 1
  }
  cat "$BODY"
}

ME_ID=$(gl GET /user | jq -r .id)
MILESTONE=$(gl GET "/groups/$(enc "$GROUP")/milestones?title=$(enc "Sprint 42")&include_ancestors=false" | jq -r '.[0].id // empty')
[[ -n $MILESTONE ]] || {
  echo "milestone Sprint 42 missing: run scripts/sandbox/gitlab-seed.sh first" >&2
  exit 1
}
TRACKER=$(enc "$GROUP/tracker")

# issue TITLE BODY -> iid
issue() {
  local title=$1 found
  found=$(gl GET "/projects/$TRACKER/issues?state=all&in=title&search=$(enc "$title")&per_page=100" |
    jq -r --arg t "$title" 'map(select(.title == $t))[0].iid // empty')
  if [[ -z $found ]]; then
    found=$(gl POST "/projects/$TRACKER/issues" "$(jq -n --arg t "$title" --arg d "$2" --argjson m "$MILESTONE" --argjson a "[$ME_ID]" \
      '{title: $t, description: $d, labels: "type::story,app::api,weight::1", milestone_id: $m, assignee_ids: $a}')" | jq -r .iid)
    echo "  created tracker#$found: $title" >&2
  fi
  echo "$found"
}

STRIP_IID=$(issue "Add request IDs to export log lines" "Correlate each export log line with the request that started it.")
KEEP_IID=$(issue "Include row counts in export audit entries" "Audit entries for exports should say how many rows were written.")
printf 'STRIP_IID=%s\nKEEP_IID=%s\n' "$STRIP_IID" "$KEEP_IID"
