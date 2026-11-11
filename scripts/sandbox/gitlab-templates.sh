#!/usr/bin/env bash
# Give the sandbox `api` project a merge request template, for Start work (Phase 3).
#
# Idempotent: creates .gitlab/merge_request_templates/Default.md on the default
# branch, or updates it when its content differs. The template carries a
# `/label` and `/assign_reviewer @<token user>` quick action (the forge runs them when an
# MR is created from it), which is what the "quick actions listed and strippable"
# acceptance exercises. The token's own user is the only user of the sandbox, and GitLab
# Free lets an author request their own review, so the reviewer shows on the MR.
# All content is fictional. Needs: curl, jq.
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
PROJECT=$(jq -rn --arg v "$GROUP/api" '$v|@uri')
FILE=$(jq -rn --arg v ".gitlab/merge_request_templates/Default.md" '$v|@uri')

BODY=$(mktemp)
HDR=$(mktemp)
trap 'rm -f "$BODY" "$HDR"' EXIT
# gl METHOD PATH [JSON]: response body in $BODY, HTTP status in $STATUS.
# Retries 429s, honouring Retry-After (GitLab.com rate-limits per endpoint).
gl() {
  local args=(-sS -o "$BODY" -D "$HDR" -w '%{http_code}' -X "$1" -H "PRIVATE-TOKEN: $GITLAB_PAT") attempt wait
  [[ -n ${3:-} ]] && args+=(-H 'Content-Type: application/json' --data "$3")
  for attempt in 1 2 3 4 5 6; do
    STATUS=$(curl "${args[@]}" "$API$2")
    [[ $STATUS != 429 ]] && return 0
    wait=$(awk 'tolower($1)=="retry-after:" {print $2+0}' "$HDR")
    printf '  429 on %s %s; retry %s in %ss\n' "$1" "$2" "$attempt" "${wait:-30}" >&2
    sleep "${wait:-30}"
  done
}

gl GET "/user"
((STATUS == 200)) || {
  echo "token user: HTTP $STATUS" >&2
  exit 1
}
REVIEWER=$(jq -r .username "$BODY")

read -r -d '' CONTENT <<TEMPLATE || true
## Summary

What changes, and why.

## Testing

- [ ] Tests added or updated
- [ ] Checked locally

/label ~"type::story"
/assign_reviewer @$REVIEWER
TEMPLATE
CONTENT+=$'\n'

gl GET "/projects/$PROJECT"
((STATUS == 200)) || {
  echo "project $GROUP/api: HTTP $STATUS" >&2
  exit 1
}
BRANCH=$(jq -r .default_branch "$BODY")
gl GET "/projects/$PROJECT/repository/files/$FILE/raw?ref=$BRANCH"
if [[ $STATUS == 200 ]]; then
  if [[ $(cat "$BODY") == "${CONTENT%$'\n'}" ]]; then
    echo "template up to date: $GROUP/api $BRANCH:.gitlab/merge_request_templates/Default.md"
    exit 0
  fi
  method=PUT verb=updated
else
  method=POST verb=created
fi
body=$(jq -n --arg b "$BRANCH" --arg c "$CONTENT" \
  '{branch:$b, content:$c, commit_message:"Add MR template for ADE start-work sandbox"}')
gl "$method" "/projects/$PROJECT/repository/files/$FILE" "$body"
((STATUS < 300)) || {
  echo "template $verb failed: HTTP $STATUS: $(cat "$BODY")" >&2
  exit 1
}
echo "template $verb: $GROUP/api $BRANCH:.gitlab/merge_request_templates/Default.md"
