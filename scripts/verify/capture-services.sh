#!/usr/bin/env bash
# Capture the workspace surfaces for visual review with VHS: the Services strip
# in the Home item preview (running, feature tree, shared, none, stopped, starting,
# failed, blocked, and a mixed six-instance strip that wraps), the `w` pop-ups (opened,
# focused, no session, plain host, failure, long failure) and the task.* pop-ups
# the task runner raises (ready, done, blocked, failed, exited).
# Output: .impeccable/review/services-*.png
#
#   scripts/verify/capture-services.sh        # desktop, narrow, no-colour sets
#
# Data: the in-process fake GitLab (scripts/verify/fakegl) for Home, and a
# synthetic project under /tmp/ade-demo-services built from local git repos whose trees
# hold the sandbox api and web servers. The strip shows real instances started
# with `ade ws up --detach`. zellij is never touched: the `w` tapes put a shim
# `zellij` first on PATH that answers the three calls Open makes.
. scripts/verify/lib.sh
build_ade
go build -o "$VERIFY_TMP/fakegl" ./scripts/verify/fakegl

"$VERIFY_TMP/fakegl" -tier premium >"$VERIFY_TMP/fake.url" 2>"$VERIFY_TMP/fake.err" &
FAKE_PID=$!
DEMO=/tmp/ade-demo-services
cleanup() {
  set +e
  for t in "$DEMO"/projects/demo-gl/*/*/; do "$ADE" ws down "$t" >/dev/null 2>&1; done
  "$ADE" ws status --json 2>/dev/null | jq -r '.[].pid // empty' | while read -r pid; do kill -TERM -- "-$pid" 2>/dev/null; done
  [[ -n ${SLEEP_PID:-} ]] && kill "$SLEEP_PID" 2>/dev/null
  pkill -f "$ADE dash" 2>/dev/null
  kill "$FAKE_PID" 2>/dev/null
  rm -rf "$VERIFY_TMP" "$DEMO"
}
trap cleanup EXIT
for _ in $(seq 50); do
  [[ -s $VERIFY_TMP/fake.url ]] && break
  sleep 0.1
done
FAKE_URL=$(head -1 "$VERIFY_TMP/fake.url")
[[ -n $FAKE_URL ]] || fail "the fake GitLab did not start: $(cat "$VERIFY_TMP/fake.err")"

# The sandbox servers compile on `go run .`; keep the real build cache.
export GOCACHE=${GOCACHE:-$(go env GOCACHE)} GOMODCACHE=${GOMODCACHE:-$(go env GOMODCACHE)}
rm -rf "$DEMO"
mkdir -p "$DEMO/data" "$DEMO/cache" "$DEMO/projects" "$DEMO/shim"
export XDG_DATA_HOME="$DEMO/data" XDG_CACHE_HOME="$DEMO/cache" XDG_CONFIG_HOME="$DEMO/config"
export ADE_CONFIG="$DEMO/config.toml" FAKE_GITLAB_TOKEN=fake-token
unset ZELLIJ ZELLIJ_SESSION_NAME ZELLIJ_PANE_ID TERM_PROGRAM
for mode in zellij plain; do
  cat >"$DEMO/config-$mode.toml" <<EOF
[user]
handle = "sandbox"
[host]
mode = "$mode"
[forges.sandbox-gitlab]
kind = "gitlab"
url = "$FAKE_URL"
token_env = "FAKE_GITLAB_TOKEN"
git_protocol = "https"
[update]
on_start = false
[paths]
root = "$DEMO/projects"
[[projects]]
source = "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
EOF
done
cp "$DEMO/config-zellij.toml" "$ADE_CONFIG"

# --- the project on disk: bare clones with a main tree and a branch tree each ---
PROJ="$DEMO/projects/demo-gl"
mkdir -p "$PROJ"
mkrepo() { # repo app-dir branch
  local repo=$1 app=$2 branch=$3 seed
  seed=$(mktemp -d -p "$VERIFY_TMP")
  git -C "$seed" init -q -b main
  cp "$REPO/scripts/sandbox/apps/$app/main.go" "$REPO/scripts/sandbox/apps/$app/go.mod" "$seed/"
  git -C "$seed" add . && git -C "$seed" -c user.name=demo -c user.email=demo@example.invalid commit -q -m init
  mkdir -p "$PROJ/$repo"
  git clone -q --bare "$seed" "$PROJ/$repo/.bare"
  echo "gitdir: ./.bare" >"$PROJ/$repo/.git"
  git -C "$PROJ/$repo/.bare" worktree add -q "$PROJ/$repo/main" main
  git -C "$PROJ/$repo/.bare" worktree add -q -b "$branch" "$PROJ/$repo/$branch" main
}
API_BRANCH=1-rate-limit-the-export-endpoint
WEB_BRANCH=1-paginate-the-audit-log-view
mkrepo api api "$API_BRANCH"
mkrepo web web "$WEB_BRANCH"
printf 'source = "fake"\nmanifest = '"'''"'\n' >"$PROJ/.project.toml"
cat "$REPO/scripts/sandbox/manifests/gitlab.ade.toml" >>"$PROJ/.project.toml"
printf "'''\n" >>"$PROJ/.project.toml"

OUT="$REPO/.impeccable/review"
mkdir -p "$OUT"
# ONLY=<regex> re-captures just the tapes whose name matches (the instance setup still
# runs); without it every services-*.png is rebuilt.
ONLY=${ONLY:-.}
for f in "$OUT"/services-*.png; do
  [[ -e $f ]] && [[ $(basename "$f" .png) =~ $ONLY ]] && rm -f "$f"
done
ALL_SHOTS=()
"$ADE" sync >/dev/null

# --- zellij shim: only for the `w` tapes; behaviour chosen by $ZSHIM ---
cat >"$DEMO/shim/zellij" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  list-sessions*) [[ ${ZSHIM:-} == nosession ]] || echo "ade [Created 5m ago]" ;;
  *query-tab-names*) if [[ ${ZSHIM:-} == focus ]]; then printf 'home\napi/1-rate-limit-the-export-endpoint\n'; else echo home; fi ;;
  *) echo 1 ;;
esac
EOF
chmod +x "$DEMO/shim/zellij"

cat >"$VERIFY_TMP/run.sh" <<EOF
#!/usr/bin/env bash
export ADE_CONFIG="\${ADE_CFG:-$ADE_CONFIG}" XDG_DATA_HOME="$XDG_DATA_HOME" XDG_CACHE_HOME="$XDG_CACHE_HOME" FAKE_GITLAB_TOKEN=fake-token
export GOCACHE="$GOCACHE"
[[ -n \${ZSHIM:-} ]] && export PATH="$DEMO/shim:\$PATH"
\${NOCOLOR:+export NO_COLOR=1}
exec "$ADE" dash
EOF
chmod +x "$VERIFY_TMP/run.sh"

# tape NAME FONTSIZE NOCOLOR ENV BODY [INJECT [HEIGHT]]: one dashboard run per tape.
# ENV is extra `Env K "V"` lines; BODY the keys and screenshots after the first
# frame. INJECT is a command run ~3s after the dashboard starts (task.* events
# are sent by another ade process, as in real use). Every Screenshot in a tape
# must exist afterwards: a missing one re-runs the tape once, then fails the script.
tape() {
  local name=$1 font=$2 nc=$3 envs=$4 body=$5 inject=${6:-} height=${7:-900}
  local file="$VERIFY_TMP/$name.tape" shots shot attempt missing injector
  [[ $name =~ $ONLY ]] || return 0
  cat >"$file" <<EOF
Output "$VERIFY_TMP/$name.gif"
Set Shell bash
Set Width 1600
Set Height $height
Set FontSize $font
Set Padding 0
Set TypingSpeed 0
Env COLORTERM "truecolor"
Env NOCOLOR "$nc"
$envs
Hide
Type "$VERIFY_TMP/run.sh"
Enter
Show
Sleep 4s
$body
Sleep 300ms
EOF
  mapfile -t shots < <(sed -n 's/^Screenshot "\(.*\)"$/\1/p' "$file")
  ALL_SHOTS+=("${shots[@]}")
  for attempt in 1 2; do
    rm -f "${shots[@]}"
    injector=
    if [[ -n $inject ]]; then
      # Wait for the dashboard, give it its first inbox poll, then send.
      (
        for _ in $(seq 100); do pgrep -f "$ADE dash" >/dev/null && break; sleep 0.2; done
        sleep 3
        eval "$inject"
      ) &
      injector=$!
    fi
    env -u NO_COLOR TERM=xterm-256color vhs "$file" >/dev/null 2>&1 || true
    [[ -n $injector ]] && wait "$injector"
    pkill -f "$ADE dash" || true
    [[ -n $inject ]] && "$ADE" inbox read --all >/dev/null 2>&1
    missing=0
    for shot in "${shots[@]}"; do [[ -s $shot ]] || missing=1; done
    ((missing == 0)) && break
  done
  return 0
}

sets() { # fn [args...]: call fn once per set with (set fontsize nocolor args...)
  local fn=$1
  shift
  "$fn" desktop 14 "" "$@"
  "$fn" narrow 30 "" "$@"
  "$fn" nocolor 14 1 "$@"
}

# detail SET: narrow terminals show one pane at a time; Tab flips to the preview.
detail() { [[ $1 == narrow ]] && printf 'Tab\nSleep 500ms\n'; true; }
# strip_height SET: a tall viewport for the narrow strip captures, so the whole
# preview (and the strip's last line) is visible instead of clipped above the footer.
strip_height() { [[ $1 == narrow ]] && echo 1700 || echo 900; }

# strip_one SET FONT NC STATE MOVES: tracker#1 (row 1) is selected at start; j moves
# down; G goes to the last row.
strip_one() {
  local set=$1 font=$2 nc=$3 state=$4 moves=$5 d
  d=$(detail "$set")
  tape "services-$set-$state" "$font" "$nc" "" "$moves
Sleep 500ms
$d
Screenshot \"$OUT/services-$set-$state.png\"" "" "$(strip_height "$set")"
}

# --- real instances running -------------------------------------------------------
step "start the instances: web on its feature tree against the api feature tree"
"$ADE" ws up --no-zellij --detach "$PROJ/web/$WEB_BRANCH" --with "api=$API_BRANCH" >/dev/null || fail "ws up web"
"$ADE" ws up --no-zellij --detach "$PROJ/api/$API_BRANCH" --run api.dev >/dev/null || fail "ws up api"
sleep 3 # the dashboard polls every two seconds

step "capture: running and shared (tracker#1), feature tree and shared (tracker#2), none"
sets strip_one running ""
sets strip_one feature 'Type "j"'
sets strip_one none 'Type "G"'

# --- a mixed strip: every state at once, six instances ----------------------------
# Extra rows for tracker#2's web worktree: a database that is ready, a worker still
# starting, a queue that died before it was ready and a cache that stopped.
DB="$XDG_CACHE_HOME/ade/cache.db"
sleep 600 &
SLEEP_PID=$!
# A pid that is certainly dead: a process that has exited and been reaped.
sleep 0.1 &
DEAD_PID=$!
wait "$DEAD_PID"
WEB_TREE="$PROJ/web/$WEB_BRANCH"
extra() { # node port pid state
  sqlite3 "$DB" "INSERT INTO ws_instances (id,node,tree,project,kind,port,pid,pstart,state,keep,log,started_at)
    VALUES ('$1@$WEB_TREE','$1','$WEB_TREE','demo-gl','service',$2,$3,0,'$4',0,'/dev/null',$(date +%s)000000000);
    INSERT INTO ws_holds (instance,holder,pid,pstart) VALUES ('$1@$WEB_TREE','$WEB_TREE',0,0);"
}
extra services.db 5432 "$SLEEP_PID" ready
extra worker.dev 0 "$SLEEP_PID" starting
extra queue.dev 0 "$DEAD_PID" starting
extra cache.dev 6379 "$DEAD_PID" ready
sleep 3
step "capture: mixed strip (ready, shared, feature tree, starting, failed, stopped)"
sets strip_one mixed 'Type "j"'
for n in services.db worker.dev queue.dev cache.dev; do
  sqlite3 "$DB" "DELETE FROM ws_instances WHERE id = '$n@$WEB_TREE'"
done

# --- the api dies behind ADE's back -----------------------------------------------
API_PID=$("$ADE" ws status --json | jq -r '.[] | select(.node == "api.dev") | .pid')
kill -TERM -- "-$API_PID"
sleep 3
step "capture: stopped (tracker#1) and blocked (tracker#2: web.dev still runs, its api died)"
sets strip_one stopped ""
sets strip_one blocked 'Type "j"'

# --- an instance that is still starting -------------------------------------------
sqlite3 "$DB" "UPDATE ws_instances SET state = 'starting', pid = $SLEEP_PID, pstart = 0 WHERE node = 'api.dev'"
sleep 3
step "capture: starting"
sets strip_one starting ""

# --- it died before it became ready -------------------------------------------------
sqlite3 "$DB" "UPDATE ws_instances SET state = 'starting', pid = $DEAD_PID, pstart = 0 WHERE node = 'api.dev'"
sleep 3
step "capture: failed"
sets strip_one failed ""
kill "$SLEEP_PID" 2>/dev/null || true
SLEEP_PID=
for t in "$PROJ"/*/*/; do "$ADE" ws down "$t" >/dev/null 2>&1 || true; done

# --- the `w` pop-ups --------------------------------------------------------------
# Row 1 of Home is tracker#1, whose api worktree exists. The failure state moves to
# tracker#11 (sixth row; no app label, so no repo to open).
popup_one() { # set font nc state zshim cfg keys
  local set=$1 font=$2 nc=$3 state=$4 zshim=$5 cfg=$6 keys=$7
  tape "services-$set-w-$state" "$font" "$nc" "Env ZSHIM \"$zshim\"
Env ADE_CFG \"$cfg\"" "$keys
Type \"w\"
Sleep 900ms
Screenshot \"$OUT/services-$set-w-$state.png\""
}
step "capture: w pop-ups"
sets popup_one opened live "$DEMO/config-zellij.toml" ""
sets popup_one focused focus "$DEMO/config-zellij.toml" ""
sets popup_one nosession nosession "$DEMO/config-zellij.toml" ""
sets popup_one plain live "$DEMO/config-plain.toml" ""
sets popup_one failed live "$DEMO/config-zellij.toml" 'Type "jjjjj"
Sleep 500ms'
# A long failure: the project snapshot is gone, so the error carries a path and an
# OS message. The recovery text must wrap, not be cut off.
mv "$PROJ/.project.toml" "$PROJ/.project.toml.off"
sets popup_one failed-long live "$DEMO/config-zellij.toml" ""
mv "$PROJ/.project.toml.off" "$PROJ/.project.toml"

# --- task.* pop-ups: events another ade process records (ade task run, ade ws up) ---
task_one() { # set font nc kind severity subject title detail
  local set=$1 font=$2 nc=$3 kind=$4 sev=$5 subj=$6 title=$7 detail=$8
  tape "services-$set-task-${kind#task.}" "$font" "$nc" "" "Sleep 4s
Screenshot \"$OUT/services-$set-task-${kind#task.}.png\"" \
    "\"$ADE\" notify send --kind $kind --severity $sev --project demo-gl --subject '$subj' --title '$title' --detail '$detail' >/dev/null"
}
step "capture: task.* pop-ups"
LOG='/tmp/ade-demo-services/cache/ade/ws/api.dev-719ef70f.log'
# Titles and subjects are exactly what the task runner publishes (workspace.ResultEvent).
sets task_one task.ready success "api/1-rate-limit-the-export-endpoint" "api.dev ready on :37007" "log: $LOG"
sets task_one task.done success "api/main" "api.check finished" "log: $LOG"
sets task_one task.blocked warn $'api.dev did not come up\nfix it, then run ade ws up' "web.dev waits for api.dev" "log: $LOG"
sets task_one task.failed error $'port 37007 is already in use\nfree the port, then run ade ws up' "api.dev failed to start" "log: $LOG"
sets task_one task.exited warn $'last log line: panic: out of memory\nrestart with ade ws up' "api.dev exited" "log: $LOG"

# Every screenshot every tape asked for must exist.
bad=0
for shot in "${ALL_SHOTS[@]}"; do
  [[ -s $shot ]] || {
    echo "MISSING: $shot" >&2
    bad=1
  }
done
((bad == 0)) || exit 1
echo "${#ALL_SHOTS[@]} screenshots"
ls "$OUT"/services-*.png
