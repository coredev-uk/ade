#!/usr/bin/env bash
# Capture the MR page and the review surface for visual review with VHS.
# Output: .impeccable/review/mr-{desktop,narrow,nocolor}-<state>.png
#
#   scripts/verify/capture-mr.sh
#
# States per set:
#   populated  overview, threads, thread-open (a thread with a reply), reply (the
#              r editor), thread-diff (a thread on a diff line, with the lines
#              around it), empty-review, diff, comment, diff-drafts, review,
#              submit, merge (refused), history, help, empty-threads, ci, ci-log
#   agent      agent-diff-files, agent-diff (agent draft blocks), agent-review
#              (pending, y n e keys), agent-review-long, agent-review-denied,
#              agent-submit (pending and denied lines); drafts seeded by
#              scripts/verify/seeddrafts, no AI call
#   rename     rename-home (Reviews requested), rename-overview, rename-threads,
#              rename-diff (a renamed file), rename-comment-old (comment on a
#              removed line), rename-diff-drafts, rename-review
#   loading    loading-overview (first read of the MR), loading-threads,
#              loading-diff, loading-ci (a fake that answers after 3 s)
#   error      error-overview, error-threads (the forge is unreachable)
#   confirm    confirm-merge (squash, delete branch)
#   ai         ai-summary, ai-summary-raw (d), ai-thread-summary (s), ai-draft-reply
#              (d on a thread: in the reply editor), ai-review-hint, ai-agent-done
#              (A: drafts pending in Review), ai-agent-diff, ai-help, palette (`:` on the page), ai-thread-open; ai-summary-running,
#              ai-agent-running, ai-summary-late, ai-agent-done-slow (a 5 s stub, the page
#              stays usable); ai-off-* (ai.mode = off: no A, no d)
#
# The data comes from the in-process fake GitLab (scripts/verify/fakegl), never the
# live forge: the sandbox has no CI and its threads change with every run. The
# fake has a failed lint job, the renamed file in api!2, and approval rules
# (Premium). Nothing is ever posted: the tapes add drafts and open dialogs, then
# quit. Synthetic paths only.
. scripts/verify/lib.sh
build_ade
go build -o "$VERIFY_TMP/fakegl" ./scripts/verify/fakegl
go build -o "$VERIFY_TMP/seeddrafts" ./scripts/verify/seeddrafts
# omp stubs for the AI states: scripts/verify/fakeomp answers each feature from canned text
# (nothing is spent); omp-slow takes 5 s per answer to catch the running states.
printf '#!/bin/sh\nexec "%s/scripts/verify/fakeomp" "$@"\n' "$REPO" >"$VERIFY_TMP/omp-ok"
printf '#!/bin/sh\nFAKEOMP_DELAY=5 exec "%s/scripts/verify/fakeomp" "$@"\n' "$REPO" >"$VERIFY_TMP/omp-slow"
chmod +x "$VERIFY_TMP/omp-ok" "$VERIFY_TMP/omp-slow"

FAKE_PIDS=()
trap 'kill "${FAKE_PIDS[@]}" 2>/dev/null || true; rm -rf "$VERIFY_TMP"' EXIT

# start_fake NAME ARGS...: run fakegl, leave its URL in URL_<NAME>.
start_fake() {
  local name=$1
  shift
  "$VERIFY_TMP/fakegl" "$@" >"$VERIFY_TMP/$name.url" 2>"$VERIFY_TMP/$name.err" &
  FAKE_PIDS+=($!)
  for _ in $(seq 50); do
    [[ -s $VERIFY_TMP/$name.url ]] && break
    sleep 0.1
  done
  [[ -s $VERIFY_TMP/$name.url ]] || fail "fake $name did not start: $(cat "$VERIFY_TMP/$name.err")"
  printf -v "URL_$name" '%s' "$(head -1 "$VERIFY_TMP/$name.url")"
}
REVIEWS="ade-demo-env/api!2" # the renamed-file MR is requested of the signed-in user
REVIEWS_API1="ade-demo-env/api!1" # tape_agent seeds the agent's drafts here
start_fake premium -tier premium -review "$REVIEWS"
start_fake free -tier free -review "$REVIEWS"
start_fake slow -tier premium -review "$REVIEWS" -latency 3s
DEAD_URL=http://127.0.0.1:9 # nothing listens: connection refused

export XDG_DATA_HOME="$VERIFY_TMP/data" XDG_CACHE_HOME="$VERIFY_TMP/cache"
export ADE_CONFIG="$VERIFY_TMP/config.toml" FAKE_GITLAB_TOKEN=fake-token
use_url() { # URL: point the config's forge at it
  cat >"$ADE_CONFIG" <<EOF
[user]
handle = "sandbox"
[forges.sandbox-gitlab]
kind = "gitlab"
url = "$1"
token_env = "FAKE_GITLAB_TOKEN"
git_protocol = "https"
[update]
on_start = false
[paths]
root = "/tmp/ade-demo"
[[projects]]
source = "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
EOF
  if [[ -n ${AI_CMD:-} ]]; then
    cat >>"$ADE_CONFIG" <<EOF
[ai]
mode = "${AI_MODE:-auto}"
profile = "stub"
[ai.profiles.stub]
driver = "omp"
command = "$AI_CMD"
models = { explain = "claude-haiku-4-5", draft = "claude-haiku-4-5", review = "claude-haiku-4-5" }
billing = { source = "omp", weekly_soft_usd = 10 }
[ai.profiles.stub.prices.claude-haiku-4-5]
input = 1.0
output = 5.0
[budget]
daily_usd = 5.0
EOF
  fi
}

OUT="$REPO/.impeccable/review"
mkdir -p "$OUT"
[[ -n ${CAPTURE_ONLY:-} ]] || rm -f "$OUT"/mr-*.png

cat >"$VERIFY_TMP/run.sh" <<EOF
#!/usr/bin/env bash
export ADE_CONFIG="$ADE_CONFIG" XDG_DATA_HOME="$XDG_DATA_HOME" XDG_CACHE_HOME="$XDG_CACHE_HOME" FAKE_GITLAB_TOKEN=fake-token
\${NOCOLOR:+export NO_COLOR=1}
exec "$ADE" dash
EOF
chmod +x "$VERIFY_TMP/run.sh"

# preamble NAME FONT NC: the tape header; Home is up when it ends.
preamble() {
  cat <<EOF
Output "$VERIFY_TMP/$1.gif"
Set Shell bash
Set Width 1600
Set Height 900
Set FontSize $2
Set Padding 0
Set TypingSpeed 0
Env COLORTERM "truecolor"
Env NOCOLOR "$3"
Hide
Type "$VERIFY_TMP/run.sh"
Enter
Show
Sleep 2500ms
EOF
}

# shot SET STATE: screenshot with time for the frame to settle. Escape is always
# followed by a pause: a key arriving at once would read as Alt+key.
shot() { printf 'Sleep 400ms\nScreenshot "%s/mr-%s-%s.png"\n' "$OUT" "$1" "$2"; }
esc() { printf 'Escape\nSleep 700ms\n'; }

# Home rows: tracker#1 (MR api!1) first, tracker#2 (web!1, failed lint) second,
# api!2 (renamed file, requested of you) the eighth row, after seven work items.
tape_main() { # SET
  local s=$1
  cat <<EOF
Enter
Sleep 2500ms
$(shot "$s" overview)
Type "2"
Sleep 1200ms
$(shot "$s" threads)
Type "j"
Enter
Sleep 300ms
$(shot "$s" thread-open)
Type "r"
Sleep 300ms
Type "Agreed. I will note the support agreement in the ADR."
$(shot "$s" reply)
$(esc)
Type "j"
Sleep 300ms
$(shot "$s" thread-diff)
Type "h"
Type "4"
Sleep 600ms
$(shot "$s" empty-review)
Type "3"
Sleep 1500ms
$(shot "$s" diff)
Enter
Type "jj"
Type "c"
Type "Says continuously, but Allow only checks Tokens."
Tab
$(shot "$s" comment)
Enter
Type "jjj"
Type "c"
Type "Use an int64 count of milli-tokens to avoid float drift."
Tab
Tab
Enter
$(shot "$s" diff-drafts)
Type "4"
Sleep 600ms
$(shot "$s" review)
Type "S"
Type "Two small things, otherwise good."
$(shot "$s" submit)
$(esc)
Type "m"
Sleep 800ms
$(shot "$s" merge)
$(esc)
Type "6"
Sleep 800ms
$(shot "$s" history)
Type "?"
Sleep 600ms
$(shot "$s" help)
$(esc)
$(esc)
Type "j"
Sleep 400ms
Enter
Sleep 2500ms
Type "2"
Sleep 800ms
$(shot "$s" empty-threads)
Type "5"
Sleep 1500ms
$(shot "$s" ci)
Type "jl"
Sleep 1500ms
$(shot "$s" ci-log)
EOF
}

# tape_agent: api!1 with the agent's drafts seeded (seeddrafts): a pending
# blocking one, a pending one over the house limit, an accepted one, a denied
# one, plus your own overview note. Nothing is accepted, denied or submitted.
tape_agent() { # SET
  local s=$1
  cat <<EOF
Enter
Sleep 2500ms
Type "3"
Sleep 1500ms
$(shot "$s" agent-diff-files)
Enter
Type "jjjj"
$(shot "$s" agent-diff)
Type "4"
Sleep 600ms
$(shot "$s" agent-review)
Type "j"
$(shot "$s" agent-review-long)
Type "jjj"
$(shot "$s" agent-review-denied)
Type "S"
Sleep 400ms
$(shot "$s" agent-submit)
EOF
}

tape_rename() { # SET
  local s=$1
  cat <<EOF
Type "jjjjjjj"
$(shot "$s" rename-home)
Enter
Sleep 2500ms
$(shot "$s" rename-overview)
Type "2"
Sleep 1200ms
$(shot "$s" rename-threads)
Type "3"
Sleep 1500ms
$(shot "$s" rename-diff)
Enter
Type "jj"
Type "c"
Sleep 400ms
Type "Why drop the old doc line?"
$(shot "$s" rename-comment-old)
Enter
Type "jjjjjjjjj"
Type "c"
Type "Allow never consumes a token."
Tab
Enter
$(shot "$s" rename-diff-drafts)
Type "4"
Sleep 600ms
$(shot "$s" rename-review)
EOF
}

tape_loading() { # SET: the fake answers after 3 s; each tab is a first read, nothing cached
  local s=$1
  cat <<EOF
Enter
Sleep 700ms
$(shot "$s" loading-overview)
Type "2"
Sleep 500ms
$(shot "$s" loading-threads)
Type "3"
Sleep 300ms
$(shot "$s" loading-diff)
Type "5"
Sleep 300ms
$(shot "$s" loading-ci)
EOF
}

tape_error() { # SET: nothing answers
  local s=$1
  cat <<EOF
Enter
Sleep 2500ms
$(shot "$s" error-overview)
Type "2"
Sleep 1500ms
$(shot "$s" error-threads)
EOF
}

tape_confirm() { # SET: a mergeable MR (Free tier)
  local s=$1
  cat <<EOF
Enter
Sleep 2500ms
Type "m"
Sleep 600ms
Type "sd"
$(shot "$s" confirm-merge)
EOF
}

# tape_ai: api!2 with AI on (an omp stub that answers at once): the Overview
# summary and its raw-description toggle, a thread's summary, the draft reply
# in the reply editor (never posted), and the finished agent review pass.
tape_ai() { # SET
  local s=$1
  cat <<EOF
Type "jjjjjjj"
Enter
Sleep 3500ms
$(shot "$s" ai-summary)
Type "d"
Sleep 400ms
$(shot "$s" ai-summary-raw)
Type "d"
Type "2"
Sleep 1000ms
Type "s"
Sleep 1800ms
$(shot "$s" ai-thread-summary)
Type "d"
Sleep 1800ms
$(shot "$s" ai-draft-reply)
$(esc)
Enter
Sleep 600ms
$(shot "$s" ai-thread-open)
$(esc)
Type "4"
Sleep 600ms
$(shot "$s" ai-review-hint)
Type "A"
Sleep 2500ms
$(shot "$s" ai-agent-done)
Type "3"
Sleep 800ms
Enter
Type "jj"
$(shot "$s" ai-agent-diff)
Type "?"
Sleep 500ms
$(shot "$s" ai-help)
$(esc)
Type ":"
Type "reply"
Sleep 500ms
$(shot "$s" palette)
EOF
}

# tape_ai_slow: the stub takes 5 s: the summary and the agent pass while they
# run (the page keeps answering keys), then the pass finishing.
tape_ai_slow() { # SET
  local s=$1
  cat <<EOF
Type "jjjjjjj"
Enter
Sleep 2500ms
$(shot "$s" ai-summary-running)
Type "4"
Type "A"
Sleep 1200ms
$(shot "$s" ai-agent-running)
Type "1"
Sleep 6000ms
$(shot "$s" ai-summary-late)
Type "4"
Sleep 6000ms
$(shot "$s" ai-agent-done-slow)
EOF
}

# tape_ai_off: ai.mode = off: raw description only, no A, no d, no AI hints.
tape_ai_off() { # SET
  local s=$1
  cat <<EOF
Type "jjjjjjj"
Enter
Sleep 2500ms
$(shot "$s" ai-off-overview)
Type "4"
Sleep 500ms
$(shot "$s" ai-off-review)
Type "?"
Sleep 500ms
$(shot "$s" ai-off-help)
EOF
}

# capture SET FONT NC TAPE SYNC_URL RUN_URL [SEED_MR]: sync a clean cache
# against one fake, then run the dashboard against another (the slow or dead
# one). SEED_MR stores the agent's review drafts on that MR before the run.
capture() {
  local set=$1 font=$2 nc=$3 fn=$4 sync_url=$5 run_url=$6 seed=${7:-}
  [[ -z ${CAPTURE_ONLY:-} || " $CAPTURE_ONLY " == *" $fn "* ]] || return 0 # e.g. CAPTURE_ONLY="tape_error tape_loading"
  rm -f "$XDG_CACHE_HOME"/ade/cache.db*
  use_url "$sync_url"
  "$ADE" sync >/dev/null
  [[ -z $seed ]] || "$VERIFY_TMP/seeddrafts" "$XDG_CACHE_HOME/ade/cache.db" "$seed" >/dev/null
  use_url "$run_url"
  {
    preamble "$set-$fn" "$font" "$nc"
    "$fn" "$set"
    echo 'Sleep 500ms'
  } >"$VERIFY_TMP/$set-$fn.tape"
  env -u NO_COLOR TERM=xterm-256color vhs "$VERIFY_TMP/$set-$fn.tape" >/dev/null 2>&1 || true
  pkill -f "$ADE dash" || true
}

for spec in "desktop 14 " "narrow 30 " "nocolor 14 1"; do
  read -r set font nc <<<"$spec"
  capture "$set" "$font" "$nc" tape_main "$URL_premium" "$URL_premium"
  capture "$set" "$font" "$nc" tape_agent "$URL_premium" "$URL_premium" "$REVIEWS_API1"
  capture "$set" "$font" "$nc" tape_rename "$URL_premium" "$URL_premium"
  capture "$set" "$font" "$nc" tape_loading "$URL_premium" "$URL_slow"
  capture "$set" "$font" "$nc" tape_error "$URL_premium" "$DEAD_URL"
  capture "$set" "$font" "$nc" tape_confirm "$URL_free" "$URL_free"
  # AI on the page: summary, thread summary, draft reply, agent pass (an omp stub, nothing is spent)
  AI_CMD=$VERIFY_TMP/omp-ok capture "$set" "$font" "$nc" tape_ai "$URL_premium" "$URL_premium"
  AI_CMD=$VERIFY_TMP/omp-slow capture "$set" "$font" "$nc" tape_ai_slow "$URL_premium" "$URL_premium"
  AI_CMD=$VERIFY_TMP/omp-ok AI_MODE=off capture "$set" "$font" "$nc" tape_ai_off "$URL_premium" "$URL_premium"
done
ls "$OUT"/mr-*.png
