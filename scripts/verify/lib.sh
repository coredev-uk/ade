# Shared helpers for scripts/verify/phaseN.sh. Source from the repo root inside
# `nix develop`. Every check prints PASS/FAIL; the script exits non-zero on the
# first failure.
set -euo pipefail

REPO=$(pwd)
VERIFY_TMP=$(mktemp -d)
trap 'rm -rf "$VERIFY_TMP"' EXIT

pass() { printf '  PASS  %s\n' "$*"; }
fail() {
  printf '  FAIL  %s\n' "$*" >&2
  exit 1
}
step() { printf '\n== %s\n' "$*"; }

# check DESCRIPTION CMD...: run CMD, PASS on exit 0.
check() {
  local desc=$1
  shift
  if "$@" >"$VERIFY_TMP/out" 2>&1; then pass "$desc"; else
    cat "$VERIFY_TMP/out" >&2
    fail "$desc"
  fi
}

# expect_json DESCRIPTION FILE JQ: PASS when the jq expression is true.
expect_json() {
  local desc=$1 file=$2 expr=$3
  if [[ $(jq -r "$expr" "$file") == true ]]; then pass "$desc"; else
    jq -c . "$file" | head -c 2000 >&2
    fail "$desc ($expr)"
  fi
}

# build_ade: compile the binary under test once.
build_ade() {
  go build -o "$VERIFY_TMP/ade" ./cmd/ade
  ADE="$VERIFY_TMP/ade"
}

# sandbox_gitlab: isolated state + a config holding only the GitLab sandbox
# project (needs GITLAB_PAT in .env).
sandbox_gitlab() {
  [[ -f .env ]] || fail "sandbox needs .env with GITLAB_PAT"
  set -a
  . ./.env
  set +a
  export XDG_DATA_HOME="$VERIFY_TMP/data" XDG_CACHE_HOME="$VERIFY_TMP/cache"
  export ADE_CONFIG="$VERIFY_TMP/config.toml"
  cat >"$ADE_CONFIG" <<EOF
[user]
handle = "sandbox"
[forges.sandbox-gitlab]
kind = "gitlab"
token_env = "GITLAB_PAT"
git_protocol = "https"
[paths]
root = "$VERIFY_TMP/projects"
[[projects]]
source = "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
EOF
}
