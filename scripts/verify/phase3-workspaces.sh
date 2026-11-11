#!/usr/bin/env bash
# Phase 3, workspace half (docs/PLAN.md §8 row 3, §2.1, §6.6, §6.7): run graph with
# ports, env injection, readiness and shared instances against the real sandbox apps;
# VS Code tasks.json (checked by running the generated tasks the way VS Code would);
# a throwaway zellij tab from the generated layout; the ade-nvim headless smoke test.
#
# Sourced by phase3.sh, or run alone from the repo root inside `nix develop`.
# Needs .env with GITLAB_PAT, zellij, nvim, jq, curl, script (util-linux) and go.
[[ -n ${VERIFY_TMP:-} ]] || . scripts/verify/lib.sh

step "workspace unit tests"
check "readiness probes (http, tcp, log), ports, run graph, layouts, tasks.json export" \
  go test ./internal/readiness/... ./internal/ports/... ./internal/workspace/...
[[ -n ${ADE:-} && -x ${ADE:-} ]] || build_ade
[[ -f .env ]] || fail "sandbox needs .env with GITLAB_PAT"

step "sandbox apps (db on \$PORT, api /healthz reports the db at \$DB_ADDR, web calls \$API_URL/healthz)"
# A subshell keeps this phase's isolated state, env and cleanup trap away from the caller.
(
  set -euo pipefail
  set -a
  . ./.env
  set +a
  WS=$(mktemp -d -p "$VERIFY_TMP" ws.XXXXXX)
  SESSION=ade-test-ws

  # Keep the shared Go build cache: the sandbox apps compile on `go run .`.
  export GOCACHE=${GOCACHE:-$(go env GOCACHE)}
  export GOMODCACHE=${GOMODCACHE:-$(go env GOMODCACHE)}
  check "apps-seed.sh pushes (or finds) the api (with its synthetic db) and web servers on main" bash scripts/sandbox/apps-seed.sh

  export XDG_DATA_HOME="$WS/data" XDG_CACHE_HOME="$WS/cache" XDG_CONFIG_HOME="$WS/config"
  export ADE_CONFIG="$WS/config.toml"
  unset ZELLIJ ZELLIJ_SESSION_NAME ZELLIJ_PANE_ID TERM_PROGRAM
  export EDITOR=cat # a long-lived stand-in for the editor pane
  cat >"$ADE_CONFIG" <<EOF
[user]
handle = "sandbox"
[host]
mode = "zellij"
[forges.sandbox-gitlab]
kind = "gitlab"
token_env = "GITLAB_PAT"
git_protocol = "https"
[paths]
root = "$WS/projects"
[[projects]]
source = "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
EOF
  PROJECTS="$WS/projects/demo-gl"
  API="$PROJECTS/api"
  WEB="$PROJECTS/web"

  cleanup() {
    set +e
    for tree in "$WEB"/*/ "$API"/*/; do
      [[ -d $tree ]] && "$ADE" ws down "$tree" >/dev/null 2>&1
    done
    "$ADE" ws status --json 2>/dev/null | jq -r '.[].pid // empty' | while read -r pid; do kill -TERM -- "-$pid" 2>/dev/null; done
    pkill -f "zellij attach $SESSION" 2>/dev/null
    env -u ZELLIJ -u ZELLIJ_SESSION_NAME -u ZELLIJ_PANE_ID zellij delete-session --force "$SESSION" >/dev/null 2>&1
    [[ -n ${FG_PID:-} ]] && kill -TERM "$FG_PID" 2>/dev/null
    return 0
  }
  trap cleanup EXIT

  # json_get FILE JQ: print a jq result.
  jget() { jq -r "$2" "$1"; }
  # health PORT: GET /healthz of a loopback port.
  health() { curl -fsS --max-time 5 "http://127.0.0.1:$1/healthz"; }
  # db_cmd PORT LINE: one request to the synthetic db's line protocol.
  db_cmd() {
    local reply
    exec 3<>"/dev/tcp/127.0.0.1/$1"
    printf '%s\n' "$2" >&3
    read -r -t 5 reply <&3 || true
    exec 3<&- 3>&-
    printf '%s' "$reply"
  }
  # epoch RFC3339: seconds (with fraction) since the epoch.
  epoch() { date -d "$1" +%s.%N; }
  # wait_for SECONDS CMD...: retry CMD once a second.
  wait_for() {
    local s=$1 i
    shift
    for ((i = 0; i < s; i++)); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done
    return 1
  }

  step "project add: clone api and web, validate the run graph in the manifest"
  check "ade project add (api/web clones, validated manifest incl. tasks and workspaces)" \
    "$ADE" project add "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
  [[ -f $API/main/main.go && -f $WEB/main/main.go ]] || fail "baseline trees lack the sandbox apps"
  git -C "$WEB/.bare" worktree add -q --track -b 901-second "$WEB/901-second" origin/main
  pass "second web worktree 901-second created beside main"

  step "cycles are rejected with the path"
  cat >"$WS/cycle.ade.toml" <<'EOF'
name  = "cycle-demo"
forge = "sandbox-gitlab"

[repos.api]
path = "ade-demo-env/api"
app  = ["api"]

[repos.api.tasks.a]
cmd      = "true"
requires = ["api.b"]

[repos.api.tasks.b]
cmd      = "true"
requires = ["api.a"]
EOF
  if "$ADE" project add "$WS/cycle.ade.toml" >"$WS/cycle.out" 2>&1; then
    cat "$WS/cycle.out" >&2
    fail "ade project add accepted a manifest with a dependency cycle"
  fi
  grep -q 'dependency cycle: api.a → api.b → api.a' "$WS/cycle.out" && pass "project add exits non-zero and names the cycle: api.a → api.b → api.a" || { cat "$WS/cycle.out" >&2; fail "no cycle path in the error"; }
  [[ ! -e $WS/projects/cycle-demo ]] && pass "nothing was cloned or registered for the rejected manifest" || fail "the rejected manifest touched the disk"

  step "run graph without zellij: db -> api -> web (shared baseline instances)"
  "$ADE" ws up --no-zellij --detach "$WEB/main" --json >"$WS/up1.json"
  U1="$WS/up1.json"
  expect_json "api.check (oneshot), api.db, api.dev and web.dev are all ready, in that dependency order" "$U1" \
    '[.[].node] == ["api.check","api.db","api.dev","web.dev"] and all(.[]; .outcome == "ready")'
  expect_json "api runs in the api baseline tree" "$U1" \
    ".[] | select(.node == \"api.dev\") | .tree == \"$API/main\""
  expect_json "ports are allocated and distinct" "$U1" \
    '([.[] | select(.node != "api.check") | .port] | length == 3 and (unique | length) == 3 and all(. > 1024))'
  API_PORT=$(jget "$U1" '.[] | select(.node == "api.dev") | .port')
  WEB_PORT=$(jget "$U1" '.[] | select(.node == "web.dev") | .port')
  API_PID=$(jget "$U1" '.[] | select(.node == "api.dev") | .pid')
  DB_PORT=$(jget "$U1" '.[] | select(.node == "api.db") | .port')
  [[ $(db_cmd "$DB_PORT" PING) == PONG ]] && pass "the db answers PING on :$DB_PORT" || fail "db does not answer on :$DB_PORT"
  health "$API_PORT" >"$WS/api1.json"
  expect_json "api /healthz answers on :$API_PORT and reports the db at :$DB_PORT reachable (DB_ADDR wired from the db's port)" "$WS/api1.json" \
    ".service == \"api\" and .status == \"ok\" and .db.reachable == true and .db.addr == \"127.0.0.1:$DB_PORT\""
  "$ADE" ws status --json >"$WS/status0.json"
  T_DB=$(epoch "$(jget "$WS/status0.json" '.[] | select(.node == "api.db") | .started_at')")
  T_API=$(epoch "$(jget "$WS/status0.json" '.[] | select(.node == "api.dev") | .started_at')")
  T_WEB=$(epoch "$(jget "$WS/status0.json" '.[] | select(.node == "web.dev") | .started_at')")
  awk -v a="$T_DB" -v b="$T_API" -v c="$T_WEB" 'BEGIN { exit !(a < b && b < c) }' && pass "start order db -> api -> web (the api was spawned only after the db was ready, web after api's /healthz)" \
    || fail "start order wrong: db $T_DB api $T_API web $T_WEB"
  health "$WEB_PORT" >"$WS/web1.json"
  expect_json "web /healthz reports the api reachable at the injected API_URL (:$API_PORT)" "$WS/web1.json" \
    ".service == \"web\" and .api.reachable == true and .api.url == \"http://127.0.0.1:$API_PORT\""

  step "a second web worktree reuses the same api instance"
  "$ADE" ws up --no-zellij --detach "$WEB/901-second" --json >"$WS/up2.json"
  U2="$WS/up2.json"
  expect_json "api.dev is shared: same pid and port, not started again" "$U2" \
    ".[] | select(.node == \"api.dev\") | .shared == true and .pid == $API_PID and .port == $API_PORT"
  expect_json "web.dev of the second worktree is its own instance on its own port" "$U2" \
    ".[] | select(.node == \"web.dev\") | .outcome == \"ready\" and (.shared // false) == false and .port != $WEB_PORT"
  WEB2_PORT=$(jget "$U2" '.[] | select(.node == "web.dev") | .port')
  health "$WEB2_PORT" >"$WS/web2.json"
  expect_json "second web also reaches the shared api" "$WS/web2.json" ".api.reachable == true and .api.url == \"http://127.0.0.1:$API_PORT\""
  "$ADE" ws status --json >"$WS/status.json"
  expect_json "status: api.dev has two holders, each web one" "$WS/status.json" \
    '([.[] | select(.node == "api.dev")] | length == 1 and .[0].alive and (.[0].holders | length) == 2) and
     ([.[] | select(.node == "web.dev")] | length == 2 and all(.[]; (.holders | length) == 1))'
  "$ADE" ws up --no-zellij --detach "$WEB/main" --json >"$WS/up1b.json"
  expect_json "re-running up for the first worktree is idempotent (everything shared, same ports)" "$WS/up1b.json" \
    "all(.[]; .outcome == \"ready\" and .shared == true) and ([.[] | select(.node == \"web.dev\") | .port] == [$WEB_PORT])"

  step "stopping: the shared api outlives its first dependent and stops with the last"
  "$ADE" ws down "$WEB/901-second" --json >"$WS/down2.json"
  expect_json "second worktree down stops only its web" "$WS/down2.json" '[.[].node] == ["web.dev"]'
  health "$API_PORT" >/dev/null && pass "api still serving for the first worktree" || fail "api stopped while web/main still needs it"
  ! health "$WEB2_PORT" >/dev/null 2>&1 && pass "second web is gone" || fail "second web still serving"
  "$ADE" ws down "$WEB/main" --json >"$WS/down1.json"
  expect_json "last dependent down stops web, then api, then the oneshot record (dependents first)" "$WS/down1.json" \
    '[.[].node] == ["web.dev","api.dev","api.db","api.check"]'
  ! health "$API_PORT" >/dev/null 2>&1 && pass "api stopped with its last dependent" || fail "api still serving"
  ! kill -0 "$API_PID" 2>/dev/null && pass "api process is gone" || fail "api pid $API_PID still alive"
  [[ $("$ADE" ws status --json | jq length) == 0 ]] && pass "no instances left" || fail "instances left behind"

  step "ports are stable per worktree across restarts"
  "$ADE" ws up --no-zellij --detach "$WEB/main" --json >"$WS/up3.json"
  expect_json "web/main gets the same web and api ports as before the restart" "$WS/up3.json" \
    "([.[] | select(.node == \"web.dev\") | .port] == [$WEB_PORT]) and ([.[] | select(.node == \"api.dev\") | .port] == [$API_PORT])"
  "$ADE" ws down "$WEB/main" >/dev/null

  step "a work item with its own api branch gets its own api (tracker#3, start-work worktree)"
  # `ade start` finds the child, branch and MR phase3.sh made on the forge and only adds the
  # worktree here (no workspace): the same worktree the item's workspace opens on.
  "$ADE" start ade-demo-env/tracker#3 --repo api --no-workspace --json >"$WS/start3.json"
  WT3=$(jget "$WS/start3.json" '.targets[0].worktree')
  BR3=$(jget "$WS/start3.json" '.targets[0].branch')
  [[ $BR3 == *-retry-failed-webhook-deliveries && $WT3 == "$API/$BR3" ]] && pass "item tree: $WT3 (branch $BR3)" || fail "unexpected start-work worktree $WT3 / branch $BR3"
  # The branch predates the sandbox app: bring the server into the tree (local, never pushed).
  git -C "$WT3" checkout -q origin/main -- main.go go.mod
  git -C "$WEB/.bare" worktree add -q --track -b 902-item "$WEB/902-item" origin/main
  "$ADE" ws up --no-zellij --detach "$WEB/main" --json >"$WS/upb.json"
  BASE_API_PORT=$(jget "$WS/upb.json" '.[] | select(.node == "api.dev") | .port')
  BASE_API_PID=$(jget "$WS/upb.json" '.[] | select(.node == "api.dev") | .pid')
  BASE_DB_PORT=$(jget "$WS/upb.json" '.[] | select(.node == "api.db") | .port')
  "$ADE" ws up --no-zellij --detach "$WEB/902-item" --item-tree "api=$WT3" --json >"$WS/upi.json"
  expect_json "api.dev runs from the item's own api tree, as its own instance" "$WS/upi.json" \
    ".[] | select(.node == \"api.dev\") | .outcome == \"ready\" and .tree == \"$WT3\" and (.shared // false) == false and .pid != $BASE_API_PID"
  expect_json "its api (and db) ports differ from the baseline's :$BASE_API_PORT/:$BASE_DB_PORT" "$WS/upi.json" \
    "(.[] | select(.node == \"api.dev\") | .port != $BASE_API_PORT) and (.[] | select(.node == \"api.db\") | .port != $BASE_DB_PORT and .tree == \"$WT3\")"
  ITEM_API_PORT=$(jget "$WS/upi.json" '.[] | select(.node == "api.dev") | .port')
  ITEM_WEB_PORT=$(jget "$WS/upi.json" '.[] | select(.node == "web.dev") | .port')
  health "$ITEM_WEB_PORT" >"$WS/webi.json"
  expect_json "the item's web reaches the item's api (API_URL names :$ITEM_API_PORT)" "$WS/webi.json" \
    ".api.reachable == true and .api.url == \"http://127.0.0.1:$ITEM_API_PORT\""
  health "$WEB_PORT" >"$WS/webb.json"
  expect_json "the baseline web still talks to the baseline api, which was left alone (same pid)" "$WS/webb.json" \
    ".api.reachable == true and .api.url == \"http://127.0.0.1:$BASE_API_PORT\""
  "$ADE" ws status --json >"$WS/statusi.json"
  expect_json "status lists two api.dev instances, one per tree" "$WS/statusi.json" \
    "[.[] | select(.node == \"api.dev\") | .tree] | sort == ([\"$API/main\", \"$WT3\"] | sort)"
  # An explicit pin picks the same tree by branch name.
  "$ADE" ws down "$WEB/902-item" >/dev/null
  "$ADE" ws up --no-zellij --detach "$WEB/902-item" --with "api=$BR3" --json >"$WS/upp.json"
  expect_json "--with api=<branch> pins the same tree: same api port as the item-tree run" "$WS/upp.json" \
    ".[] | select(.node == \"api.dev\") | .tree == \"$WT3\" and .port == $ITEM_API_PORT"
  "$ADE" ws down "$WEB/902-item" >/dev/null
  health "$BASE_API_PORT" >/dev/null && pass "baseline api still serving after the item's workspace went down" || fail "baseline api stopped"
  ! health "$ITEM_API_PORT" >/dev/null 2>&1 && pass "the item's api stopped with its workspace" || fail "item api still serving"
  "$ADE" ws down "$WEB/main" >/dev/null
  [[ $("$ADE" ws status --json | jq length) == 0 ]] && pass "no instances left" || fail "instances left behind"

  step "foreground supervision: Ctrl-C stops what this workspace started"
  "$ADE" ws up --no-zellij "$WEB/main" >"$WS/fg.out" 2>&1 &
  FG_PID=$!
  wait_for 180 grep -q 'Ctrl-C stops' "$WS/fg.out" || {
    cat "$WS/fg.out" >&2
    fail "foreground ws up never became ready"
  }
  grep -q 'services: api.check ready' "$WS/fg.out" && pass "readiness is printed (services strip)" || fail "no services strip in output"
  FG_WEB=$(jq -r --arg d "$WEB/main" '.[] | select(.tree == $d and .node == "web.dev") | .port' <("$ADE" ws status --json))
  [[ $(health "$FG_WEB" | jq -r .api.reachable) == true ]] && pass "web reaches api while supervised" || fail "web cannot reach api"
  kill -TERM "$FG_PID"
  wait "$FG_PID" || true
  FG_PID=
  [[ $("$ADE" ws status --json | jq length) == 0 ]] && pass "SIGTERM released and stopped both instances" || fail "instances left after SIGTERM"
  ! health "$API_PORT" >/dev/null 2>&1 && pass "api port closed" || fail "api still serving after SIGTERM"

  step "task events and run records: inbox rows, and a record in the work item's directory"
  check "ade sync (the cache maps branches to work items)" "$ADE" sync
  "$ADE" inbox --json >"$WS/inbox.json"
  expect_json "the earlier runs left task.ready for api.dev and web.dev and task.done for api.check in the inbox" "$WS/inbox.json" \
    '([.[] | select(.kind == "task.ready") | .title | split(" ")[0]] | contains(["api.dev","web.dev"])) and
     ([.[] | select(.kind == "task.done") | .title | split(" ")[0]] | contains(["api.check"])) and
     all(.[] | select(.kind | startswith("task.")); .severity == "success")'
  api_ready() { "$ADE" inbox --json | jq '[.[] | select(.kind == "task.ready" and (.title | startswith("api.dev ")))] | length'; }
  c0=$(api_ready)
  "$ADE" ws up --no-zellij --detach "$WEB/main" >/dev/null
  "$ADE" ws up --no-zellij --detach "$WEB/901-second" >/dev/null
  c1=$(api_ready)
  "$ADE" ws down "$WEB/901-second" >/dev/null
  "$ADE" ws down "$WEB/main" >/dev/null
  [[ $((c1 - c0)) == 1 ]] && pass "two workspaces sharing one api raised one task.ready for it" || fail "task.ready count moved by $((c1 - c0)), want 1"

  # A worktree whose build is broken: api.check fails, so api.dev is blocked, never started.
  git -C "$API/.bare" worktree add -q --track -b broken-build "$API/broken-build" origin/main
  printf 'package main\n\nthis is not go\n' >"$API/broken-build/broken.go"
  if "$ADE" ws up --no-zellij --run api.dev "$API/broken-build" >"$WS/broken.out" 2>&1; then
    cat "$WS/broken.out" >&2
    fail "ws up succeeded on a tree that does not build"
  fi
  grep -q 'blocked: api.check not ready' "$WS/broken.out" && pass "the failed requirement blocks api.dev, with the reason" || { cat "$WS/broken.out" >&2; fail "no blocked message"; }
  [[ $("$ADE" ws status --json | jq length) == 0 ]] && pass "a failed bring-up leaves nothing running" || fail "instances left after a failed bring-up"
  "$ADE" inbox --json >"$WS/inbox2.json"
  expect_json "failures are errors (persistent pop-ups), dependents warn" "$WS/inbox2.json" \
    '([.[] | select(.kind == "task.failed" and .title == "api.check failed to start" and .severity == "error" and (.subject | contains("run ade ws up")))] | length == 1) and
     ([.[] | select(.kind == "task.blocked" and .title == "api.db waits for api.check" and .severity == "warn")] | length == 1) and
     ([.[] | select(.kind == "task.blocked" and .title == "api.dev waits for api.db" and .severity == "warn")] | length == 1)'
  expect_json "the failure detail names the log" "$WS/inbox2.json" \
    '[.[] | select(.kind == "task.failed")] | .[0].detail | contains("log: ")'

  # tracker#1's MR api!1 owns a branch: a workspace on its worktree gets a run record.
  "$ADE" items --json >"$WS/items.json"
  MR_BRANCH=$(jq -r '.[] | select(.ref == "ade-demo-env/tracker#1") | .mrs[0].source_branch' "$WS/items.json")
  [[ -n $MR_BRANCH && $MR_BRANCH != null ]] || fail "tracker#1 has no MR branch in the cache"
  git -C "$API/.bare" worktree add -q --track -b "$MR_BRANCH" "$API/$MR_BRANCH" "origin/$MR_BRANCH"
  # The MR's branch predates the sandbox app: bring the server into the tree (local, never pushed).
  git -C "$API/$MR_BRANCH" checkout -q origin/main -- main.go go.mod
  RECORDS="$XDG_DATA_HOME/ade/items/sandbox-gitlab/ade-demo-env/tracker/1/runs"
  "$ADE" ws up --no-zellij "$API/$MR_BRANCH" --run api.dev >"$WS/rec.out" 2>&1 &
  FG_PID=$!
  wait_for 180 grep -q 'Ctrl-C stops' "$WS/rec.out" || {
    cat "$WS/rec.out" >&2
    fail "ws up on the MR branch never became ready"
  }
  RUN_MD=$(find "$RECORDS" -name run.md 2>/dev/null | head -1)
  [[ -n $RUN_MD ]] && pass "a run record exists in tracker#1's item directory (runs/<id>/run.md)" || fail "no run record under $RECORDS"
  grep -q '^state: running' "$RUN_MD" && grep -q '^kind: workspace' "$RUN_MD" && grep -q '^item: ade-demo-env/tracker#1' "$RUN_MD" \
    && grep -q "^branch: $MR_BRANCH" "$RUN_MD" && pass "while running: state running, item and branch recorded" || { cat "$RUN_MD" >&2; fail "run record frontmatter"; }
  grep -Eq '^\| api\.dev \| ready \| [0-9]+ \| [0-9]+ \|' "$RUN_MD" && pass "the table names api.dev with its port and pid" || { cat "$RUN_MD" >&2; fail "run record lacks the api.dev row"; }
  kill -TERM "$FG_PID"
  wait "$FG_PID" || true
  FG_PID=
  grep -q '^state: stopped' "$RUN_MD" && grep -q '^ended: ' "$RUN_MD" && pass "after Ctrl-C the record says stopped, with an end time" || { cat "$RUN_MD" >&2; fail "run record not finalised"; }
  grep -qiE 'token|secret|PORT=|API_URL' "$RUN_MD" && fail "the run record holds env or credentials" || pass "the record is metadata only (no env, no commands)"
  # A baseline tree belongs to no work item: no record.
  before=$(find "$XDG_DATA_HOME/ade/items" -name run.md | wc -l)
  "$ADE" ws up --no-zellij --detach "$WEB/main" >/dev/null && "$ADE" ws down "$WEB/main" >/dev/null
  [[ $(find "$XDG_DATA_HOME/ade/items" -name run.md | wc -l) == "$before" ]] && pass "a baseline tree writes no run record" || fail "a baseline run wrote a record"

  step "killing api leaves web blocked"
  "$ADE" ws up --no-zellij "$WEB/main" >"$WS/blk.out" 2>&1 &
  FG_PID=$!
  wait_for 180 grep -q 'Ctrl-C stops' "$WS/blk.out" || {
    cat "$WS/blk.out" >&2
    fail "foreground ws up never became ready"
  }
  blocked_web() { "$ADE" ws status --json | jq -e '.[] | select(.node == "web.dev" and .state == "blocked" and .blocked_by == "api.dev" and .alive)' >/dev/null; }
  ! blocked_web && pass "with api running, web is not blocked" || fail "web blocked while api runs"
  BLK_API_PID=$("$ADE" ws status --json | jq -r '.[] | select(.node == "api.dev") | .pid')
  kill -TERM -- "-$BLK_API_PID" # api and its go-run child: its own session, so its process group
  wait_for 20 blocked_web && pass "ade ws status: web.dev is blocked by api.dev (its process still runs)" || {
    "$ADE" ws status >&2
    fail "web not reported blocked after api was killed"
  }
  "$ADE" ws status --json >"$WS/blocked.json"
  expect_json "api.dev is dead, not blocked; the db and the finished check are untouched" "$WS/blocked.json" \
    '([.[] | select(.node == "api.dev")][0] | .alive == false and .state != "blocked") and
     ([.[] | select(.node == "api.db")][0] | .alive and .state == "ready")'
  "$ADE" ws status | grep -q 'web.dev  *blocked by api.dev' && pass "text status says \"blocked by api.dev\"" || fail "text status lacks the blocked line"
  wait_for 20 grep -q 'web.dev blocked: api.dev is not running' "$WS/blk.out" && pass "the supervising workspace says so too" || fail "no blocked line in the workspace output"
  kill -TERM "$FG_PID"
  wait "$FG_PID" || true
  FG_PID=
  "$ADE" inbox --json >"$WS/inbox-blk.json"
  expect_json "inbox: one task.blocked warning for web.dev naming api.dev" "$WS/inbox-blk.json" \
    '[.[] | select(.kind == "task.blocked" and .severity == "warn" and (.title | startswith("web.dev ")) and (.title | contains("api.dev")))] | length == 1'
  [[ $("$ADE" ws status --json | jq length) == 0 ]] && pass "Ctrl-C still released everything" || fail "instances left after the blocked run"

  step "ade doctor --vscode"
  "$ADE" doctor --vscode --json >"$WS/doctor.json" 2>/dev/null || true
  expect_json "vscode checks: terminal, code launcher, host, key settings" "$WS/doctor.json" \
    '[.checks[] | select(.group == "vscode") | .name] | contains(["terminal","code launcher","host here","terminal key settings"])'
  expect_json "the settings snippet only releases keys (entries start with -)" "$WS/doctor.json" \
    '.vscode_settings["terminal.integrated.commandsToSkipShell"] | length > 0 and all(startswith("-"))'
  "$ADE" doctor --vscode 2>/dev/null | grep -q '"-workbench.action.quickOpen"' && pass "text output prints the commandsToSkipShell snippet" || fail "no snippet in text output"
  (cd "$WEB/main" && "$ADE" vscode tasks >/dev/null && "$ADE" doctor --vscode --json 2>/dev/null | jq -e '.checks[] | select(.name == "tasks.json") | .status == "ok"' >/dev/null) \
    && pass "in a worktree, doctor sees the generated tasks.json" || fail "doctor did not see tasks.json"
  rm -f "$WEB/main/.vscode/tasks.json"

  step "VS Code: tasks.json from the run graph"
  "$ADE" vscode tasks "$WEB/main" --json >"$WS/vscode.json"
  TASKS="$WEB/main/.vscode/tasks.json"
  [[ -f $TASKS ]] && pass ".vscode/tasks.json written" || fail "tasks.json missing"
  jq -e . "$TASKS" >/dev/null && pass "tasks.json is valid JSON" || fail "tasks.json is not JSON"
  expect_json "one task per graph node plus the compound task" "$TASKS" \
    '[.tasks[].label] | sort == ["ade: api.check","ade: api.db","ade: api.dev","ade: up","ade: web.dev"]'
  expect_json "dependsOn mirrors requires (web -> api.dev -> api.db -> api.check)" "$TASKS" \
    '(.tasks[] | select(.label == "ade: web.dev") | .dependsOn) == ["ade: api.dev"] and
     (.tasks[] | select(.label == "ade: api.dev") | .dependsOn) == ["ade: api.db"] and
     (.tasks[] | select(.label == "ade: api.db") | .dependsOn) == ["ade: api.check"] and
     (.tasks[] | select(.label == "ade: api.check") | .dependsOn // []) == [] and
     (.tasks[] | select(.label == "ade: up") | .dependsOn) == ["ade: web.dev"]'
  expect_json "services are background tasks whose matcher ends on readiness" "$TASKS" \
    '(.tasks[] | select(.label == "ade: web.dev") | .isBackground and .problemMatcher.background.endsPattern == "web ready on :\\d+") and
     (.tasks[] | select(.label == "ade: api.dev") | .isBackground and .problemMatcher.background.endsPattern == "^ade: ready api\\.dev") and
     (.tasks[] | select(.label == "ade: api.db") | .isBackground and .problemMatcher.background.endsPattern == "^ade: ready api\\.db") and
     (.tasks[] | select(.label == "ade: api.check") | (.isBackground // false) == false)'
  # Keep the user's own tasks and settings on regeneration.
  jq '.tasks += [{"label":"lint","type":"shell","command":"true"}] | .inputs = [{"id":"x","type":"promptString","description":"x"}]' "$TASKS" >"$WS/tasks.edited" && mv "$WS/tasks.edited" "$TASKS"
  "$ADE" vscode tasks "$WEB/main" >/dev/null
  expect_json "regenerating keeps user tasks and inputs, no duplicates of ours" "$TASKS" \
    '([.tasks[].label] | sort == ["ade: api.check","ade: api.db","ade: api.dev","ade: up","ade: web.dev","lint"]) and (.inputs | length == 1)'

  step "VS Code substitute: run the generated tasks as VS Code would (dependsOn order, background readiness)"
  # run_task LABEL: execute the task's command; a background task counts as started once its
  # output matches background.endsPattern, others once they exit 0. dependsOn is honoured first.
  declare -A STARTED=()
  TASK_PIDS=()
  run_task() {
    local label=$1 dep
    [[ -n ${STARTED[$label]:-} ]] && return 0
    while read -r dep; do [[ -n $dep ]] && run_task "$dep"; done < <(jq -r --arg l "$label" '.tasks[] | select(.label == $l) | .dependsOn[]?' "$TASKS")
    local cmd args=() out="$WS/task-${label//[^a-zA-Z0-9]/_}.out" ends bg
    cmd=$(jq -r --arg l "$label" '.tasks[] | select(.label == $l) | .command // empty' "$TASKS")
    [[ -z $cmd ]] && { STARTED[$label]=1; return 0; } # the compound task has no command of its own
    mapfile -t args < <(jq -r --arg l "$label" '.tasks[] | select(.label == $l) | .args[]' "$TASKS")
    bg=$(jq -r --arg l "$label" '.tasks[] | select(.label == $l) | .isBackground // false' "$TASKS")
    if [[ $bg == true ]]; then
      ends=$(jq -r --arg l "$label" '.tasks[] | select(.label == $l) | .problemMatcher.background.endsPattern' "$TASKS")
      "$cmd" "${args[@]}" >"$out" 2>&1 &
      TASK_PIDS+=("$!")
      wait_for 180 grep -qP "$ends" "$out" || {
        cat "$out" >&2
        fail "task '$label' never matched its endsPattern: $ends"
      }
      pass "task '$label' reached its background endsPattern"
    else
      "$cmd" "${args[@]}" >"$out" 2>&1 || {
        cat "$out" >&2
        fail "task '$label' failed"
      }
      pass "task '$label' finished with exit 0"
    fi
    STARTED[$label]=1
  }
  run_task "ade: up"
  WEB_VS=$(jq -r --arg d "$WEB/main" '.[] | select(.tree == $d and .node == "web.dev") | .port' <("$ADE" ws status --json))
  [[ $(health "$WEB_VS" | jq -r .api.reachable) == true ]] && pass "web (started by its VS Code task) reaches the api started by api.dev's task" || fail "web cannot reach api"
  [[ $("$ADE" ws status --json | jq '[.[] | select(.node == "api.dev")] | length') == 1 ]] && pass "api.dev runs once although both tasks asked for it" || fail "api.dev started twice"
  for pid in "${TASK_PIDS[@]}"; do kill -TERM "$pid" 2>/dev/null || true; done
  for pid in "${TASK_PIDS[@]}"; do wait "$pid" 2>/dev/null || true; done
  wait_for 30 bash -c "[[ \$('$ADE' ws status --json | jq length) == 0 ]]" && pass "terminating the tasks stopped every instance" || fail "instances left after the tasks ended"

  step "zellij: a tab from the generated layout in a throwaway session ($SESSION)"
  Z() { env -u ZELLIJ -u ZELLIJ_SESSION_NAME -u ZELLIJ_PANE_ID zellij "$@"; }
  Z delete-session --force "$SESSION" >/dev/null 2>&1 || true
  "$ADE" ws layout "$WEB/main" >"$WS/layout.kdl"
  grep -q 'tab name="web/main"' "$WS/layout.kdl" && grep -q '"task" "run" "api.db"' "$WS/layout.kdl" && grep -q '"task" "run" "api.dev"' "$WS/layout.kdl" && grep -q '"task" "run" "web.dev"' "$WS/layout.kdl" \
    && pass "layout has the tab and a pane per run-graph node" || { cat "$WS/layout.kdl" >&2; fail "layout lacks tab or node panes"; }
  Z attach --create-background "$SESSION" >/dev/null 2>&1
  # A client must be attached for panes to be created; `script` provides a pty.
  command -v script >/dev/null || fail "script (util-linux) is needed to attach a pty client"
  setsid -f sh -c "sleep 300 | env -u ZELLIJ -u ZELLIJ_SESSION_NAME script -qfc 'stty rows 50 cols 160; zellij attach $SESSION' /dev/null >/dev/null 2>&1"
  wait_for 20 bash -c "env -u ZELLIJ -u ZELLIJ_SESSION_NAME -u ZELLIJ_PANE_ID zellij --session $SESSION action list-panes >/dev/null" || fail "zellij session did not start"
  sleep 2
  Z --session "$SESSION" action new-tab --name scratch-1
  Z --session "$SESSION" action new-tab --name scratch-2
  mapfile -t TABS_BEFORE < <(Z --session "$SESSION" action query-tab-names)
  "$ADE" ws up "$WEB/main" --session "$SESSION" --json >"$WS/open1.json"
  expect_json "ws up opens a new tab web/main in $SESSION" "$WS/open1.json" '.host == "zellij" and .created == true and .tab == "web/main" and .session == "ade-test-ws"'
  Z --session "$SESSION" action query-tab-names | grep -qx 'web/main' && pass "zellij lists the tab" || fail "tab missing from zellij"
  wait_for 20 bash -c "env -u ZELLIJ -u ZELLIJ_SESSION_NAME -u ZELLIJ_PANE_ID zellij --session $SESSION action list-panes --json | jq -e '[.[] | select(.tab_name == \"web/main\" and .is_plugin == false)] | length >= 3' >/dev/null" \
    && pass "panes were created in the tab" || fail "tab has no panes"
  Z --session "$SESSION" action list-panes --json >"$WS/panes.json"
  expect_json "panes run 'ade task run' for api.db, api.dev and web.dev in the worktree" "$WS/panes.json" \
    '[.[] | select(.tab_name == "web/main") | .terminal_command // ""] | (any(contains("task run api.db")) and any(contains("task run api.dev")) and any(contains("task run web.dev")))'
  expect_json "every running pane's cwd is the worktree (service panes wait for Enter, so they have no process yet)" "$WS/panes.json" \
    "[.[] | select(.tab_name == \"web/main\" and .is_plugin == false and .exited == false and .pane_cwd != null) | .pane_cwd] | (length >= 1) and all(. == \"$WEB/main\")"
  expect_json "the tab's panes sit below the tab bar (the new tab has the standard chrome)" "$WS/panes.json" \
    '[.[] | select(.tab_name == "web/main" and .is_plugin == false) | .pane_y] | min >= 1'
  mapfile -t TABS_NOW < <(Z --session "$SESSION" action query-tab-names)
  for t in "${TABS_BEFORE[@]}"; do printf '%s\n' "${TABS_NOW[@]}" | grep -qxF "$t" && pass "tab '$t' survived the open" || fail "tab '$t' disappeared"; done
  sleep 3
  [[ $("$ADE" ws status --json | jq length) == 0 ]] && pass "nothing started when the tab opened: the service panes wait for Enter" || fail "an instance started on open"
  pane_of() { Z --session "$SESSION" action list-panes --json | jq -r --arg n "$1" '[.[] | select(.tab_name == "web/main" and .title == $n and .is_plugin == false) | "terminal_\(.id)"] | first // empty'; }
  Z --session "$SESSION" action dump-screen --pane-id "$(pane_of web.dev)" | grep -q 'Waiting to run' && pass "the web.dev pane shows its command and waits" || fail "web.dev pane is not waiting"
  Z --session "$SESSION" action send-keys --pane-id "$(pane_of web.dev)" Enter
  wait_for 240 bash -c "[[ \$('$ADE' ws status --json | jq '[.[] | select(.node == \"web.dev\" and .alive)] | length') == 1 ]]" \
    || fail "Enter in the web.dev pane did not bring up web.dev"
  WEB_Z=$(jq -r --arg d "$WEB/main" '.[] | select(.tree == $d and .node == "web.dev") | .port' <("$ADE" ws status --json))
  wait_for 30 bash -c "curl -fsS --max-time 5 http://127.0.0.1:$WEB_Z/healthz | jq -e .api.reachable >/dev/null" \
    && pass "web started from its pane brought up its requirement api and reaches it" || fail "web in the tab cannot reach api"
  [[ $("$ADE" ws status --json | jq '[.[] | select(.node == "api.dev" and .alive)] | length') == 1 ]] && pass "one api instance, started by web's pane" || fail "api not started once"
  Z --session "$SESSION" action send-keys --pane-id "$(pane_of api.dev)" Enter
  wait_for 60 bash -c "env -u ZELLIJ -u ZELLIJ_SESSION_NAME -u ZELLIJ_PANE_ID zellij --session $SESSION action dump-screen --pane-id $(pane_of api.dev) | grep -q 'ade: ready api.dev'" \
    && pass "Enter in the api.dev pane then shares the running instance" || fail "api.dev pane did not start"
  [[ $("$ADE" ws status --json | jq '[.[] | select(.node == "api.dev")] | length') == 1 ]] && pass "still one api instance behind both panes" || fail "api started twice"
  "$ADE" ws up "$WEB/main" --session "$SESSION" --json >"$WS/open2.json"
  expect_json "opening again focuses the existing tab (idempotent)" "$WS/open2.json" '.created == false'
  [[ $(Z --session "$SESSION" action query-tab-names | grep -cx 'web/main') == 1 ]] && pass "still exactly one web/main tab" || fail "duplicate tab"
  # `s` on an item: the same start-work run, now with the workspace step, in the throwaway session.
  "$ADE" start ade-demo-env/tracker#3 --repo api --session "$SESSION" --json >"$WS/start-tab.json"
  expect_json "ade start (with a workspace): linked child, draft MR and worktree reported, workspace step ran" "$WS/start-tab.json" \
    ".targets[0] | .child != null and .mr != null and .worktree == \"$WT3\""
  expect_json "the workspace step opened a tab (not skipped)" "$WS/start-tab.json" \
    '[.steps[] | select(.kind == "workspace")] | length == 1 and all(.[]; .outcome != "skipped" and .outcome != "failed")'
  Z --session "$SESSION" action query-tab-names | grep -qx "api/$BR3" && pass "zellij lists the item's tab api/$BR3 (the branch is named for the item)" || {
    Z --session "$SESSION" action query-tab-names >&2
    fail "no tab for the started item"
  }
  # Never act on a session that is not running.
  if "$ADE" ws up "$WEB/main" --session ade-test-nonexistent >/dev/null 2>&1; then fail "opened a tab in a session that does not exist"; else pass "a missing session is an error, nothing is created"; fi
  Z list-sessions --no-formatting | grep -q '^ade-test-nonexistent' && fail "created a stray session" || pass "no stray session created"
  pkill -f "zellij attach $SESSION" || true
  Z delete-session --force "$SESSION" >/dev/null 2>&1
  Z list-sessions --no-formatting 2>/dev/null | grep -q "^$SESSION " && fail "throwaway session not deleted" || pass "throwaway session deleted"
  # The panes' processes die with the session; release anything left held by them.
  "$ADE" ws down "$WEB/main" >/dev/null 2>&1 || true
  wait_for 30 bash -c "[[ \$('$ADE' ws status --json | jq length) == 0 ]]" && pass "instances released after the session ended" || fail "instances left after the session ended"

  step "ade-nvim: headless smoke test"
  check "ade sync (cache for the picker)" "$ADE" sync
  command -v nvim >/dev/null || fail "nvim not on PATH"
  ADE_BIN="$ADE" ADE_SMOKE_WORKTREE="$WEB/main" check "nvim --headless: :AdeItems, :AdeOpen, :AdeReview" \
    nvim --headless -u NONE -i NONE --cmd "set rtp^=$REPO/editors/ade-nvim" -l "$REPO/editors/ade-nvim/test/smoke.lua"
  # nvim prints PASS/FAIL lines itself; surface them in the log.
  sed 's/^/    /' "$VERIFY_TMP/out" || true
)

printf '\nPhase 3 (workspaces): all checks passed\n'
