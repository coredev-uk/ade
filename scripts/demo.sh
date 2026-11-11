#!/usr/bin/env bash
# Interactive ADE demo: the real dashboard against a seeded fake GitLab
# (Premium tier, sprint history, review request, MR template) and a stub AI
# that answers from canned text. Nothing touches a real forge, your config,
# or any paid service; all state lives in a temp dir removed on exit.
#
#   nix develop -c bash scripts/demo.sh            # drive it yourself
#   nix develop -c bash scripts/demo.sh --record   # also write demo.gif (VHS tour)
#
# Start work is real end to end: the fake serves git remotes too (file://
# bare repos with the sandbox apps), so branches, worktrees and workspaces
# work. The live sandbox flows are covered by scripts/verify/phase*.sh.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO=$(pwd)
T=$(mktemp -d /tmp/ade-demo.XXXX)
PIDS=()
trap 'kill "${PIDS[@]}" 2>/dev/null || true; rm -rf "$T"' EXIT

go build -o "$T/ade" ./cmd/ade
go build -o "$T/fakegl" ./scripts/verify/fakegl
cp scripts/verify/fakeomp "$T/fakeomp"

mkdir -p "$T/remotes"
"$T/fakegl" -tier premium -agile -carried -mr-template -review ade-demo-env/api!2 \
  -git-root "$T/remotes" -git-apps "$REPO/scripts/sandbox/apps" >"$T/gl.url" 2>"$T/gl.err" &
PIDS+=($!)
for _ in $(seq 50); do [[ -s $T/gl.url ]] && break; sleep 0.1; done
URL=$(head -1 "$T/gl.url")
[[ -n $URL ]] || { cat "$T/gl.err" >&2; exit 1; }

mkdir -p "$T/projects"
sed -e 's/^weight .*/weight     = { source = "gitlab" }/' \
  -e 's/^iterations = .*/iterations = { source = "milestones", overlap = 10 }/' \
  scripts/sandbox/manifests/gitlab.ade.toml |
  awk -v api="file://$T/remotes/api.git" -v web="file://$T/remotes/web.git" '
    {print}
    /^path +=.*\/api"/ {print "url  = \"" api "\""}
    /^path +=.*\/web"/ {print "url  = \"" web "\""}' >"$T/manifest.ade.toml"
cat >"$T/config.toml" <<EOF
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
root = "$T/projects"
[[projects]]
source = "$T/manifest.ade.toml"
[ai]
mode = "auto"
profile = "stub"
[ai.profiles.stub]
driver = "omp"
command = "$T/fakeomp"
models = { explain = "claude-haiku-4-5", draft = "claude-haiku-4-5", review = "claude-haiku-4-5" }
billing = { source = "omp", weekly_soft_usd = 10 }
[ai.profiles.stub.prices.claude-haiku-4-5]
input = 1.0
output = 5.0
[budget]
daily_usd = 5.0
EOF
export ADE_CONFIG="$T/config.toml" XDG_DATA_HOME="$T/data" XDG_CACHE_HOME="$T/cache" FAKE_GITLAB_TOKEN=fake-token
"$T/ade" project add "$T/manifest.ade.toml" >/dev/null
"$T/ade" sync >/dev/null
"$T/ade" notify send --kind ci.failed --severity error --project demo-gl \
  --subject "ade-demo-env/tracker#2" --title "web!1 pipeline failed: lint" >/dev/null

if [[ ${1:-} == --record ]]; then
  cat >"$T/run.sh" <<EOF
#!/usr/bin/env bash
export ADE_CONFIG="$ADE_CONFIG" XDG_DATA_HOME="$XDG_DATA_HOME" XDG_CACHE_HOME="$XDG_CACHE_HOME" FAKE_GITLAB_TOKEN=fake-token
exec "$T/ade" dash
EOF
  chmod +x "$T/run.sh"
  cat >"$T/tour.tape" <<EOF
Output "$REPO/demo.gif"
Set Shell bash
Set Width 1600
Set Height 900
Set FontSize 14
Set Padding 0
Set TypingSpeed 0
Set PlaybackSpeed 1
Env COLORTERM "truecolor"
Hide
Type "$T/run.sh"
Enter
Sleep 2s
Show
Sleep 3s
Type "j"
Sleep 1s
Type "j"
Sleep 1s
Type "?"
Sleep 3s
Escape
Sleep 1s
Type "/"
Sleep 500ms
Type "rate"
Sleep 2s
Escape
Sleep 1s
Type ":"
Sleep 2s
Escape
Sleep 1s
Type "i"
Sleep 3s
Escape
Sleep 1s
Type "gg"
Sleep 500ms
Enter
Sleep 4s
Type "2"
Sleep 3s
Type "3"
Sleep 3s
Type "j"
Sleep 500ms
Type "5"
Sleep 3s
Type "4"
Sleep 2s
Escape
Sleep 1s
Type "V"
Sleep 4s
Escape
Sleep 1s
Type "S"
Sleep 4s
Escape
Sleep 1s
Type "b"
Sleep 3s
Escape
Sleep 1s
Type "L"
Sleep 3s
Escape
Sleep 1s
Type "t"
Sleep 3s
Type "q"
Sleep 1s
EOF
  env -u NO_COLOR TERM=xterm-256color vhs "$T/tour.tape"
  echo "wrote $REPO/demo.gif"
else
  env -u NO_COLOR "$T/ade" dash
fi
