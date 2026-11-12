#!/usr/bin/env bash
# Capture Home states the live sandbox cannot show, against the in-process fake
# GitLab (Premium, -agile -carried) with throwaway state. Output:
# .impeccable/review/home-{desktop,wide,narrow,nocolor}-<state>.png, states:
#   previous   the Previous sprint section (Sprint 41's unfinished issue)
#   search     / with a query that also finds open MRs on the forge
#   palette    : narrowed by typing
#   picker     f, the filter picker with type, app and workflow values
#   activity   (mid and narrow sets only) Activity expanded (an event injected with `ade notify send`)
#   load       the project rule: committed, done, left and your average
# The wide set (Width 2400) shows three regions: list | preview | rail.
. scripts/verify/lib.sh
build_ade
go build -o "$VERIFY_TMP/fakegl" ./scripts/verify/fakegl

PIDS=()
trap 'kill "${PIDS[@]}" 2>/dev/null || true; rm -rf "$VERIFY_TMP"' EXIT
"$VERIFY_TMP/fakegl" -tier premium -agile -carried >"$VERIFY_TMP/gl.url" 2>"$VERIFY_TMP/gl.err" &
PIDS+=($!)
for _ in $(seq 50); do
  [[ -s $VERIFY_TMP/gl.url ]] && break
  sleep 0.1
done
URL=$(head -1 "$VERIFY_TMP/gl.url")
[[ -n $URL ]] || fail "fakegl did not start: $(cat "$VERIFY_TMP/gl.err")"

DEMO="$VERIFY_TMP/ade-demo"
mkdir -p "$DEMO/projects"
export XDG_DATA_HOME="$DEMO/data" XDG_CACHE_HOME="$DEMO/cache" FAKE_GITLAB_TOKEN=fake-token
# Premium weights come from the weight field; the overlap shows Sprint 41 for the
# first ten working days of Sprint 42.
sed -e 's/^weight .*/weight     = { source = "gitlab" }/' \
  -e 's/^iterations = .*/iterations = { source = "milestones", overlap = 10 }/' \
  "$REPO/scripts/sandbox/manifests/gitlab.ade.toml" >"$DEMO/manifest.ade.toml"
cat >"$DEMO/config.toml" <<EOC
[user]
handle = "sandbox"
timezone = "UTC"
[forges.sandbox-gitlab]
kind = "gitlab"
url = "$URL"
token_env = "FAKE_GITLAB_TOKEN"
git_protocol = "https"
[update]
on_start = false
[paths]
root = "$DEMO/projects"
[[projects]]
source = "$DEMO/manifest.ade.toml"
EOC
export ADE_CONFIG="$DEMO/config.toml"
"$ADE" sync >/dev/null || fail "ade sync against the fake failed"
"$ADE" notify send --kind ci.failed --severity error --project demo-gl \
  --subject "ade-demo-env/tracker#2" --title "web!1 pipeline failed: lint" >/dev/null
"$ADE" notify send --kind mr.merged --severity success --project demo-gl \
  --subject "ade-demo-env/api!2" --title "api!2 merged" >/dev/null

cat >"$VERIFY_TMP/run.sh" <<EOF
#!/usr/bin/env bash
export ADE_CONFIG="$ADE_CONFIG" XDG_DATA_HOME="$XDG_DATA_HOME" XDG_CACHE_HOME="$XDG_CACHE_HOME" FAKE_GITLAB_TOKEN=fake-token
\${NOCOLOR:+export NO_COLOR=1}
exec "$ADE" dash
EOF
chmod +x "$VERIFY_TMP/run.sh"

OUT="$REPO/.impeccable/review"
mkdir -p "$OUT"
rm -f "$OUT"/home-*.png

tape() { # set fontsize nocolor width
  local set=$1 font=$2 nc=$3 width=$4
  cat >"$VERIFY_TMP/$set.tape" <<EOF
Output "$VERIFY_TMP/$set.gif"
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
Wait+Screen@60s /Sprint 42/
Sleep 3s
Screenshot "$OUT/home-$set-load.png"
Type "G"
Sleep 500ms
Screenshot "$OUT/home-$set-previous.png"
Type "g"
Type "/"
Type "export"
Sleep 2500ms
Screenshot "$OUT/home-$set-search.png"
Escape
Sleep 300ms
Type ":"
Type "sprint"
Sleep 500ms
Screenshot "$OUT/home-$set-palette.png"
Escape
Sleep 300ms
Type "f"
Sleep 500ms
Screenshot "$OUT/home-$set-picker.png"
Escape
Sleep 300ms
EOF
  # Activity is a section of the list below the three-region width only; there
  # the rail always shows it.
  case $set in
    narrow | mid | midnocolor) printf 'Type "e"\nSleep 600ms\nScreenshot "%s/home-%s-activity.png"\n' "$OUT" "$set" >>"$VERIFY_TMP/$set.tape" ;;
  esac
}

ALL_SHOTS=()
capture() {
  local set=$1 shots attempt shot missing
  tape "$@"
  mapfile -t shots < <(sed -n 's/^Screenshot "\(.*\)"$/\1/p' "$VERIFY_TMP/$set.tape")
  ALL_SHOTS+=("${shots[@]}")
  for attempt in 1 2; do
    env -u NO_COLOR TERM=xterm-256color vhs "$VERIFY_TMP/$set.tape" >/dev/null 2>&1 || true
    pkill -f "$ADE dash" || true
    missing=0
    for shot in "${shots[@]}"; do [[ -s $shot ]] || missing=1; done
    ((missing == 0)) && break
  done
}

capture desktop 14 "" 1600
capture wide 14 "" 2400
capture narrow 30 "" 1600
capture nocolor 14 1 1600
capture mid 14 "" 1000
capture midnocolor 14 1 1000

bad=0
for shot in "${ALL_SHOTS[@]}"; do
  [[ -s $shot ]] || { echo "MISSING: $shot" >&2; bad=1; }
done
((bad == 0)) || exit 1
ls "$OUT"/home-*.png
