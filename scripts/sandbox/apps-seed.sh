#!/usr/bin/env bash
# Push the sandbox app code (scripts/sandbox/apps/<repo>) to the main branch of
# the GitLab sandbox repos, so workspaces have something real to run: api serves
# /healthz on $PORT, web serves a page and /healthz that calls $API_URL/healthz.
#
# Idempotent: a repo whose main already holds these files is left alone.
# Synthetic content only. Needs: git, GITLAB_PAT (e.g. via .env).
#
# Env: GITLAB_URL (default https://gitlab.com), ADE_SANDBOX_GROUP (default ade-demo-env)
set -euo pipefail

: "${GITLAB_PAT:?set GITLAB_PAT (e.g. via .env)}"
GITLAB_URL="${GITLAB_URL:-https://gitlab.com}"
GROUP="${ADE_SANDBOX_GROUP:-ade-demo-env}"
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

AUTH=$(printf 'oauth2:%s' "$GITLAB_PAT" | base64 -w0)

# sgit REMOTE_URL ARGS...: git with the token as a per-command header (never
# written to .git/config). The insteadOf pin beats a user rule that would
# rewrite https to ssh and bypass the token (same trick as internal/git).
sgit() {
  local url=$1
  shift
  env GIT_TERMINAL_PROMPT=0 GIT_CONFIG_COUNT=2 \
    GIT_CONFIG_KEY_0=http.extraHeader "GIT_CONFIG_VALUE_0=Authorization: Basic $AUTH" \
    GIT_CONFIG_KEY_1="url.$url.insteadOf" "GIT_CONFIG_VALUE_1=$url" \
    git "$@"
}

for repo in api web; do
  url="$GITLAB_URL/$GROUP/$repo.git"
  work="$TMP/$repo"
  sgit "$url" clone -q --depth 1 "$url" "$work"
  # Replace our files only; anything else in the repo (README, ...) stays.
  cp "$HERE/apps/$repo/main.go" "$HERE/apps/$repo/go.mod" "$work/"
  if [[ -z $(git -C "$work" status --porcelain) ]]; then
    echo "  $repo: main already has the sandbox app"
    continue
  fi
  git -C "$work" add main.go go.mod
  git -C "$work" -c user.name="ADE Sandbox" -c user.email="sandbox@example.invalid" \
    commit -q -m "Add sandbox $repo server for workspace runs"
  branch=$(git -C "$work" symbolic-ref --short HEAD)
  sgit "$url" -C "$work" push -q origin "HEAD:$branch"
  echo "  $repo: pushed to $branch"
done
