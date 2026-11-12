#!/usr/bin/env bash
# Dev environment (docs/PLAN.md §6.3, §6.6, §6.7, §2.2): the tab ADE opens for a ticket's
# worktree, end to end, offline.
#
#   start work -> a tab with  [omp briefed by the ADE implement skill | overview | lazygit + shell]
#                 and a strip with one pane per service, all waiting for Enter (nothing starts on open);
#                 the session's other tabs stay and the new tab has the tab bar and status bar;
#                 the multi-root VS Code workspace;
#                 the commit prefix on agent commits, merge messages and the worktree's git config.
#
# The demo setup: the fake GitLab (Premium tier) serving real file:// git remotes with the sandbox
# apps (scripts/demo.sh), a stub omp (scripts/verify/fakeomp-session, no spend), a throwaway zellij
# session ade-test-devenv with a pty client attached through `script` (zellij creates panes only for
# an attached client, as phase3-workspaces.sh does). Screen dumps go to .impeccable/review/devenv-*.txt.
#
# Run from the repo root inside `nix develop`; exits 0 when every check passes. Needs zellij, git,
# script (util-linux), jq, curl and go.
[[ -n ${VERIFY_TMP:-} ]] || . scripts/verify/lib.sh

SESSION=ade-test-devenv
OUT="$REPO/.impeccable/review"
mkdir -p "$OUT"
rm -f "$OUT"/devenv-*.txt
PIDS=()

Z() { env -u ZELLIJ -u ZELLIJ_SESSION_NAME -u ZELLIJ_PANE_ID zellij "$@"; }
ZS() { Z --session "$SESSION" "$@"; }

cleanup() {
  set +e
  pkill -f "zellij attach $SESSION" 2>/dev/null
  Z delete-session --force "$SESSION" >/dev/null 2>&1
  if [[ -n ${ADE:-} && -x ${ADE:-} ]]; then
    for tree in "$PROJ"/web/*/ "$PROJ"/api/*/; do
      [[ -d $tree ]] && "$ADE" ws down "$tree" >/dev/null 2>&1
    done
  fi
  kill "${PIDS[@]}" 2>/dev/null
  return 0
}
trap 'cleanup; rm -rf "$VERIFY_TMP"' EXIT

# wait_for SECONDS CMD...: retry CMD once a second.
wait_for() {
  local s=$1 i
  shift
  for ((i = 0; i < s; i++)); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done
  return 1
}
# pane_id TAB TITLE: the zellij id of a terminal pane (terminal_N), by tab and pane name.
pane_id() {
  ZS action list-panes --json | jq -r --arg t "$1" --arg n "$2" \
    '[.[] | select(.tab_name == $t and .title == $n and .is_plugin == false) | "terminal_\(.id)"] | first // empty'
}
# dump TAB TITLE FILE: write the pane's screen to FILE.
dump() {
  local id
  id=$(pane_id "$1" "$2")
  [[ -n $id ]] || return 1
  ZS action dump-screen --pane-id "$id" >"$3"
}
# press_enter TAB TITLE: send the Enter key to the pane, which starts a waiting service pane. (Raw
# `zellij action write 13` is not seen by a suspended command pane; a key event is, like a keypress.)
press_enter() {
  local id
  id=$(pane_id "$1" "$2") || return 1
  ZS action send-keys --pane-id "$id" Enter
}
# screen_has TAB TITLE REGEXP: the pane's screen matches.
screen_has() {
  local id
  id=$(pane_id "$1" "$2")
  [[ -n $id ]] && ZS action dump-screen --pane-id "$id" | grep -Eq "$3"
}

step "unit tests of every piece"
check "layout KDL (overview pane, omp pane, services strip), skill rendering, layout precedence, code-workspace JSON, run graph" \
  go test -count=1 ./internal/workspace/... ./internal/itemdoc/... ./internal/commitmsg/... ./internal/commitprefix/...
check "issue overview view and refresh" go test -count=1 ./internal/overview/...
check "start work passes the item and layout; commit prefix in start work, agent commits, merges; config validation + schema" \
  go test -count=1 ./internal/startwork/... ./internal/agent/... ./internal/config/... ./internal/review/... ./internal/forge/...
check "CLI: ade item --watch, ade vscode workspace, ade mr merge prefix" go test -count=1 ./internal/cli/...
check "TUI: main pane toggle, W, v, merge dialog prefix" go test -count=1 ./internal/tui/... -run 'Start|Workspace|CodeWorkspace|Merge'

step "the demo setup: fake GitLab with real git remotes, stub omp"
build_ade
go build -o "$VERIFY_TMP/fakegl" ./scripts/verify/fakegl
T=$VERIFY_TMP
mkdir -p "$T/remotes" "$T/bin" "$T/omp"
cp scripts/verify/fakeomp-session "$T/bin/omp"
"$T/fakegl" -tier premium -agile -carried -mr-template -review ade-demo-env/api!2 \
  -git-root "$T/remotes" -git-apps "$REPO/scripts/sandbox/apps" >"$T/gl.url" 2>"$T/gl.err" &
PIDS+=($!)
for _ in $(seq 50); do [[ -s $T/gl.url ]] && break; sleep 0.1; done
URL=$(head -1 "$T/gl.url")
[[ -n $URL ]] || fail "the fake GitLab did not start: $(cat "$T/gl.err")"
# The manifest of the sandbox with the commit prefix rule, and the remotes the fake serves.
sed -e 's/^weight .*/weight     = { source = "gitlab" }/' \
  -e 's/^iterations = .*/iterations = { source = "milestones", overlap = 10 }/' \
  -e 's|^mr     = .*|&\ncommit_prefix = "GL-{parent}"|' \
  scripts/sandbox/manifests/gitlab.ade.toml |
  awk -v api="file://$T/remotes/api.git" -v web="file://$T/remotes/web.git" '
    {print}
    /^path +=.*\/api"/ {print "url  = \"" api "\""}
    /^path +=.*\/web"/ {print "url  = \"" web "\""}' >"$T/manifest.ade.toml"
grep -q '^commit_prefix = "GL-{parent}"' "$T/manifest.ade.toml" || fail "manifest lacks the commit prefix"
export XDG_DATA_HOME="$T/data" XDG_CACHE_HOME="$T/cache" XDG_CONFIG_HOME="$T/xdgcfg"
export ADE_CONFIG="$T/config.toml" FAKE_GITLAB_TOKEN=fake-token FAKEOMP_RECORD="$T/omp"
unset ZELLIJ ZELLIJ_SESSION_NAME ZELLIJ_PANE_ID TERM_PROGRAM
export EDITOR=cat # a long-lived stand-in for the editor pane
export PATH="$T/bin:$PATH"
export GOCACHE=${GOCACHE:-$(go env GOCACHE)} GOMODCACHE=${GOMODCACHE:-$(go env GOMODCACHE)}
cat >"$ADE_CONFIG" <<EOF
[user]
handle = "sandbox"
timezone = "UTC"
[host]
mode = "zellij"
editor = "\$EDITOR"
[forges.sandbox-gitlab]
kind = "gitlab"
url = "$URL"
token_env = "FAKE_GITLAB_TOKEN"
git_protocol = "https"
[update]
on_start = false
[paths]
root = "$T/projects"
[[projects]]
source = "$T/manifest.ade.toml"
[ai]
mode = "auto"
profile = "stub"
[ai.profiles.stub]
driver = "omp"
agent = "omp"
command = "$T/bin/omp"
models = { explain = "claude-haiku-4-5", draft = "claude-haiku-4-5", plan = "claude-haiku-4-5", implement = "claude-haiku-4-5", review = "claude-haiku-4-5" }
billing = { source = "omp", weekly_soft_usd = 10 }
[ai.profiles.stub.prices.claude-haiku-4-5]
input = 1.0
output = 5.0
[budget]
daily_usd = 5.0
agent_run_usd = 0.5
EOF
PROJ="$T/projects/demo-gl"
REF=ade-demo-env/tracker#3
check "ade project add clones api and web from the fake's git remotes" "$ADE" project add "$T/manifest.ade.toml"
check "ade sync" "$ADE" sync

step "a throwaway zellij session with a pty client ($SESSION)"
command -v script >/dev/null || fail "script (util-linux) is needed to attach a pty client"
Z delete-session --force "$SESSION" >/dev/null 2>&1 || true
Z attach --create-background "$SESSION" >/dev/null 2>&1
setsid -f sh -c "sleep 900 | env -u ZELLIJ -u ZELLIJ_SESSION_NAME script -qfc 'stty rows 52 cols 200; zellij attach $SESSION' '$T/client.log' >/dev/null 2>&1"
wait_for 20 ZS action list-panes || fail "the zellij session did not start"
sleep 2
pass "session $SESSION is running with an attached client"
# Tabs that exist before any workspace opens: they must all survive it.
ZS action new-tab --name scratch-1
ZS action new-tab --name scratch-2
ZS action go-to-tab 1
sleep 1
mapfile -t PRE < <(ZS action query-tab-names)
[[ ${#PRE[@]} -ge 3 ]] && pass "the session has ${#PRE[@]} tabs before: ${PRE[*]}" || fail "tabs before: ${PRE[*]}"
CLIENT_OFF=$(stat -c %s "$T/client.log")

step "start work (child issues, branches, draft MRs, worktrees) opens the tab with the omp main pane"
"$ADE" start "$REF" --repo api --repo web --layout agent --session "$SESSION" --json >"$T/start.json" || {
  cat "$T/start.json" >&2
  fail "ade start"
}
API_WT=$(jq -r '.targets[] | select(.repo == "api") | .worktree' "$T/start.json")
WEB_WT=$(jq -r '.targets[] | select(.repo == "web") | .worktree' "$T/start.json")
BRANCH=$(jq -r '.targets[] | select(.repo == "api") | .branch' "$T/start.json")
API_MR=$(jq -r '.targets[] | select(.repo == "api") | .mr' "$T/start.json")
API_MR_URL=$(jq -r '.targets[] | select(.repo == "api") | .mr_url' "$T/start.json")
TAB="api/$BRANCH"
expect_json "start work created the child, branch, draft MR and worktrees in both repos" "$T/start.json" \
  '(.targets | length) == 2 and all(.targets[]; .child != null and .mr != null and .worktree != null)'
expect_json "the workspace step opened a tab" "$T/start.json" '[.steps[] | select(.kind == "workspace")] | length == 1 and all(.[]; .outcome == "created")'
[[ $(ZS action query-tab-names | grep -cx "$TAB") == 1 ]] && pass "zellij lists the tab $TAB" || fail "no tab $TAB"
wait_for 30 bash -c "$(declare -f pane_id ZS Z); SESSION=$SESSION; [[ -n \$(pane_id '$TAB' agent) && -n \$(pane_id '$TAB' overview) && -n \$(pane_id '$TAB' api.dev) && -n \$(pane_id '$TAB' web.dev) ]]" \
  || { ZS action list-panes --json | jq -c '.[] | {tab_name, title}' >&2; fail "the tab's panes were not created"; }
# Panes are created first and sized a moment later: wait for the layout to settle.
settled() {
  ZS action list-panes --json >"$T/panes.json"
  jq -e --arg t "$TAB" '.[] | select(.tab_name == $t and .title == "overview") | .pane_columns >= 60 and .pane_rows >= 12' "$T/panes.json"
}
wait_for 20 settled || true
ZS action list-panes --json >"$T/panes.json"
expect_json "the tab has the main (omp), overview, lazygit and shell panes and one pane per service" "$T/panes.json" \
  "[.[] | select(.tab_name == \"$TAB\" and .is_plugin == false) | .title] | (index(\"agent\") != null and index(\"overview\") != null and index(\"lazygit\") != null and index(\"shell\") != null and index(\"api.db\") != null and index(\"api.dev\") != null and index(\"web.dev\") != null)"
jq -r --arg t "$TAB" '.[] | select(.tab_name == $t and .is_plugin == false) | "\(.title)\t\(.pane_columns)x\(.pane_rows) at \(.pane_x),\(.pane_y)"' "$T/panes.json" >"$OUT/devenv-panes.txt"
expect_json "the overview pane has room to read (at least 60 columns x 12 rows in a 200x52 terminal)" "$T/panes.json" \
  ".[] | select(.tab_name == \"$TAB\" and .title == \"overview\") | .pane_columns >= 60 and .pane_rows >= 12"
expect_json "every running pane starts in the worktree (waiting service panes have no process yet)" "$T/panes.json" \
  "[.[] | select(.tab_name == \"$TAB\" and .is_plugin == false and .exited == false and .pane_cwd != null) | .pane_cwd] | length >= 4 and all(. == \"$API_WT\")"
expect_json "the overview pane runs ade item <tracker ref> --watch" "$T/panes.json" \
  ".[] | select(.tab_name == \"$TAB\" and .title == \"overview\") | .terminal_command | endswith(\"item $REF --watch\")"
expect_json "lazygit runs in its pane" "$T/panes.json" \
  ".[] | select(.tab_name == \"$TAB\" and .title == \"lazygit\") | .terminal_command | endswith(\"lazygit\")"
expect_json "the services run 'ade task run' for api.db, api.dev and web.dev" "$T/panes.json" \
  "[.[] | select(.tab_name == \"$TAB\") | .terminal_command // \"\"] | (any(contains(\"task run api.db\")) and any(contains(\"task run api.dev\")) and any(contains(\"task run web.dev\")))"
ZS action dump-layout >"$OUT/devenv-layout.txt"
grep -q 'name="overview"\|name "overview"\|overview' "$OUT/devenv-layout.txt" && pass "zellij dump-layout saved to .impeccable/review/devenv-layout.txt" || fail "dump-layout lacks the overview pane"
"$ADE" ws status --json >"$T/status-open.json"
expect_json "no service started when the tab opened (ws status is empty)" "$T/status-open.json" 'length == 0'
expect_json "every service pane waits for Enter (start_suspended)" "$T/panes.json" \
  "[.[] | select(.tab_name == \"$TAB\" and (.title | test(\"^(services|api\\\\.|web\\\\.)\"))) | .exited] | length >= 4 and all(. == false)"
grep -c 'start_suspended' "$OUT/devenv-layout.txt" | grep -Eqx '[4-9]|[1-9][0-9]' && pass "the live layout marks the services and task panes start_suspended" || fail "dump-layout lacks start_suspended panes"

step "existing tabs stay and the new tab has the normal chrome"
POST=$(ZS action query-tab-names)
for t in "${PRE[@]}"; do grep -qxF "$t" <<<"$POST" && pass "tab '$t' is still there" || fail "tab '$t' disappeared: $POST"; done
[[ $(wc -l <<<"$POST") == $((${#PRE[@]} + 1)) ]] && pass "the new tab was added to the session's tabs, nothing replaced" || fail "tabs: $POST"
expect_json "the tab's panes sit below the tab bar (first row is not a pane)" "$T/panes.json" \
  "[.[] | select(.tab_name == \"$TAB\" and .is_plugin == false) | .pane_y] | min >= 1"
ZS action dump-layout | grep -c 'zellij:tab-bar' | grep -qx '[1-9][0-9]*' && pass "the layout has the tab-bar plugin" || fail "no tab bar in the session"
ZS action dump-layout | grep -c 'zellij:status-bar' | grep -qx '[1-9][0-9]*' && pass "and the status-bar plugin" || fail "no status bar in the session"
ZS action go-to-tab-name "${PRE[0]}"
ZS action go-to-tab-name "$TAB"
sleep 1
tail -c +"$((CLIENT_OFF + 1))" "$T/client.log" >"$T/client-since-open.log"
for t in scratch-1 scratch-2; do grep -aqF "$t" "$T/client-since-open.log" && pass "the client drew tab '$t' in the tab bar" || fail "tab bar never showed '$t'"; done

step "main pane: omp started with the ADE implement skill (stub omp records its args, env and the skill)"
wait_for 20 test -s "$T/omp/SKILL.md" || { cat "$T/omp/args" >&2; fail "the stub omp never saw the skill"; }
mapfile -t OMPARGS <"$T/omp/args"
[[ ${OMPARGS[0]} == --config && ${OMPARGS[1]} == "$XDG_CACHE_HOME"/ade/ws/agent/*/omp.yml ]] \
  && pass "omp --config <cache>/ws/agent/<tree>/omp.yml (an overlay for this run; ~/.omp is not touched)" || fail "omp args: ${OMPARGS[*]}"
[[ ${OMPARGS[2]} == *"skill://ade-implement"* && ${OMPARGS[2]} == *"$REF"* && ${OMPARGS[2]} == *"do not push"* ]] \
  && pass "the opening message points at skill://ade-implement for $REF and says not to push" || fail "prompt: ${OMPARGS[2]}"
[[ $(cat "$T/omp/cwd") == "$API_WT" ]] && pass "omp runs in the item's api worktree" || fail "omp cwd $(cat "$T/omp/cwd")"
grep -q '^skills:' "${OMPARGS[1]}" && grep -q 'customDirectories' "${OMPARGS[1]}" && pass "the overlay only adds the skill directory" || fail "overlay: $(cat "${OMPARGS[1]}")"
SKILL="$T/omp/SKILL.md"
for want in "name: ade-implement" "# Implement $REF: Retry failed webhook deliveries" "$API_MR" "$API_MR_URL" "$BRANCH" \
  "Child issue in this repo" "Only the first commit of the merge request starts with \`GL-3\`" "**Stop for review.**" "Do not push" "$WEB_WT"; do
  grep -qF -- "$want" "$SKILL" || {
    cat "$SKILL" >&2
    fail "the skill lacks: $want"
  }
done
pass "the skill carries the ticket (parent + child), the MR link and branch, the other repo's tree, the commit prefix rule and 'stop for review, no push'"
cp "$SKILL" "$OUT/devenv-skill.txt"
dump "$TAB" agent "$OUT/devenv-agent.txt" && grep -q 'fakeomp: skill ade-implement' "$OUT/devenv-agent.txt" && pass "the omp pane shows the session that loaded the skill" || fail "agent pane dump"
[[ ! -e $HOME/.omp/agent/config.yml ]] || ! grep -qF "$T" "$HOME/.omp/agent/config.yml" && pass "the user's omp config does not mention the overlay" || fail "~/.omp/agent/config.yml was edited"
[[ ! -e $API_WT/.omp && ! -e $API_WT/.vscode ]] && pass "nothing was written into the worktree" || fail "ADE wrote files into the worktree"
[[ -z $(git -C "$API_WT" status --porcelain) ]] && pass "the worktree is clean (skill and overlay live in the cache)" || fail "worktree dirty: $(git -C "$API_WT" status --porcelain)"

step "regenerated on open: a new note shows up in the skill"
REC=$("$ADE" item "$REF" --json | jq -r .path)
"$ADE" sync >/dev/null # the cache learns the new child and MR (the dashboard does this after start work)
printf '\nUse the exponential backoff helper, not a new loop (note for the proof).\n' >>"$REC"
"$ADE" ws up "$API_WT" --layout agent --session "$SESSION" --json >"$T/open2.json"
expect_json "opening again focuses the existing tab" "$T/open2.json" '.created == false'
grep -q "exponential backoff helper" "$(dirname "${OMPARGS[1]}")/skills/ade-implement/SKILL.md" && pass "the skill holds the developer's notes after the next open" || fail "notes missing from the regenerated skill"

step "overview pane: the item live from the cache"
wait_for 20 screen_has "$TAB" overview 'Retry failed webhook deliveries' || { dump "$TAB" overview /dev/stderr; fail "overview lacks the title"; }
pass "the overview shows the ticket title"
"$ADE" sync >/dev/null
wait_for 30 screen_has "$TAB" overview 'draft' || { dump "$TAB" overview /dev/stderr; fail "the overview did not follow the sync"; }
dump "$TAB" overview "$OUT/devenv-overview.txt"
grep -q "$REF" "$OUT/devenv-overview.txt" && grep -q 'Retry failed webhook deliveries' "$OUT/devenv-overview.txt" && grep -Eq 'draft' "$OUT/devenv-overview.txt" \
  && pass "after a sync the overview shows the stage (draft) and the MR without being restarted" || fail "overview content"
grep -Eq "$(basename "$API_MR" | sed 's/.*!//')|!" "$OUT/devenv-overview.txt" && pass "it lists the MR" || fail "no MR in the overview"
ZS action send-keys --pane-id "$(pane_id "$TAB" overview)" "G" # scroll to the end: description, acceptance, notes
wait_for 10 screen_has "$TAB" overview 'Acceptance' || { dump "$TAB" overview /dev/stderr; fail "no acceptance in the overview"; }
dump "$TAB" overview "$OUT/devenv-overview-description.txt"
grep -Eq 'Description' "$OUT/devenv-overview-description.txt" && grep -q 'Exponential backoff, max 6 attempts' "$OUT/devenv-overview-description.txt" \
  && pass "scrolled down it shows the description and the acceptance criteria" || fail "no description in the overview"
grep -q 'exponential backoff helper' "$OUT/devenv-overview-description.txt" && pass "and your notes (live: written to the record a moment ago)" || fail "no notes in the overview"
ZS action send-keys --pane-id "$(pane_id "$TAB" overview)" "g"

step "services: nothing starts with the tab; Enter in a pane starts that service and what it requires"
dump "$TAB" api.dev "$OUT/devenv-services-idle.txt"
grep -qi 'waiting to run' "$OUT/devenv-services-idle.txt" && grep -q 'task run api.dev' "$OUT/devenv-services-idle.txt" \
  && pass "the api.dev pane shows its command and waits (start_suspended)" || { cat "$OUT/devenv-services-idle.txt" >&2; fail "api.dev pane is not waiting"; }
"$ADE" ws status --json >"$T/status-idle.json"
expect_json "still no instance (the previous steps opened and re-opened the tab)" "$T/status-idle.json" 'length == 0'
press_enter "$TAB" api.dev
wait_for 120 screen_has "$TAB" api.dev 'ade: ready api.dev' || { dump "$TAB" api.dev /dev/stderr; fail "api.dev never became ready after Enter"; }
"$ADE" ws status --json >"$T/status-a.json"
expect_json "Enter in api.dev started api.dev (with its requirements) from the item's api worktree, and not web.dev" "$T/status-a.json" \
  "([.[] | select(.alive) | .node] | index(\"web.dev\") == null) and (.[] | select(.node == \"api.dev\") | .tree == \"$API_WT\" and .alive)"
screen_has "$TAB" web.dev 'Waiting to run' && pass "web.dev is still waiting" || fail "web.dev started by itself"
ZS action send-keys --pane-id "$(pane_id "$TAB" api.dev)" "Ctrl c"
wait_for 20 bash -c "[[ \$('$ADE' ws status --json | jq length) == 0 ]]" && pass "Ctrl-C in api.dev stops it" || fail "api.dev still alive after Ctrl-C"
# web needs api: Enter in web.dev brings api up first, in this pane
press_enter "$TAB" web.dev
wait_for 240 screen_has "$TAB" web.dev 'ade: ready web.dev' || { dump "$TAB" web.dev /dev/stderr; fail "web.dev never became ready in its pane"; }
dump "$TAB" web.dev "$OUT/devenv-services-web.txt"
grep -Eq 'api.dev :[0-9]+ +ready' "$OUT/devenv-services-web.txt" && pass "Enter in web.dev started its requirement api.dev first (the pane shows both)" || fail "web.dev pane does not show api.dev"
"$ADE" ws status --json >"$T/status.json"
expect_json "api.dev is up although its own pane was not started" "$T/status.json" ".[] | select(.node == \"api.dev\") | .tree == \"$API_WT\" and .alive"
press_enter "$TAB" api.dev
wait_for 60 screen_has "$TAB" api.dev 'ade: ready api.dev' || { dump "$TAB" api.dev /dev/stderr; fail "api.dev pane did not start"; }
dump "$TAB" api.dev "$OUT/devenv-services-api.txt"
"$ADE" ws status --json >"$T/status.json"
expect_json "its own pane then shares that instance (api.dev held twice)" "$T/status.json" \
  "[.[] | select(.node == \"api.dev\" and .alive)] | length == 1 and (.[0].holders | length) >= 2"
grep -q 'Ctrl-C stops it' "$OUT/devenv-services-web.txt" && grep -q 'Enter starts it again' "$OUT/devenv-services-web.txt" \
  && pass "the pane says how to stop (Ctrl-C) and start again (Enter)" || fail "no hint in the web.dev pane"
API_PORT=$(jq -r '.[] | select(.node == "api.dev") | .port' "$T/status.json")
expect_json "web.dev runs from the item's own web worktree (the tab opened after both worktrees existed)" "$T/status.json" \
  ".[] | select(.node == \"web.dev\") | .tree == \"$WEB_WT\" and .alive"
WEB_PORT=$(jq -r '.[] | select(.node == "web.dev" and .alive) | .port' "$T/status.json" | head -1)
curl -fsS --max-time 5 "http://127.0.0.1:$WEB_PORT/healthz" >"$T/web.health"
expect_json "web reaches the item's api (API_URL is the item's api :$API_PORT, not the baseline's)" "$T/web.health" \
  ".api.reachable == true and .api.url == \"http://127.0.0.1:$API_PORT\""
ZS action send-keys --pane-id "$(pane_id "$TAB" web.dev)" "Ctrl c"
wait_for 20 bash -c "! '$ADE' ws status --json | jq -e '.[] | select(.node == \"web.dev\" and .alive)' >/dev/null" && pass "Ctrl-C in the web.dev pane stops web.dev" || fail "web.dev still alive after Ctrl-C"
"$ADE" ws status --json | jq -e '.[] | select(.node == "api.dev" and .alive)' >/dev/null && pass "api.dev keeps running (only the pane's own service stopped)" || fail "api.dev stopped with web.dev"
ZS action send-keys --pane-id "$(pane_id "$TAB" web.dev)" "Enter"
wait_for 120 bash -c "'$ADE' ws status --json | jq -e '.[] | select(.node == \"web.dev\" and .alive)' >/dev/null" && pass "Enter in the pane starts web.dev again" || fail "web.dev did not restart"

step "a second tab for the web worktree (w): the item's api tree is found without flags"
"$ADE" sync >/dev/null
"$ADE" ws up "$WEB_WT" --layout code --session "$SESSION" --json >"$T/open-web.json"
WEB_TAB=$(jq -r .tab "$T/open-web.json")
expect_json "a code-layout tab is created for the web worktree" "$T/open-web.json" '.created == true and .layout == "code" and .host == "zellij"'
wait_for 30 bash -c "$(declare -f pane_id ZS Z); SESSION=$SESSION; [[ -n \$(pane_id '$WEB_TAB' editor) && -n \$(pane_id '$WEB_TAB' overview) && -n \$(pane_id '$WEB_TAB' web.dev) ]]" || fail "web tab panes missing"
ZS action list-panes --json >"$T/panes2.json"
expect_json "the code layout has editor, overview, lazygit and shell panes" "$T/panes2.json" \
  "[.[] | select(.tab_name == \"$WEB_TAB\" and .is_plugin == false) | .title] | (index(\"editor\") != null and index(\"overview\") != null and index(\"lazygit\") != null and index(\"shell\") != null)"
expect_json "web.dev's pane carries --item-tree api=<the item's api worktree> (found from the cache)" "$T/panes2.json" \
  ".[] | select(.tab_name == \"$WEB_TAB\" and .title == \"web.dev\") | .terminal_command | contains(\"--item-tree api=$API_WT\")"
# One step for the whole graph: Enter in the tab's services pane runs `ade ws up --no-zellij` for this tree.
press_enter "$WEB_TAB" services
wait_for 120 screen_has "$WEB_TAB" services 'ready; Ctrl-C stops' || { dump "$WEB_TAB" services /dev/stderr; fail "the services pane did not bring the graph up"; }
pass "Enter in the services pane brought the whole run graph up in order"
dump "$WEB_TAB" overview "$OUT/devenv-web-tab-overview.txt"
"$ADE" ws status --json >"$T/status2.json"
expect_json "web.dev of the item's web worktree is one instance, held by both tabs" "$T/status2.json" \
  "[.[] | select(.node == \"web.dev\" and .tree == \"$WEB_WT\" and .alive)] | length == 1 and (.[0].holders | length) >= 2"
WEB2_PORT=$(jq -r ".[] | select(.node == \"web.dev\" and .tree == \"$WEB_WT\") | .port" "$T/status2.json")
curl -fsS --max-time 5 "http://127.0.0.1:$WEB2_PORT/healthz" >"$T/web2.health"
expect_json "it reaches the item's api, which both tabs share" "$T/web2.health" ".api.reachable == true and .api.url == \"http://127.0.0.1:$API_PORT\""
expect_json "a single api.dev instance runs, in the item's tree" "$T/status2.json" \
  "[.[] | select(.node == \"api.dev\")] | length == 1 and .[0].tree == \"$API_WT\""

step "the commit prefix is written per worktree for the user's own hooks"
for wt in "$API_WT" "$WEB_WT"; do
  [[ $(git -C "$wt" config --worktree --get ade.parent) == 3 && $(git -C "$wt" config --worktree --get ade.commitPrefix) == GL-3 ]] \
    && pass "$(basename "$(dirname "$wt")"): git config --worktree ade.parent 3, ade.commitPrefix GL-3" || fail "worktree config of $wt"
done
[[ $(git -C "$PROJ/api/main" config --worktree --get ade.commitPrefix 2>/dev/null || true) == "" ]] && pass "the baseline tree and other worktrees are untouched" || fail "baseline tree has the prefix"
git -C "$PROJ/api/.bare" config --get extensions.worktreeConfig | grep -qx true && pass "extensions.worktreeConfig is on" || fail "extensions.worktreeConfig"
[[ $(git -C "$API_WT" config --worktree --get ade.target) == main ]] && pass "ade.target records the MR's target branch (main)" || fail "ade.target of $API_WT"
# A commit-msg hook can read it (the docs/manifest.md sample): only the MR's first commit is prefixed.
mkdir -p "$T/hooks"
cat >"$T/hooks/commit-msg" <<'HOOK'
#!/bin/sh
prefix=$(git config --get ade.commitPrefix) || exit 0
[ -n "$prefix" ] || exit 0
target=$(git config --get ade.target) || exit 0
ahead=$(git rev-list --count "$target..HEAD" 2>/dev/null || git rev-list --count "origin/$target..HEAD" 2>/dev/null) || exit 0
[ "$ahead" = 0 ] || exit 0
case "$(head -n1 "$1")" in "$prefix"*) ;; *) printf '%s %s\n' "$prefix" "$(cat "$1")" >"$1" ;; esac
HOOK
chmod +x "$T/hooks/commit-msg"
hookcommit() { git -C "$API_WT" add "$1" && git -C "$API_WT" -c core.hooksPath="$T/hooks" -c user.name=t -c user.email=t@example.com commit -q -m "$2"; }
echo hook >"$API_WT/hook.txt"
hookcommit hook.txt "add hook file"
[[ $(git -C "$API_WT" log -1 --format=%s) == "GL-3 add hook file" ]] && pass "a commit-msg hook reading ade.commitPrefix prefixes the MR's first commit" || fail "hook result: $(git -C "$API_WT" log -1 --format=%s)"
echo hook2 >"$API_WT/hook2.txt"
hookcommit hook2.txt "add second file"
[[ $(git -C "$API_WT" log -1 --format=%s) == "add second file" ]] && pass "the second commit of the branch is not prefixed" || fail "second hook commit: $(git -C "$API_WT" log -1 --format=%s)"
git -C "$API_WT" reset -q --hard HEAD~2

step "agent commit and merge carry the prefix"
"$ADE" agent plan "$REF" --repo api --yes --json >"$T/plan.json" || { cat "$T/plan.json" >&2; fail "agent plan"; }
RUN=$(jq -r '.id // .run // .run_id' "$T/plan.json")
"$ADE" agent approve "$RUN" >/dev/null
"$ADE" agent run "$RUN" --json >"$T/run.json" || { cat "$T/run.json" >&2; fail "agent run"; }
"$ADE" agent push "$RUN" --json >"$T/push.json" || { cat "$T/push.json" >&2; fail "agent push"; }
SHA=$(jq -r '.commits[-1] // empty' "$T/push.json")
[[ -n $SHA ]] || { cat "$T/push.json" >&2; fail "no agent commit recorded"; }
SUBJECT=$(git -C "$API_WT" log -1 --format=%s "$SHA")
[[ $SUBJECT == GL-3\ * ]] && pass "the agent's commit message starts with the prefix: \"$SUBJECT\"" || fail "agent commit subject: $SUBJECT"
[[ $(git -C "$API_WT" log -1 --format=%an "$SHA") == *"ade agent"* ]] && pass "authored as the agent" || fail "agent author"
[[ $(git --git-dir "$T/remotes/api.git" log -1 --format=%s "$BRANCH") == "$SUBJECT" ]] && pass "the pushed branch on the remote has it" || fail "remote head differs"
# Merge through ADE: ready the MR, then ask for the merge. The fake (Premium) wants two approvals, so the merge
# is set to happen when the checks pass (--auto): the request, and so the commit messages, are the same.
"$ADE" mr ready "$API_MR" >/dev/null || fail "ade mr ready"
"$ADE" sync >/dev/null
"$ADE" mr merge "$API_MR" --squash --auto --dry-run >"$T/merge-dry.txt" 2>&1 || { cat "$T/merge-dry.txt" >&2; fail "mr merge --dry-run"; }
grep -q 'GL-3 ' "$T/merge-dry.txt" && grep -q 'squash message:' "$T/merge-dry.txt" \
  && pass "mr merge --dry-run shows the squash message: GL-3 + MR title" || { cat "$T/merge-dry.txt" >&2; fail "dry-run message"; }
if "$ADE" mr merge "$API_MR" --squash --auto --yes >"$T/merge.txt" 2>&1; then
  "$ADE" audit --json >"$T/audit.json" 2>"$T/audit.err" || { cat "$T/audit.err" >&2; fail "ade audit"; }
  expect_json "the merge request sent a prefixed squash message and no merge message (audited)" "$T/audit.json" \
    '[.[] | select(.action == "forge.mr.merge" and .outcome == "ok")] | length >= 1 and all(.[]; (.params.message == null) and (.params.squash_message | startswith("GL-3 ")))'
else
  cat "$T/merge.txt" >&2
  "$ADE" mr show "$API_MR" >&2 || true
  fail "ade mr merge"
fi

step "multi-root VS Code workspace (ade vscode workspace)"
"$ADE" vscode workspace "$REF" --json >"$T/ws.json" || { cat "$T/ws.json" >&2; fail "ade vscode workspace"; }
CW=$(jq -r .path "$T/ws.json")
[[ -f $CW && $CW == *.code-workspace ]] && pass "wrote $CW" || fail "no workspace file"
jq -e . "$CW" >/dev/null && pass "the .code-workspace is valid JSON" || fail "invalid JSON"
[[ $CW == "$XDG_DATA_HOME"/ade/items/* ]] && pass "it lives in the item's records directory, outside every git tree" || fail "workspace file location $CW"
expect_json "folders: all repos of the project, the item's worktrees (named by branch)" "$CW" \
  "(.folders | length) == 2 and ([.folders[] | .path] | sort) == ([\"$API_WT\", \"$WEB_WT\"] | sort) and all(.folders[]; .name | test(\"^(api|web) \\\\(3-retry\"))"
expect_json "tasks: ade: api.dev before web.dev (dependsOn), the compound ade: up, absolute cwd" "$CW" \
  '([.tasks.tasks[].label] | index("ade: web.dev") != null and index("ade: up") != null) and (.tasks.tasks[] | select(.label == "ade: web.dev") | .dependsOn == ["ade: api.dev"]) and all(.tasks.tasks[] | select(.options != null); .options.cwd | startswith("/"))'
expect_json "web's task is wired to the item's api (--item-tree api=...)" "$CW" \
  ".tasks.tasks[] | select(.label == \"ade: web.dev\") | .args | index(\"--item-tree\") != null and index(\"api=$API_WT\") != null"
# An item with no worktrees: every folder is a baseline main/.
"$ADE" vscode workspace ade-demo-env/tracker#8 --json >"$T/ws8.json"
expect_json "an item that was not started gets baseline folders: api (main), web (main)" "$(jq -r .path "$T/ws8.json")" \
  "([.folders[].name] | sort) == [\"api (main)\", \"web (main)\"] and ([.folders[].path] | sort) == [\"$PROJ/api/main\", \"$PROJ/web/main\"]"
"$ADE" vscode workspace "$WEB_WT" --open >"$T/ws-open.txt" 2>&1 || fail "ade vscode workspace --open"
grep -q '\.code-workspace' "$T/ws-open.txt" && pass "--open without code on PATH prints the file (no failure)" || { cat "$T/ws-open.txt" >&2; fail "--open output"; }

step "the throwaway session goes away"
pkill -f "zellij attach $SESSION" || true
Z delete-session --force "$SESSION" >/dev/null 2>&1
Z list-sessions --no-formatting 2>/dev/null | grep -q "^$SESSION " && fail "throwaway session not deleted" || pass "session $SESSION deleted"
wait_for 30 bash -c "[[ \$('$ADE' ws status --json | jq length) == 0 ]]" || "$ADE" ws down "$API_WT" >/dev/null 2>&1 || true

printf '\nDev environment: all checks passed (screens in .impeccable/review/devenv-*.txt)\n'
