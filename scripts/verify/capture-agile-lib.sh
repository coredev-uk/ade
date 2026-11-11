# Shared by capture-standup.sh and capture-sprint.sh: runs the Standup or Sprint
# review page against the in-process fake GitLab (Premium, with the extra history
# of fakegl -agile), a canned OpenAI-compatible endpoint (fakeai: no spend) and
# throwaway state under a temp dir. Nothing touches a live forge or any dotfile.
#
# Needs SURF (standup|sprint) and KEY (S|V) set before sourcing.
. scripts/verify/lib.sh
build_ade
go build -o "$VERIFY_TMP/fakegl" ./scripts/verify/fakegl
go build -o "$VERIFY_TMP/fakeai" ./scripts/verify/fakeai

start() { # NAME CMD...: start a helper that prints its URL on the first line
  local name=$1
  shift
  "$@" >"$VERIFY_TMP/$name.url" 2>"$VERIFY_TMP/$name.err" &
  PIDS+=($!)
  for _ in $(seq 50); do
    [[ -s $VERIFY_TMP/$name.url ]] && break
    sleep 0.1
  done
  URL=$(head -1 "$VERIFY_TMP/$name.url")
  [[ -n $URL ]] || fail "$name did not start: $(cat "$VERIFY_TMP/$name.err")"
}
PIDS=()
trap 'kill "${PIDS[@]}" 2>/dev/null || true; rm -rf "$VERIFY_TMP"' EXIT
start gl "$VERIFY_TMP/fakegl" -tier premium -agile
FAST=$URL
start slow "$VERIFY_TMP/fakegl" -tier premium -agile -latency 1500ms
SLOW=$URL
start ai "$VERIFY_TMP/fakeai"
AI=$URL

DEMO="$VERIFY_TMP/ade-demo"
export XDG_DATA_HOME="$DEMO/data" XDG_CACHE_HOME="$DEMO/cache"
export FAKE_GITLAB_TOKEN=fake-token FAKE_AI_KEY=fake-key
mkdir -p "$DEMO"

# The fake is Premium, so weights come from its weight field, not labels.
sed 's/^weight .*/weight     = { source = "gitlab" }/' "$REPO/scripts/sandbox/manifests/gitlab.ade.toml" >"$DEMO/manifest.ade.toml"

config() { # FILE FORGE_URL AI_MODE [CONFIRM_OVER_USD]
  cat >"$1" <<EOC
[user]
handle = "sandbox"
timezone = "UTC"
[forges.sandbox-gitlab]
kind = "gitlab"
url = "$2"
token_env = "FAKE_GITLAB_TOKEN"
git_protocol = "https"
[update]
on_start = false
[paths]
root = "$DEMO/projects"
[ai]
mode = "$3"
profile = "fake"
[ai.profiles.fake]
driver = "openai-compat"
base_url = "$AI"
key_env = "FAKE_AI_KEY"
models = { explain = "fake-model", draft = "fake-model", review = "fake-model" }
prices = { "fake-model" = { input = 1.0, output = 2.0 } }
[budget]
call_usd = 0.5
daily_usd = 3
confirm_over_usd = ${4:-0}
[[projects]]
source = "$DEMO/manifest.ade.toml"
EOC
}
config "$DEMO/main.toml" "$FAST" auto
config "$DEMO/slow.toml" "$SLOW" auto
config "$DEMO/down.toml" "http://127.0.0.1:1" auto
config "$DEMO/ask.toml" "$FAST" ask 0.0001
config "$DEMO/off.toml" "$FAST" off

OUT="$REPO/.impeccable/review"
mkdir -p "$OUT"
rm -f "$OUT/$SURF"-*.png

# runner CFG [XDG_DIR]: the program a tape starts.
runner() {
  cat >"$VERIFY_TMP/run-$1.sh" <<EOR
#!/usr/bin/env bash
export ADE_CONFIG="$DEMO/$1.toml" XDG_DATA_HOME="${2:-$XDG_DATA_HOME}" XDG_CACHE_HOME="${3:-$XDG_CACHE_HOME}" FAKE_GITLAB_TOKEN=fake-token FAKE_AI_KEY=fake-key
\${NOCOLOR:+export NO_COLOR=1}
exec "$ADE" dash
EOR
  chmod +x "$VERIFY_TMP/run-$1.sh"
}
for c in main slow down ask off; do runner "$c"; done
runner main "$DEMO/empty-data" "$DEMO/empty-cache"
mv "$VERIFY_TMP/run-main.sh" "$VERIFY_TMP/run-empty.sh"
runner main

# shot_tape SET FONT NOCOLOR STATE CFG BODY: one tape, one run, screenshots in BODY.
shot_tape() {
  local set=$1 font=$2 nc=$3 state=$4 cfg=$5 body=$6
  cat >"$VERIFY_TMP/$set-$state.tape" <<EOT
Output "$VERIFY_TMP/$set-$state.gif"
Set Shell bash
Set Width 1600
Set Height 900
Set FontSize $font
Set Padding 0
Set TypingSpeed 0
Env COLORTERM "truecolor"
Env NOCOLOR "$nc"
Hide
Type "$VERIFY_TMP/run-$cfg.sh"
Enter
Show
Sleep 2500ms
Type "$KEY"
$body
EOT
  local tape="$VERIFY_TMP/$set-$state.tape" shots
  mapfile -t shots < <(sed -n 's/^Screenshot "\(.*\)"$/\1/p' "$tape")
  ALL_SHOTS+=("${shots[@]}")
  # VHS occasionally drops a screenshot; run the tape again once if any is missing.
  local attempt shot missing
  for attempt in 1 2; do
    env -u NO_COLOR TERM=xterm-256color vhs "$tape" >/dev/null 2>&1 || true
    pkill -f "$ADE dash" || true
    missing=0
    for shot in "${shots[@]}"; do [[ -s $shot ]] || missing=1; done
    ((missing == 0)) && break
  done
}
ALL_SHOTS=()

# verify_shots: every screenshot a tape asked for must exist; otherwise exit 1.
verify_shots() {
  local shot bad=0
  for shot in "${ALL_SHOTS[@]}"; do
    [[ -s $shot ]] || { echo "MISSING: $shot" >&2; bad=1; }
  done
  ((bad == 0)) || exit 1
}

snap() { printf 'Sleep %s\nShow\nScreenshot "%s/%s-%s-%s.png"\n' "$1" "$OUT" "$SURF" "$SET" "$2"; }

reset_state() { # each set starts with no cached answers and a fresh sync
  rm -rf "$XDG_CACHE_HOME/ade" "$XDG_DATA_HOME/ade/items"
  ADE_CONFIG="$DEMO/main.toml" "$ADE" sync >/dev/null
}

capture_set() { # SET FONT NOCOLOR
  SET=$1
  local font=$2 nc=$3
  reset_state
  shot_tape "$SET" "$font" "$nc" empty empty "$(snap 1500ms empty)"
  shot_tape "$SET" "$font" "$nc" loading slow "$(snap 500ms loading)"
  shot_tape "$SET" "$font" "$nc" down down "$(snap 3000ms down)"
  shot_tape "$SET" "$font" "$nc" confirm ask "$(snap 2500ms ready; printf 'Type "n"\n'; snap 800ms confirm)"
  shot_tape "$SET" "$font" "$nc" off off "$(snap 2500ms ready; printf 'Type "n"\n'; snap 800ms off)"
  shot_tape "$SET" "$font" "$nc" main main "$(page_body)"
}
