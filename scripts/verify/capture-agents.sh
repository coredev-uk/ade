#!/usr/bin/env bash
# Capture the agent surfaces (menu with the run list, plan review and approve,
# run review with diff and push confirm, a run stopped at its cost cap, all
# runs) for visual review with VHS. Output: .impeccable/review/agent-*.png
#
#   scripts/verify/capture-agents.sh        # desktop, narrow, no-colour sets
#
# Data: the in-process fake GitLab (scripts/verify/fakegl) for the item and its
# MR with two unresolved threads, and runs seeded in every state by
# scripts/verify/fakeagents. No agent process is started and nothing is pushed:
# the tape only opens dialogs, approves one plan locally and backs out of push.
. scripts/verify/lib.sh
build_ade
go build -o "$VERIFY_TMP/fakegl" ./scripts/verify/fakegl
go build -o "$VERIFY_TMP/fakeagents" ./scripts/verify/fakeagents

"$VERIFY_TMP/fakegl" -tier premium >"$VERIFY_TMP/fake.url" 2>"$VERIFY_TMP/fake.err" &
FAKE_PID=$!
trap 'kill $FAKE_PID 2>/dev/null || true; rm -rf "$VERIFY_TMP"' EXIT
for _ in $(seq 50); do
  [[ -s $VERIFY_TMP/fake.url ]] && break
  sleep 0.1
done
FAKE_URL=$(head -1 "$VERIFY_TMP/fake.url")
[[ -n $FAKE_URL ]] || fail "the fake GitLab did not start: $(cat "$VERIFY_TMP/fake.err")"

export XDG_DATA_HOME="$VERIFY_TMP/data" XDG_CACHE_HOME="$VERIFY_TMP/cache"
export ADE_CONFIG="$VERIFY_TMP/config.toml" FAKE_GITLAB_TOKEN=fake-token
cat >"$ADE_CONFIG" <<EOT
[user]
handle = "sandbox"
[forges.sandbox-gitlab]
kind = "gitlab"
url = "$FAKE_URL"
token_env = "FAKE_GITLAB_TOKEN"
git_protocol = "https"
[update]
on_start = false
[paths]
root = "$VERIFY_TMP/projects"
[[projects]]
source = "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
EOT

OUT="$REPO/.impeccable/review"
mkdir -p "$OUT"
rm -f "$OUT"/agent-*.png
"$ADE" sync >/dev/null

cat >"$VERIFY_TMP/run.sh" <<EOT
#!/usr/bin/env bash
export ADE_CONFIG="$ADE_CONFIG" XDG_DATA_HOME="$XDG_DATA_HOME" XDG_CACHE_HOME="$XDG_CACHE_HOME" FAKE_GITLAB_TOKEN=fake-token
\${NOCOLOR:+export NO_COLOR=1}
exec "$ADE" dash
EOT
chmod +x "$VERIFY_TMP/run.sh"

# tape NAME FONTSIZE NOCOLOR: the first Home row is tracker#1. Runs list newest
# first: running, review, awaiting approval, failed, pushed.
tape() {
  local name=$1 font=$2 nc=$3
  cat >"$VERIFY_TMP/$name.tape" <<EOT
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
Type "j"
Sleep 300ms
Type "a"
Sleep 800ms
Screenshot "$OUT/agent-$name-empty.png"
Escape
Sleep 300ms
Type "k"
Sleep 300ms
Type "a"
Sleep 1200ms
Screenshot "$OUT/agent-$name-menu.png"
Type "jj"
Enter
Sleep 800ms
Screenshot "$OUT/agent-$name-plan.png"
Enter
Sleep 600ms
Screenshot "$OUT/agent-$name-plan-approved.png"
Escape
Sleep 400ms
Type "k"
Enter
Sleep 800ms
Screenshot "$OUT/agent-$name-review.png"
Type "P"
Sleep 500ms
Screenshot "$OUT/agent-$name-push.png"
Type "n"
Sleep 300ms
Escape
Sleep 400ms
Type "jj"
Enter
Sleep 800ms
Screenshot "$OUT/agent-$name-failed.png"
Escape
Sleep 400ms
Escape
Sleep 400ms
Type "A"
Sleep 1000ms
Screenshot "$OUT/agent-$name-all.png"
Sleep 300ms
EOT
}

capture() { # name fontsize nocolor
  # Every set starts from the same runs: the tape approves a plan locally.
  rm -rf "$XDG_DATA_HOME"/ade/items/*/*/*/*/runs "$XDG_DATA_HOME"/ade/items/*/*/*/runs 2>/dev/null || true
  rm -f "$XDG_CACHE_HOME"/ade/cache.db*
  "$ADE" sync >/dev/null
  SEED=$("$VERIFY_TMP/fakeagents") || fail "seeding runs failed"
  tape "$@"
  env -u NO_COLOR TERM=xterm-256color vhs "$VERIFY_TMP/$1.tape" >/dev/null 2>&1 || true
  pkill -f "$ADE dash" || true
  git -C "$SEED/api" worktree prune 2>/dev/null || true
  rm -rf "$SEED"
}

capture desktop 14 ""
capture narrow 30 ""
capture nocolor 14 1
ls "$OUT"/agent-*.png
