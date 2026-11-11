#!/usr/bin/env bash
# Capture the dashboard for visual review with VHS, from clean, isolated state
# seeded by one sync of the GitLab sandbox. Output: .impeccable/review/*.png
#
#   scripts/verify/capture.sh            # desktop, narrow, no-colour sets
#
# Each set: first frame, a quiet Home (<set>.png), drift selection, the filter
# picker (f) and the applied filter, search (/), the palette (:), Activity (e),
# a ci.failed pop-up injected with `ade notify send` after the filter frame, the
# notification centre, the Worktrees page, the help dialog (`?` on Home). The
# wide set (2400 px: three regions, list | preview | activity and services)
# takes the same walk. The Previous sprint section needs a sprint that just
# ended with unfinished work, which the live sandbox does not have: it is
# captured against the fake forge by capture-home.sh. A missing screenshot
# reruns the set once, then fails the script.
. scripts/verify/lib.sh
build_ade
sandbox_gitlab
OUT="$REPO/.impeccable/review"
mkdir -p "$OUT"
rm -f "$OUT"/{desktop,narrow,nocolor,wide}*.png # Home's sets only; other surfaces own their prefixes

# Run under a short synthetic root so paths in the screenshots read cleanly.
DEMO=/tmp/ade-demo
rm -rf "$DEMO"
mkdir -p "$DEMO/data" "$DEMO/cache" "$DEMO/projects"
trap 'rm -rf "$VERIFY_TMP" "$DEMO"' EXIT
export XDG_DATA_HOME="$DEMO/data" XDG_CACHE_HOME="$DEMO/cache"
sed -i "s|^root = .*|root = \"$DEMO/projects\"|" "$ADE_CONFIG"
"$ADE" sync >/dev/null

cat >"$VERIFY_TMP/run.sh" <<EOF
#!/usr/bin/env bash
export ADE_CONFIG="$ADE_CONFIG" XDG_DATA_HOME="$XDG_DATA_HOME" XDG_CACHE_HOME="$XDG_CACHE_HOME" GITLAB_PAT="$GITLAB_PAT"
\${NOCOLOR:+export NO_COLOR=1}
exec "$ADE" dash
EOF
chmod +x "$VERIFY_TMP/run.sh"

tape() { # name fontsize nocolor [width]
  local name=$1 font=$2 nc=$3 width=${4:-1600}
  cat >"$VERIFY_TMP/$name.tape" <<EOF
Output "$VERIFY_TMP/$name.gif"
Set Shell bash
Set Width $width
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
Sleep 1500ms
Screenshot "$OUT/$name-first-frame.png"
# The dash syncs the live sandbox on start, which can take a minute: the base
# frame waits for the header to read idle ("synced Ns ago").
Wait+Screen@240s /synced [0-9]+s ago/
Sleep 500ms
Screenshot "$OUT/$name.png"
Type "j"
Sleep 400ms
Screenshot "$OUT/$name-drift.png"
Type "f"
Sleep 400ms
Screenshot "$OUT/$name-filter-picker.png"
Type "blocked"
Sleep 200ms
Enter
Sleep 400ms
Screenshot "$OUT/$name-filter.png"
Type "f"
Sleep 200ms
Type "all"
Enter
Sleep 12s
Screenshot "$OUT/$name-popup.png"
Type "i"
Sleep 700ms
Screenshot "$OUT/$name-centre.png"
Escape
Sleep 300ms
Escape
Sleep 300ms
Type "t"
Sleep 700ms
Screenshot "$OUT/$name-worktrees.png"
Sleep 300ms
Type "t"
Sleep 500ms
Type "?"
Sleep 600ms
Screenshot "$OUT/$name-help.png"
Sleep 300ms
Escape
Sleep 300ms
Type "e"
Sleep 500ms
Screenshot "$OUT/$name-activity.png"
Type "e"
Sleep 200ms
Type "/"
Type "rate"
Sleep 2500ms
Screenshot "$OUT/$name-search.png"
Escape
Sleep 300ms
Type ":"
Type "sprint"
Sleep 500ms
Screenshot "$OUT/$name-palette.png"
Escape
Sleep 300ms
EOF
}

# sync_runs counts the audit log's sync.run entries: the forge sync appends one
# when it finishes, so a higher count means the dash's first sync is done.
sync_runs() { cat "$XDG_DATA_HOME"/ade/audit/*.jsonl 2>/dev/null | grep -c '"sync.run"' || true; }

# inject_when_synced waits for the dash's first sync to finish, then sends the
# ci.failed event. vhs sees "synced" a few seconds late, so the base, drift and
# filter frames come 5-8s after the sync and the tape holds a 12s gap before the
# pop-up frame: an event 10s after the sync lands inside that gap.
inject_when_synced() {
  local before
  before=$(sync_runs)
  for _ in $(seq 480); do
    sleep 0.5
    (($(sync_runs) > before)) && break
  done
  sleep 10
  "$ADE" notify send --kind ci.failed --severity error --project demo-gl \
    --subject "ade-demo-env/tracker#2" --title "web!1 pipeline failed: lint (eslint, 3 errors)" >/dev/null
}

ALL_SHOTS=()
capture() { # name fontsize nocolor [width]
  local name=$1 shots attempt shot missing
  tape "$@"
  mapfile -t shots < <(sed -n 's/^Screenshot "\(.*\)"$/\1/p' "$VERIFY_TMP/$name.tape")
  ALL_SHOTS+=("${shots[@]}")
  # VHS occasionally drops a screenshot; run the tape again once if any is missing.
  for attempt in 1 2; do
    inject_when_synced &
    env -u NO_COLOR TERM=xterm-256color vhs "$VERIFY_TMP/$name.tape" >/dev/null 2>&1 || true
    wait
    pkill -f "$ADE dash" || true
    "$ADE" inbox read --all >/dev/null # each set starts from the same unread state
    missing=0
    for shot in "${shots[@]}"; do [[ -s $shot ]] || missing=1; done
    ((missing == 0)) && break
  done
}

capture desktop 14 ""
capture narrow 30 ""
capture nocolor 14 1
capture wide 14 "" 2400

# Every screenshot a tape asked for must exist; otherwise exit 1.
bad=0
for shot in "${ALL_SHOTS[@]}"; do
  [[ -s $shot ]] || { echo "MISSING: $shot" >&2; bad=1; }
done
((bad == 0)) || exit 1
ls "$OUT"
