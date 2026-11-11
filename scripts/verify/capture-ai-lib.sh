#!/usr/bin/env bash
# Shared setup for the AI surface captures (capture-budget.sh, capture-log.sh,
# capture-classify.sh). Source it from the repo root inside `nix develop`:
#
#   . scripts/verify/capture-ai-lib.sh
#   ai_capture_init <prefix>          # fake GitLab + gateway, binary, base config, a sync
#   use_config NAME MODE DAILY [OMP]  # a config variant (AI mode, daily cap, omp command)
#   seed_ai                           # a week of synthetic AI spend in the audit log
#   capture NAME FONT NOCOLOR BODY    # run one VHS tape, BODY = the lines after the first frame
#
# use_config also reads GW_URL (the gateway profile's provider: fakegw ok /
# slow / reject, GW_OK by default), GW_BILLING (its billing source) and
# FORGE_URL (the fake GitLab, or one with latency).
#
# Everything is synthetic: the in-process fake GitLab (scripts/verify/fakegl),
# an omp stub that answers from a canned line (nothing is spent, no network),
# and audit entries written by seed_ai. Nothing touches the live forge.
. scripts/verify/lib.sh

# start_service VAR BIN [ARGS...]: run a helper that prints its URL on the
# first line; VAR holds the URL once it is up. Stopped on exit.
BG_PIDS=()
start_service() {
  local var=$1 bin=$2 file
  shift 2
  file=$VERIFY_TMP/$var.url
  "$VERIFY_TMP/$bin" "$@" >"$file" 2>"$VERIFY_TMP/$var.err" &
  BG_PIDS+=($!)
  local _
  for _ in $(seq 50); do
    [[ -s $file ]] && break
    sleep 0.1
  done
  [[ -s $file ]] || fail "$bin did not start: $(cat "$VERIFY_TMP/$var.err")"
  printf -v "$var" %s "$(head -1 "$file")"
}

ai_capture_init() {
  PREFIX=$1
  build_ade
  go build -o "$VERIFY_TMP/fakegl" ./scripts/verify/fakegl
  go build -o "$VERIFY_TMP/fakegw" ./scripts/verify/fakegw
  trap 'kill "${BG_PIDS[@]}" 2>/dev/null || true; pkill -f "$ADE dash" 2>/dev/null || true; rm -rf "$VERIFY_TMP"' EXIT
  start_service FAKE_URL fakegl -tier premium
  start_service GW_OK fakegw # the gateway profile's healthy provider
  GW_URL=$GW_OK
  GW_BILLING=litellm
  FORGE_URL=$FAKE_URL
  export XDG_DATA_HOME="$VERIFY_TMP/data" XDG_CACHE_HOME="$VERIFY_TMP/cache" FAKE_GITLAB_TOKEN=fake-token
  OUT="$REPO/.impeccable/review"
  mkdir -p "$OUT"
  rm -f "$OUT/$PREFIX"-*.png

  # omp stubs: one canned turn_end line, as `omp -p --mode json` prints it.
  local text='{"type":"bug","apps":["web"],"reason":"Totals lag behind the table after a filter change: a stale-data defect in the exports UI."}'
  local line
  line=$(jq -nc --arg t "$text" '{type:"turn_end",message:{role:"assistant",model:"claude-haiku-4-5",provider:"anthropic",stopReason:"stop",content:[{type:"text",text:$t}],usage:{input:620,output:48,cost:{total:0.0009}}}}')
  printf '%s\n' "$line" >"$VERIFY_TMP/canned.jsonl"
  printf '#!/bin/sh\ncat >/dev/null\ncat "%s"\n' "$VERIFY_TMP/canned.jsonl" >"$VERIFY_TMP/omp-ok"
  printf '#!/bin/sh\ncat >/dev/null\nsleep 4\ncat "%s"\n' "$VERIFY_TMP/canned.jsonl" >"$VERIFY_TMP/omp-slow"
  chmod +x "$VERIFY_TMP/omp-ok" "$VERIFY_TMP/omp-slow"
  OMP_OK=$VERIFY_TMP/omp-ok
  OMP_SLOW=$VERIFY_TMP/omp-slow
  OMP_GONE=omp-not-installed # not on PATH

  use_config base off 2.00
  "$ADE" sync >/dev/null
}

# use_config NAME MODE DAILY [OMP_COMMAND]: write $VERIFY_TMP/cfg-NAME.toml and
# make it the one `ade` and the dashboard use from now on.
use_config() {
  local name=$1 mode=$2 daily=$3 omp=${4:-$OMP_OK}
  ADE_CONFIG=$VERIFY_TMP/cfg-$name.toml
  cat >"$ADE_CONFIG" <<EOF
[user]
handle = "sandbox"
timezone = "UTC"
[forges.sandbox-gitlab]
kind = "gitlab"
url = "$FORGE_URL"
token_env = "FAKE_GITLAB_TOKEN"
git_protocol = "https"
[update]
on_start = false
[paths]
root = "$VERIFY_TMP/projects"
[[projects]]
source = "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
[ai]
mode = "$mode"
profile = "claude-sub"
[ai.profiles.claude-sub]
driver = "omp"
command = "$omp"
models = { explain = "claude-haiku-4-5", draft = "claude-sonnet-5", review = "claude-sonnet-5" }
billing = { source = "omp", weekly_soft_usd = 10 }
[ai.profiles.claude-sub.prices.claude-haiku-4-5]
input = 1.0
output = 5.0
[ai.profiles.claude-sub.prices.claude-sonnet-5]
input = 3.0
output = 15.0
[ai.profiles.gateway]
driver = "openai-compat"
base_url = "$GW_URL/v1"
key_env = "FAKE_GITLAB_TOKEN"
models = { explain = "mini", draft = "sonnet" }
billing = { source = "$GW_BILLING" }
[budget]
daily_usd = $daily
weekly_usd = 8.00
confirm_over_usd = 0.25
EOF
  export ADE_CONFIG
  cat >"$VERIFY_TMP/run.sh" <<EOF
#!/usr/bin/env bash
export ADE_CONFIG="$ADE_CONFIG" XDG_DATA_HOME="$XDG_DATA_HOME" XDG_CACHE_HOME="$XDG_CACHE_HOME" FAKE_GITLAB_TOKEN=fake-token
\${NOCOLOR:+export NO_COLOR=1}
exec "$ADE" dash
EOF
  chmod +x "$VERIFY_TMP/run.sh"
}

# seed_ai: a week of synthetic AI traffic in the audit log: two profiles, paid
# calls and cache hits, two budget stops, review drafts with their triage, and
# the ordinary forge/git lines around them. Today's claude-sub spend is $0.85.
seed_ai() {
  local dir=$XDG_DATA_HOME/ade/audit n=0 now
  mkdir -p "$dir"
  now=$(date -u +%s)
  ent() { # AGO_MIN ACTION ITEM OUTCOME PROFILE MODEL COST HIT IN OUT [ERROR] [PARAMS_JSON] [TARGET]
    local ts file id
    n=$((n + 1))
    ts=$((now - $1 * 60))
    id=$(printf '01SEED%020d' "$n")
    file=$dir/$(date -u -d "@$ts" +%Y-%m).jsonl
    jq -nc --arg id "$id" --arg ts "$(date -u -d "@$ts" +%Y-%m-%dT%H:%M:%SZ)" --arg a "$2" --arg item "$3" \
      --arg out "$4" --arg p "${5:-}" --arg m "${6:-}" --argjson cost "${7:-0}" --argjson hit "${8:-false}" \
      --argjson tin "${9:-0}" --argjson tout "${10:-0}" --arg err "${11:-}" --argjson params "${12:-null}" --arg target "${13:-}" '
      {id:$id, ts:$ts, actor:"user", action:$a, target:(if $target != "" then $target else $item end), item:$item, outcome:$out}
      + (if $err != "" then {error:$err} else {} end)
      + (if $params != null then {params:$params} else {} end)
      + (if $p != "" then {ai:{profile:$p, driver:(if $p == "gateway" then "openai-compat" else "omp" end), model:$m,
          tokens_in:$tin, tokens_out:$tout, cost_usd:$cost,
          cost_source:(if $p == "gateway" then "gateway" else "reported" end), cache_hit:$hit,
          artifact:("items/sandbox-gitlab/" + $item + "/ai/summary.md"),
          dest_host:(if $p == "gateway" then "llm.example.com" else "omp:anthropic" end), bytes:($tin * 4)}} else {} end)' >>"$file"
  }
  local G=ade-demo-env
  # Today, claude-sub: $0.85 across summaries, a review pass, a classify, a reply.
  ent 12 ai.summarize "$G/api!1" ok claude-sub claude-haiku-4-5 0.0052 false 1840 160
  ent 11 ai.summarize "$G/api!1" ok claude-sub claude-haiku-4-5 0 true 0 0
  ent 20 ai.draft-reply "$G/api!3" ok claude-sub claude-sonnet-5 0.0600 false 3800 340
  ent 55 ai.review "$G/api!1" ok claude-sub claude-sonnet-5 0.3410 false 21800 1450
  ent 80 ai.review "$G/api!2" ok claude-sub claude-sonnet-5 0.3120 false 19900 1320
  ent 95 ai.draft-reply "$G/api!1" ok claude-sub claude-sonnet-5 0.0780 false 4200 380
  ent 130 ai.classify "$G/tracker#11" ok claude-sub claude-haiku-4-5 0.0021 false 640 52
  ent 150 ai.summarize "$G/web!1" ok claude-sub claude-haiku-4-5 0.0067 false 2310 190
  ent 170 ai.summarize "$G/web!1" ok claude-sub claude-haiku-4-5 0 true 0 0
  ent 200 ai.draft-reply "$G/api!2" ok claude-sub claude-sonnet-5 0.0450 false 2900 260
  # Earlier days.
  local d
  for d in 1 2 3 4 5 6; do
    ent $((d * 1440 + 100)) ai.summarize "$G/api!$((d % 3 + 1))" ok claude-sub claude-haiku-4-5 "0.00$((d + 3))" false 1700 150
    ent $((d * 1440 + 160)) ai.review "$G/api!$((d % 2 + 1))" ok claude-sub claude-sonnet-5 "0.$((d * 41 % 60 + 20))" false 20000 1400
    ent $((d * 1440 + 220)) ai.summarize "$G/web!1" ok claude-sub claude-haiku-4-5 0 true 0 0
  done
  # gateway profile: cheap mini calls, one budget stop from the provider.
  ent 300 ai.summarize "$G/api!2" ok gateway mini 0.0011 false 1500 120
  ent 310 ai.classify "$G/tracker#6" ok gateway mini 0.0004 false 600 40
  ent 2900 ai.summarize "$G/api!1" ok gateway mini 0.0012 false 1500 120
  ent 2880 budget.stop "$G/api!1" error "" "" 0 false 0 0 "Budget has been exceeded! Key=ade-team Current cost: 50.02, Max budget: 50" \
    '{"feature":"review","profile":"gateway","model":"sonnet","source":"gateway"}'
  ent 1500 budget.stop "$G/web!1" error "" "" 0 false 0 0 "daily cap reached: \$1.98 spent today + ~\$0.12 for this call is over budget.daily_usd \$2.00" \
    '{"feature":"review","profile":"claude-sub","model":"claude-sonnet-5","source":"local"}'
  # Review drafts and what was done with them: claude-sub mostly accepted,
  # gateway mostly denied.
  local i
  for i in $(seq 1 12); do
    ent $((60 + i)) review.comment.draft "$G/api!1" ok "" "" 0 false 0 0 "" '{"profile":"claude-sub","severity":"suggestion"}' "dr-sub-$i"
  done
  for i in 1 2 3 4 5 6 7; do ent $((40 + i)) review.comment.accept "$G/api!1" ok "" "" 0 false 0 0 "" "" "dr-sub-$i"; done
  for i in 8 9; do ent $((30 + i)) review.comment.edit "$G/api!1" ok "" "" 0 false 0 0 "" "" "dr-sub-$i"; done
  for i in 10 11; do ent $((20 + i)) review.comment.deny "$G/api!1" ok "" "" 0 false 0 0 "" "" "dr-sub-$i"; done
  for i in $(seq 1 6); do
    ent $((3000 + i)) review.comment.draft "$G/api!2" ok "" "" 0 false 0 0 "" '{"profile":"gateway","severity":"nit"}' "dr-gw-$i"
  done
  ent 2990 review.comment.accept "$G/api!2" ok "" "" 0 false 0 0 "" "" "dr-gw-1"
  for i in 2 3 4 5; do ent $((2980 - i)) review.comment.deny "$G/api!2" ok "" "" 0 false 0 0 "" "" "dr-gw-$i"; done
  # The ordinary lines around them.
  ent 40 review.submit "$G/api!1" ok "" "" 0 false 0 0 "" '{"uploaded":9,"published":9}'
  ent 38 forge.draftnote.publish "$G/api!1" ok
  ent 90 forge.thread.reply "$G/api!1" ok "" "" 0 false 0 0 "" '{"chars":84}' "d4f0a1"
  ent 125 forge.issue.labels "$G/tracker#6" ok "" "" 0 false 0 0 "" '{"add":["type::chore"],"reason":"classify (keywords)"}'
  ent 240 git.pull.failed "demo-gl/api" error "" "" 0 false 0 0 "fetch origin: could not resolve host"
  ent 400 budget.override "$G/api!2" ok "" "" 0 false 0 0 "" '{"feature":"review","profile":"claude-sub","reason":"daily cap"}'
  SEED_LINES=$n
}

# reseed_ai: the audit log back to exactly the seed. Each `ade dash` run adds
# entries of its own, so a tape that shows entry counts starts from this
# (BEFORE_CAPTURE=reseed_ai), and every set shows the same counts.
reseed_ai() {
  rm -rf "$XDG_DATA_HOME/ade/audit"
  seed_ai
  local got
  got=$(cat "$XDG_DATA_HOME"/ade/audit/*.jsonl | wc -l)
  [[ $got -eq $SEED_LINES ]] || fail "reseed wrote $got audit lines, want $SEED_LINES"
}

# capture NAME FONT NOCOLOR BODY: one VHS tape. BODY holds the tape lines that follow the first frame;
# Screenshot paths use "$OUT/$PREFIX-$NAME-<state>.png". $BEFORE_CAPTURE runs before each attempt
# (reset what a tape changes), $AFTER_CAPTURE after it.
capture() {
  local name=$1 font=$2 nc=$3 body=$4
  cat >"$VERIFY_TMP/$name.tape" <<EOF
Output "$VERIFY_TMP/$name.gif"
Set Shell bash
Set Width 1600
Set Height 900
Set FontSize $font
Set Padding 0
Set TypingSpeed 0
Env COLORTERM "truecolor"
Env NOCOLOR "$nc"
Hide
Type "$VERIFY_TMP/run.sh"
Enter
Show
Sleep 2500ms
$body
Sleep 300ms
EOF
  # VHS occasionally drops a screenshot or a whole tape: run it again once if
  # any screenshot is missing. Every PNG is removed before an attempt, so the
  # ones verified are all from one complete run. verify_shots (end of each
  # script) fails if any is still missing.
  local tape="$VERIFY_TMP/$name.tape" shots attempt shot missing
  mapfile -t shots < <(sed -n 's/^Screenshot "\(.*\)"$/\1/p' "$tape")
  ALL_SHOTS+=("${shots[@]}")
  for attempt in 1 2; do
    rm -f "${shots[@]}"
    [[ -z ${BEFORE_CAPTURE:-} ]] || "$BEFORE_CAPTURE"
    env -u NO_COLOR TERM=xterm-256color vhs "$tape" >/dev/null 2>&1 || true
    pkill -f "$ADE dash" || true
    [[ -z ${AFTER_CAPTURE:-} ]] || "$AFTER_CAPTURE"
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
  echo "${#ALL_SHOTS[@]} screenshots written"
}

# shot STATE: a tape fragment: pause, then screenshot as $PREFIX-$NAME-$STATE.
# (NAME is substituted by the caller via the variable SET_NAME.)
shot() { printf 'Sleep %s\nScreenshot "%s/%s-%s-%s.png"\n' "${2:-900ms}" "$OUT" "$PREFIX" "$SET_NAME" "$1"; }

# presses KEY N [GAP]: a tape fragment: KEY typed N times with a pause after
# each, so a repeated key is never swallowed as one (cycling a filter is
# only right when every press lands).
presses() {
  local i
  for ((i = 0; i < $2; i++)); do printf 'Type "%s"\nSleep %s\n' "$1" "${3:-150ms}"; done
}

# sets BODYFN: run BODYFN for each of the three sets (desktop 14pt, narrow 30pt, nocolor).
sets() {
  local fn=$1
  SET_NAME=desktop
  capture desktop 14 "" "$($fn)"
  SET_NAME=narrow
  capture narrow 30 "" "$($fn)"
  SET_NAME=nocolor
  capture nocolor 14 1 "$($fn)"
}
