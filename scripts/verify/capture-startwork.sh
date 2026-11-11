#!/usr/bin/env bash
# Capture the Start work dialog (choose, confirm, M create-MR-later, and their
# edge states) for visual review with VHS. Output: .impeccable/review/startwork-*.png
#
#   scripts/verify/capture-startwork.sh        # desktop, narrow, no-colour sets
#
# Data comes from the in-process fake GitLab (scripts/verify/fakegl, Free tier,
# with an MR template in api), never the live forge, and real git remotes on
# disk under /tmp/ade-demo so the worktree step really runs. Every set starts
# from freshly seeded state.
. scripts/verify/lib.sh
build_ade
go build -o "$VERIFY_TMP/fakegl" ./scripts/verify/fakegl

DEMO=/tmp/ade-demo-startwork # not /tmp/ade-demo: other capture scripts delete it
OUT="$REPO/.impeccable/review"
mkdir -p "$OUT"
FAKE_PID="" SLOW_PID=""
cleanup() {
  [[ -n $FAKE_PID ]] && kill "$FAKE_PID" 2>/dev/null || true
  [[ -n $SLOW_PID ]] && kill "$SLOW_PID" 2>/dev/null || true
  rm -rf "$VERIFY_TMP" "$DEMO"
}
trap cleanup EXIT

export GIT_AUTHOR_NAME=demo GIT_AUTHOR_EMAIL=demo@example.com GIT_COMMITTER_NAME=demo GIT_COMMITTER_EMAIL=demo@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

serve() { # PIDVAR URLFILE ARGS...: start a fake GitLab, wait for its URL
  local var=$1 file=$2
  shift 2
  : >"$file"
  "$VERIFY_TMP/fakegl" "$@" >"$file" 2>"$file.err" &
  printf -v "$var" '%s' "$!"
  for _ in $(seq 50); do
    [[ -s $file ]] && break
    sleep 0.1
  done
  [[ -s $file ]] || fail "the fake GitLab did not start: $(cat "$file.err")"
}

remote() { # NAME BRANCH...: a bare remote with main and the given branches
  local name=$1 work="$DEMO/work-$1"
  shift
  git init -q --bare -b main "$DEMO/remotes/$name.git"
  git clone -q "$DEMO/remotes/$name.git" "$work" 2>/dev/null
  printf '%s service\n' "$name" >"$work/README.md"
  git -C "$work" add README.md
  git -C "$work" commit -q -m "Initial commit"
  git -C "$work" push -q origin main
  local b
  for b in "$@"; do git -C "$DEMO/remotes/$name.git" branch "$b" main; done
  rm -rf "$work"
}

# fresh: reset all state. The api remote already has the branches Start work
# will cut: tracker#4 is started first (child api#3), tracker#3 later (child api#4), as the forge's
# own repository would after creating them.
fresh() {
  [[ -n $FAKE_PID ]] && kill "$FAKE_PID" 2>/dev/null || true
  [[ -n $SLOW_PID ]] && kill "$SLOW_PID" 2>/dev/null || true
  rm -rf "$DEMO"
  mkdir -p "$DEMO/data" "$DEMO/cache" "$DEMO/projects" "$DEMO/remotes"
  remote api 3-remove-the-legacy-csv-export-path 4-retry-failed-webhook-deliveries
  remote web
  serve FAKE_PID "$VERIFY_TMP/fake.url" -tier free -mr-template
  serve SLOW_PID "$VERIFY_TMP/slow.url" -tier free -mr-template -latency 1500ms
  awk -v api="file://$DEMO/remotes/api.git" -v web="file://$DEMO/remotes/web.git" '
    {print}
    /^path +=.*\/api"/ {print "url  = \"" api "\""}
    /^path +=.*\/web"/ {print "url  = \"" web "\""}' \
    "$REPO/scripts/sandbox/manifests/gitlab.ade.toml" >"$DEMO/demo.ade.toml"
  export XDG_DATA_HOME="$DEMO/data" XDG_CACHE_HOME="$DEMO/cache" FAKE_GITLAB_TOKEN=fake-token
  config "$(head -1 "$VERIFY_TMP/fake.url")" "$DEMO/config.toml"
  config "$(head -1 "$VERIFY_TMP/slow.url")" "$DEMO/config-slow.toml"
  export ADE_CONFIG="$DEMO/config.toml"
  "$ADE" project add "$DEMO/demo.ade.toml" >/dev/null
  "$ADE" sync >/dev/null
  # tracker#4 is already branched, waiting for its MR (the M state).
  "$ADE" start ade-demo-env/tracker#4 --repo api --mode branch --no-workspace >/dev/null
  "$ADE" sync >/dev/null
}

config() { # URL FILE
  cat >"$2" <<EOC
[user]
handle = "sandbox"
[forges.sandbox-gitlab]
kind = "gitlab"
url = "$1"
token_env = "FAKE_GITLAB_TOKEN"
git_protocol = "https"
[update]
on_start = false
[host]
mode = "plain"
[paths]
root = "$DEMO/projects"
[[projects]]
source = "$DEMO/demo.ade.toml"
EOC
}

cat >"$VERIFY_TMP/run.sh" <<EOS
#!/usr/bin/env bash
unset ZELLIJ ZELLIJ_SESSION_NAME ZELLIJ_PANE_ID
export ADE_CONFIG="\${SLOW:+$DEMO/config-slow.toml}"
[[ -n \$ADE_CONFIG ]] || export ADE_CONFIG="$DEMO/config.toml"
export XDG_DATA_HOME="$DEMO/data" XDG_CACHE_HOME="$DEMO/cache" FAKE_GITLAB_TOKEN=fake-token
\${NOCOLOR:+export NO_COLOR=1}
exec "$ADE" dash
EOS
chmod +x "$VERIFY_TMP/run.sh"

# head NAME FONT NOCOLOR SLOW: the tape preamble. Home opens on its first row.
head_() {
  cat <<EOT
Output "$VERIFY_TMP/$1.gif"
Set Shell bash
Set Width 1600
Set Height 900
Set FontSize $2
Set Padding 0
Set TypingSpeed 0
Env COLORTERM "truecolor"
Env NOCOLOR "$3"
Env SLOW "$4"
Hide
Type "$VERIFY_TMP/run.sh"
Enter
Show
Sleep 2500ms
EOT
}

shot() { printf 'Sleep 700ms\nScreenshot "%s/startwork-%s-%s.png"\n' "$OUT" "$1" "$2"; }

# The tracker rows in Home's order (court, then stage). `goto N` presses j from
# the first row to reach tracker#N; the #3 flow runs last because starting work
# re-sorts the rows.
ROWS=(1 2 4 3 8 11 5 6)
goto() { # N: j presses to reach tracker#N from the first row
  local n=$1 i
  printf 'Type "g"\n'
  for i in "${!ROWS[@]}"; do
    if [[ ${ROWS[$i]} == "$n" && $i -gt 0 ]]; then printf 'Type "%s"\n' "$(printf 'j%.0s' $(seq 1 "$i"))"; fi
  done
}

tape() { # NAME FONT NOCOLOR
  local name=$1
  {
    head_ "$name" "$2" "$3" ""
    # Browse: no forge writes
    goto 8
    printf 'Type "s"\nSleep 1500ms\nType "m"\nType "m"\n'
    shot "$name" browse-choose
    printf 'Enter\nSleep 2500ms\n'
    shot "$name" browse-confirm
    printf 'Escape\nSleep 300ms\nEscape\nSleep 500ms\n'
    # two app labels: a child in each repo
    goto 8
    printf 'Type "s"\nSleep 1500ms\n'
    shot "$name" cross-repo-choose
    printf 'Enter\nSleep 2500ms\n'
    shot "$name" cross-repo-confirm
    printf 'Escape\nSleep 300ms\nEscape\nSleep 500ms\n'
    # nothing routes from tracker#11 (no app label): nothing preselected
    goto 11
    printf 'Type "s"\nSleep 1500ms\n'
    shot "$name" empty
    printf 'Enter\nSleep 500ms\n'
    shot "$name" empty-warning
    printf 'Escape\nSleep 500ms\n'
    # M: tracker#4 is branched and waits for its MR
    goto 4
    printf 'Type "M"\nSleep 3000ms\n'
    shot "$name" mr-confirm
    printf 'Escape\nSleep 500ms\n'
    # M on work that is not started: nothing to create (error state)
    goto 1
    printf 'Type "M"\nSleep 3000ms\n'
    shot "$name" mr-nothing
    printf 'Escape\nSleep 300ms\n'
    # choose: tracker#3 routes to api by its app label
    goto 3
    printf 'Type "s"\nSleep 1500ms\n'
    shot "$name" choose
    # confirm: every write, the template body, quick actions kept
    printf 'Enter\nSleep 2500ms\n'
    shot "$name" confirm
    # strip the reviewer request only
    printf 'Type "j"\nType "x"\n'
    shot "$name" confirm-stripped
    # run it: created rows, worktree, references
    printf 'Enter\nSleep 4000ms\n'
    shot "$name" "done"
    printf 'Enter\nSleep 800ms\n'
  } >"$VERIFY_TMP/$name.tape"
}

loading_tape() { # NAME FONT NOCOLOR: against the slow fake
  local name=$1
  {
    head_ "$name" "$2" "$3" "1"
    goto 3
    printf 'Type "s"\nSleep 700ms\n'
    shot "$name" loading
    printf 'Sleep 6000ms\nEnter\nSleep 900ms\n'
    shot "$name" planning
    printf 'Escape\n'
  } >"$VERIFY_TMP/$name.tape"
}

capture() { # NAME FONT NOCOLOR
  rm -f "$OUT"/startwork-"$1"-*.png
  fresh
  tape "$1" "$2" "$3"
  env -u NO_COLOR TERM=xterm-256color vhs "$VERIFY_TMP/$1.tape" >/dev/null 2>&1 || true
  pkill -f "$ADE dash" || true
  fresh
  loading_tape "$1-l" "$2" "$3"
  env -u NO_COLOR TERM=xterm-256color vhs "$VERIFY_TMP/$1-l.tape" >/dev/null 2>&1 || true
  pkill -f "$ADE dash" || true
  for f in "$OUT"/startwork-"$1"-l-*.png; do mv "$f" "${f/-$1-l-/-$1-}"; done
}

SETS=${SETS:-"desktop narrow nocolor"} # SETS=desktop for a quick look
for set in $SETS; do
  case $set in
  desktop) capture desktop 14 "" ;;
  narrow) capture narrow 30 "" ;;
  nocolor) capture nocolor 14 1 ;;
  esac
done
ls "$OUT"/startwork-*.png
