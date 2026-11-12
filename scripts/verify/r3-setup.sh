#!/usr/bin/env bash
# Round 3 acceptance: `ade setup`, the non-interactive idempotent first start.
# Run from the repo root inside `nix develop`. The GitLab sandbox part needs
# .env with GITLAB_PAT (read-only forge calls, a handful of requests); the demo
# part needs nothing (fake GitLab with real local git remotes).
#
# Never touches ~/.config/ade or the real `ade` zellij session: every run uses
# a throwaway HOME, and --session uses the throwaway session ade-test-setup,
# deleted on exit.
. scripts/verify/lib.sh

SESSION=ade-test-setup
PIDS=()
cleanup() {
  zellij delete-session --force "$SESSION" >/dev/null 2>&1 || true
  [[ ${#PIDS[@]} -gt 0 ]] && kill "${PIDS[@]}" 2>/dev/null || true
  rm -rf "$VERIFY_TMP"
}
trap cleanup EXIT

step "unit tests"
check "onboard: lock, tool advice, template fetch" go test ./internal/onboard/... -run 'Lock|CheckTools|MissingAdvice|SplitList|FetchTemplate'
check "cli: setup from env, existing config, partial exit, concurrency, session" \
  go test ./internal/cli/... -run 'Setup|ApplySetupEnv'

build_ade
go build -o "$VERIFY_TMP/fakegl" ./scripts/verify/fakegl

# fresh_home NAME: an empty HOME for one machine; sets M (the home), no config.
fresh_home() {
  M="$VERIFY_TMP/$1"
  mkdir -p "$M"
}

# run_setup OUT_JSON [args...]: ade setup --json on machine $M; returns its exit code.
run_setup() {
  local out=$1 rc=0
  shift
  env -u XDG_CONFIG_HOME -u XDG_DATA_HOME -u XDG_CACHE_HOME -u ADE_CONFIG HOME="$M" \
    "$ADE" setup --json "$@" >"$out" 2>"$out.log" || rc=$?
  return $rc
}

# ------------------------------------------------------------------ demo fake
step "demo fake GitLab with git remotes: fresh HOME, config from ADE_SETUP_* env"
mkdir -p "$VERIFY_TMP/remotes"
"$VERIFY_TMP/fakegl" -tier premium -agile -git-root "$VERIFY_TMP/remotes" -git-apps "$REPO/scripts/sandbox/apps" \
  >"$VERIFY_TMP/gl.url" 2>"$VERIFY_TMP/gl.err" &
PIDS+=($!)
for _ in $(seq 50); do [[ -s $VERIFY_TMP/gl.url ]] && break; sleep 0.1; done
URL=$(head -1 "$VERIFY_TMP/gl.url")
[[ -n $URL ]] || {
  cat "$VERIFY_TMP/gl.err" >&2
  fail "fakegl did not start"
}
sed -e 's/^weight .*/weight     = { source = "gitlab" }/' \
  -e 's/^iterations = .*/iterations = { source = "milestones", overlap = 10 }/' \
  scripts/sandbox/manifests/gitlab.ade.toml |
  awk -v api="file://$VERIFY_TMP/remotes/api.git" -v web="file://$VERIFY_TMP/remotes/web.git" '
    {print}
    /^path +=.*\/api"/ {print "url  = \"" api "\""}
    /^path +=.*\/web"/ {print "url  = \"" web "\""}' >"$VERIFY_TMP/manifest.ade.toml"

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid
export FAKE_GITLAB_TOKEN=fake-token
export ADE_SETUP_FORGE=gitlab ADE_SETUP_HOST="$URL" ADE_SETUP_TOKEN_ENV=FAKE_GITLAB_TOKEN
export ADE_SETUP_GIT_PROTOCOL=https ADE_SETUP_PROJECT="$VERIFY_TMP/manifest.ade.toml"

fresh_home demo
export ADE_SETUP_ROOT="$M/ade"
CFG=$M/.config/ade/config.toml
[[ ! -e $CFG ]] || fail "fresh HOME already has a config"
run_setup "$VERIFY_TMP/s1.json" || {
  cat "$VERIFY_TMP/s1.json.log" >&2
  fail "first ade setup exited non-zero"
}
check "config written from env" test -f "$CFG"
check "config holds forge and project, never the token" bash -c "grep -q 'kind = \"gitlab\"' '$CFG' && grep -q 'manifest.ade.toml' '$CFG' && ! grep -q fake-token '$CFG'"
check "repos cloned with baseline worktrees" bash -c "test -d '$M/ade/demo-gl/api/main' && test -d '$M/ade/demo-gl/web/main'"
expect_json "status ok, config created" "$VERIFY_TMP/s1.json" '.status == "ok" and .config_created == true'
expect_json "first sync produced items" "$VERIFY_TMP/s1.json" '(.sync | length) >= 1 and (.sync[0].items > 0) and (.sync[0].error // "") == ""'
expect_json "zellij unlock-first preset written" "$VERIFY_TMP/s1.json" '(.zellij_config | length) > 0'
expect_json "tools reported" "$VERIFY_TMP/s1.json" '(.tools | length) >= 7'
check "setup.run is audited" grep -rq setup.run "$M/.local/share/ade"

step "rerun is a no-op with the same result"
SUM1=$(sha256sum "$CFG" | cut -d' ' -f1)
run_setup "$VERIFY_TMP/s2.json" || {
  cat "$VERIFY_TMP/s2.json.log" >&2
  fail "second ade setup exited non-zero"
}
[[ $(sha256sum "$CFG" | cut -d' ' -f1) == "$SUM1" ]] && pass "config untouched" || fail "config changed on rerun"
expect_json "second run: ok, nothing created, nothing cloned, nothing pulled" "$VERIFY_TMP/s2.json" \
  '.status == "ok" and .config_created == false and ([.add[]?.repos[]? | select(.cloned == true)] | length) == 0 and ([.update.repos[]? | select((.pulled // []) | length > 0)] | length) == 0'
[[ $(jq -c '[.sync[] | {project, items}]' "$VERIFY_TMP/s1.json") == "$(jq -c '[.sync[] | {project, items}]' "$VERIFY_TMP/s2.json")" ]] &&
  pass "same sync result" || fail "sync result differs between runs: $(jq -c '[.sync[] | {project, items}]' "$VERIFY_TMP/s1.json") vs $(jq -c '[.sync[] | {project, items}]' "$VERIFY_TMP/s2.json")"
pass "second run took $(jq .duration_ms "$VERIFY_TMP/s2.json") ms (first: $(jq .duration_ms "$VERIFY_TMP/s1.json") ms)"

step "rejected and empty token: partial exit 3, config kept, no clone, no prompt"
fresh_home badtoken
export ADE_SETUP_ROOT="$M/ade"
set +e
FAKE_GITLAB_TOKEN=wrong-token run_setup "$VERIFY_TMP/bad.json" </dev/null
RC=$?
set -e
[[ $RC == 3 ]] && pass "exit 3 for a rejected token" || {
  cat "$VERIFY_TMP/bad.json.log" >&2
  fail "exit $RC, want 3"
}
expect_json "partial, forge steps skipped, machine steps ran" "$VERIFY_TMP/bad.json" \
  '.status == "partial" and (.skipped | index("sync")) != null and (.tokens[0].error | length) > 0 and (.tools | length) > 0'
check "config exists, nothing cloned" bash -c "test -f '$M/.config/ade/config.toml' && ! test -d '$M/ade/demo-gl/api'"
set +e
FAKE_GITLAB_TOKEN= run_setup "$VERIFY_TMP/empty.json" </dev/null
RC=$?
set -e
[[ $RC == 3 ]] && pass "exit 3 for an empty token" || fail "exit $RC, want 3"
FAKE_GITLAB_TOKEN=fake-token run_setup "$VERIFY_TMP/fixed.json" || fail "rerun with the token fixed failed"
expect_json "fixing the token and rerunning completes the setup" "$VERIFY_TMP/fixed.json" \
  '.status == "ok" and .config_created == false and (.sync[0].items > 0)'

step "--session starts the throwaway session once, then finds it running"
if command -v zellij >/dev/null; then
  fresh_home sess
  export ADE_SETUP_ROOT="$M/ade"
  zellij delete-session --force "$SESSION" >/dev/null 2>&1 || true
  run_setup "$VERIFY_TMP/sess1.json" --session --session-name "$SESSION" || {
    cat "$VERIFY_TMP/sess1.json.log" >&2
    fail "setup --session failed"
  }
  expect_json "session started" "$VERIFY_TMP/sess1.json" '.session.status == "started"'
  sleep 1
  zellij list-sessions --no-formatting 2>/dev/null | grep -q "^$SESSION " && pass "zellij lists $SESSION" || fail "$SESSION not listed"
  run_setup "$VERIFY_TMP/sess2.json" --session --session-name "$SESSION" || fail "second setup --session failed"
  expect_json "second run finds it running" "$VERIFY_TMP/sess2.json" '.session.status == "running"'
  zellij delete-session --force "$SESSION" >/dev/null 2>&1 || true
  zellij list-sessions --no-formatting 2>/dev/null | grep -q "^$SESSION " && fail "$SESSION was not deleted" || pass "$SESSION deleted"
else
  fail "zellij not on PATH (run inside nix develop)"
fi

step "--config-from copies a team template"
fresh_home tmpl
unset ADE_SETUP_FORGE ADE_SETUP_HOST ADE_SETUP_TOKEN_ENV ADE_SETUP_GIT_PROTOCOL ADE_SETUP_PROJECT ADE_SETUP_ROOT
cat >"$VERIFY_TMP/team.toml" <<EOF
[forges.sandbox-gitlab]
kind = "gitlab"
url = "$URL"
token_env = "FAKE_GITLAB_TOKEN"
git_protocol = "https"
[paths]
root = "$M/ade"
[[projects]]
source = "$VERIFY_TMP/manifest.ade.toml"
EOF
run_setup "$VERIFY_TMP/tmpl.json" --config-from "$VERIFY_TMP/team.toml" || {
  cat "$VERIFY_TMP/tmpl.json.log" >&2
  fail "setup --config-from failed"
}
cmp -s "$VERIFY_TMP/team.toml" "$M/.config/ade/config.toml" && pass "config is the template" || fail "config differs from the template"
expect_json "template project cloned and synced" "$VERIFY_TMP/tmpl.json" '.status == "ok" and (.sync[0].items > 0)'

# --------------------------------------------------------------- GitLab sandbox
step "GitLab sandbox (ade-demo-env): fresh HOME, token from the environment"
if [[ -f .env ]]; then
  set -a
  . ./.env
  set +a
  fresh_home sandbox
  export ADE_SETUP_FORGE=gitlab ADE_SETUP_HOST=https://gitlab.com ADE_SETUP_TOKEN_ENV=GITLAB_PAT
  export ADE_SETUP_GIT_PROTOCOL=https ADE_SETUP_PROJECT="$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
  export ADE_SETUP_ROOT="$M/ade"
  run_setup "$VERIFY_TMP/sb1.json" || {
    cat "$VERIFY_TMP/sb1.json.log" >&2
    fail "setup against the sandbox failed"
  }
  expect_json "ok; config created; repos cloned; sync done" "$VERIFY_TMP/sb1.json" \
    '.status == "ok" and .config_created == true and (.sync[0].items > 0) and (.sync[0].error // "") == ""'
  check "tracker-linked repos on disk" bash -c "test -d '$M/ade/demo-gl/api/main' && test -d '$M/ade/demo-gl/web/main'"
  SUMSB=$(sha256sum "$M/.config/ade/config.toml" | cut -d' ' -f1)
  run_setup "$VERIFY_TMP/sb2.json" || fail "sandbox rerun failed"
  [[ $(sha256sum "$M/.config/ade/config.toml" | cut -d' ' -f1) == "$SUMSB" ]] && pass "config untouched on rerun" || fail "config changed on rerun"
  expect_json "rerun is a no-op with the same sync" "$VERIFY_TMP/sb2.json" \
    '.status == "ok" and .config_created == false and ([.add[]?.repos[]? | select(.cloned == true)] | length) == 0'
  [[ $(jq -c '[.sync[] | {project, items}]' "$VERIFY_TMP/sb1.json") == "$(jq -c '[.sync[] | {project, items}]' "$VERIFY_TMP/sb2.json")" ]] &&
    pass "same sync result" || fail "sandbox sync differs between runs"
else
  fail "no .env with GITLAB_PAT: the sandbox part of this script needs it"
fi

step "done"
pass "ade setup verified"
