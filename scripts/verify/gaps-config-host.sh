#!/usr/bin/env bash
# Gap round, ConfigHost items (docs/PLAN.md §2, §2.1, §2.3, §4.2, §4.3, §6.4, §6.7):
#   host.editor resolution, conventions.mr.draft, *.local.toml overlays, `ade config schema`,
#   audit_retain pruning, `ade audit tail`, `--debug-http`, `ade cache reset`, the doctor
#   records-in-git warning, the zellij unlock-first preset, `ade ws restart`, update order
#   (git then sync), cycle time + per-parent narrative.
#
# Fake forge, fake AI endpoint and throwaway state for everything except the ws restart
# section, which runs the sandbox apps (needs .env with GITLAB_PAT, jq, curl, go).
# Run from the repo root inside `nix develop`; exits 0 when every check passes.
[[ -n ${VERIFY_TMP:-} ]] || . scripts/verify/lib.sh
[[ -n ${ADE:-} && -x ${ADE:-} ]] || build_ade

PIDS=()
cleanup() {
  set +e
  kill "${PIDS[@]}" 2>/dev/null
  if [[ -n ${WS_TREE:-} ]]; then "$ADE" ws down "$WS_TREE" >/dev/null 2>&1; fi
  return 0
}
trap 'cleanup; rm -rf "$VERIFY_TMP"' EXIT

# run_ade ENV... -- ARGS: run ade with extra environment, capture stdout/stderr/exit code.
# Results: $VERIFY_TMP/stdout, $VERIFY_TMP/stderr, RC.
run_ade() {
  local env=()
  while [[ $1 != -- ]]; do
    env+=("$1")
    shift
  done
  shift
  set +e
  env "${env[@]}" "$ADE" "$@" >"$VERIFY_TMP/stdout" 2>"$VERIFY_TMP/stderr"
  RC=$?
  set -e
}

# ---------------------------------------------------------------------------------------
step "unit and package tests of every item"
check "editor resolution matrix (auto/code/ade-nvim/nvim/\$EDITOR, VS Code detection incl. zellij panes, argv for code -g file:line, --wait, nvim +line)" \
  go test ./internal/editor/... -count=1
check "config: local overrides (merge semantics), schema, committed schemas current, host.editor validation" \
  go test ./internal/config/... -count=1
check "audit: retention prune (whole months, never the current), follow, partial lines" \
  go test ./internal/audit/... ./internal/app/... -count=1
check "--debug-http transport: redacted request/response lines, no bodies" go test ./internal/httpdebug/... -count=1
check "project snapshots carry local manifest overrides; invalid merge is rejected" \
  go test ./internal/project/... -run 'LocalOverride' -count=1
check "conventions.mr.draft: unset is a draft, false is not" go test ./internal/startwork/... -run 'MRDraft' -count=1
check "workspace Restart (blocked subtree, holds kept, dead requirement stays blocked) and editor pane layout" \
  go test ./internal/workspace/... -run 'Restart|CodeLayout' -count=1
check "zellij unlock-first preset + argv with --config" go test ./internal/host/... -count=1
check "doctor: records dir in a git work tree, resolved editor" go test ./internal/doctor/... -count=1
check "cache reset (deletes, rebuilds ledger, audits, dry-run and decline touch nothing)" \
  go test ./internal/cli/... -run 'Cache' -count=1
check "refresh order: git update, then forge sync" go test ./internal/tui/... -run 'Refresh' -count=1
check "agile: cycle time per item and median, per-parent narrative, markdown only" go test ./internal/agile/... -count=1

# ---------------------------------------------------------------------------------------
step "ade config schema: generated from the structs, committed under docs/schema"
"$ADE" config schema --write "$VERIFY_TMP/schema" >/dev/null
check "docs/schema/*.json equal what the structs generate (regenerate: ade config schema --write docs/schema)" \
  diff -r docs/schema "$VERIFY_TMP/schema"
"$ADE" config schema >"$VERIFY_TMP/config.schema.json"
"$ADE" config schema --manifest >"$VERIFY_TMP/manifest.schema.json"
expect_json "config schema: draft 2020-12, host.editor enum has ade-nvim, closed objects" "$VERIFY_TMP/config.schema.json" \
  '."$schema" | endswith("2020-12/schema")'
expect_json "config schema lists the editor choices" "$VERIFY_TMP/config.schema.json" \
  '.properties.host.properties.editor.enum == ["auto","nvim","ade-nvim","code","$EDITOR"] and .additionalProperties == false'
expect_json "config schema: audit_retain pattern, durations, maps" "$VERIFY_TMP/config.schema.json" \
  '(.properties.records.properties.audit_retain.pattern | contains("forever")) and (.properties.update.properties.interval.type == "string") and (.properties.forges.additionalProperties.properties.kind.enum == ["gitlab","github"])'
expect_json "manifest schema: conventions.mr.draft is a boolean, ports are auto or a number" "$VERIFY_TMP/manifest.schema.json" \
  '.properties.conventions.properties.mr.properties.draft.type == "boolean" and (.properties.services.additionalProperties.properties.port.oneOf | length == 2)'

# ---------------------------------------------------------------------------------------
step "local overrides: *.local.toml beside the config and beside a manifest"
LO="$VERIFY_TMP/local"
mkdir -p "$LO"
export XDG_DATA_HOME="$LO/data" XDG_CACHE_HOME="$LO/cache"
cat >"$LO/config.toml" <<EOF
[user]
handle = "jdoe"
[host]
mode   = "plain"
editor = "nvim"
[notify]
toast = ["ci.*", "mr.*"]
[forges.gl]
kind = "gitlab"
token_env = "GL_TOKEN"
EOF
cat >"$LO/config.local.toml" <<EOF
[host]
editor = "\$EDITOR"
[notify]
toast = ["budget.*"]
EOF
run_ade ADE_CONFIG="$LO/config.toml" EDITOR=hx VISUAL= TERM_PROGRAM=vscode -- doctor
grep -q 'editor.*\$EDITOR (hx)' "$VERIFY_TMP/stdout" && pass "the overlay's host.editor = \"\$EDITOR\" wins over the base's nvim (doctor reports \$EDITOR (hx))" ||
  { cat "$VERIFY_TMP/stdout" >&2; fail "doctor does not show the overlay's editor"; }
run_ade ADE_CONFIG="$LO/config.toml" -- config check --json
expect_json "config check names the overlay it merged" "$VERIFY_TMP/stdout" \
  '.[0].kind == "config" and (.[0].overlay | endswith("config.local.toml"))'
printf '[host]\nedtor = "typo"\n' >"$LO/config.local.toml"
run_ade ADE_CONFIG="$LO/config.toml" -- config check --json
expect_json "a typo in the overlay is reported like one in the base" "$VERIFY_TMP/stdout" \
  '[.[0].issues[] | select(.path == "host.edtor" and .msg == "unknown key")] | length == 1'
printf '[host\neditor = ' >"$LO/config.local.toml"
run_ade ADE_CONFIG="$LO/config.toml" -- config check
[[ $RC -ne 0 ]] && grep -q 'config.local.toml' "$VERIFY_TMP/stdout" "$VERIFY_TMP/stderr" && pass "a syntax error names config.local.toml" ||
  { cat "$VERIFY_TMP/stdout" "$VERIFY_TMP/stderr" >&2; fail "overlay syntax error not reported with its file"; }
rm "$LO/config.local.toml"

cat >"$LO/shop.ade.toml" <<EOF
name = "shop"
forge = "gl"
[repos.api]
path = "shop/api"
[repos.api.tasks.dev]
cmd  = "make run"
port = "auto"
[groups]
dev = ["api.dev"]
EOF
run_ade ADE_CONFIG="$LO/config.toml" -- config check --manifest "$LO/shop.ade.toml"
[[ $RC -eq 0 ]] && pass "the manifest alone is valid" || fail "base manifest invalid: $(cat "$VERIFY_TMP/stdout")"
cat >"$LO/shop.ade.local.toml" <<EOF
[repos.api.tasks.dev]
requires = ["api.nope"]
EOF
run_ade ADE_CONFIG="$LO/config.toml" -- config check --manifest "$LO/shop.ade.toml"
[[ $RC -ne 0 ]] && grep -q 'api.nope' "$VERIFY_TMP/stdout" && grep -q 'local override' "$VERIFY_TMP/stdout" &&
  pass "shop.ade.local.toml is merged into the manifest before validation (its bad requires is caught, the override is named)" ||
  { cat "$VERIFY_TMP/stdout" >&2; fail "manifest overlay not applied"; }
cat >"$LO/shop.ade.local.toml" <<EOF
[repos.api]
url = "git@example.com:me/api.git"
[repos.api.tasks.dev]
port = 8080
EOF
run_ade ADE_CONFIG="$LO/config.toml" -- config check --manifest "$LO/shop.ade.toml"
[[ $RC -eq 0 ]] && pass "a valid per-machine override (clone URL, fixed port) passes" || fail "$(cat "$VERIFY_TMP/stdout")"

# ---------------------------------------------------------------------------------------
step "audit_retain: whole months past the retention are pruned, the current month never, and it is audited"
RT="$VERIFY_TMP/retain"
mkdir -p "$RT/data/ade/audit"
export XDG_DATA_HOME="$RT/data" XDG_CACHE_HOME="$RT/cache"
this_month=$(date -u +%Y-%m)
prev_month=$(date -u -d "$(date -u +%Y-%m-01) -1 month" +%Y-%m)
for m in 2019-03 2019-04 2021-07 "$prev_month"; do
  printf '{"id":"%s","ts":"%s-15T10:00:00Z","actor":"user","action":"seed.%s","outcome":"ok"}\n' "SEED$m" "$m" "$m" >"$RT/data/ade/audit/$m.jsonl"
done
cat >"$RT/config.toml" <<EOF
[user]
handle = "jdoe"
[records]
audit_retain = "forever"
EOF
run_ade ADE_CONFIG="$RT/config.toml" -- inbox --json
[[ -f $RT/data/ade/audit/2019-03.jsonl && -f $RT/data/ade/audit/2021-07.jsonl ]] && pass "audit_retain = forever keeps every month" || fail "forever pruned something"
sed -i 's/audit_retain = "forever"/audit_retain = "400d"/' "$RT/config.toml"
run_ade ADE_CONFIG="$RT/config.toml" -- inbox --json
[[ ! -f $RT/data/ade/audit/2019-03.jsonl && ! -f $RT/data/ade/audit/2019-04.jsonl && ! -f $RT/data/ade/audit/2021-07.jsonl ]] &&
  pass "400d removed the 2019 and 2021 months" || fail "old months survive: $(ls "$RT/data/ade/audit")"
[[ -f $RT/data/ade/audit/$prev_month.jsonl && -f $RT/data/ade/audit/$this_month.jsonl ]] &&
  pass "last month (inside the window) and the current month are untouched" || fail "recent months pruned: $(ls "$RT/data/ade/audit")"
run_ade ADE_CONFIG="$RT/config.toml" -- audit --action 'audit.prune' --json
expect_json "the prune is audited: audit.prune names the months" "$VERIFY_TMP/stdout" \
  '([.[] | select(.action == "audit.prune") | .target] | join(",")) == "2019-03,2019-04,2021-07" and all(.[]; .outcome == "ok")'
sed -i 's/audit_retain = "400d"/audit_retain = "0d"/' "$RT/config.toml"
run_ade ADE_CONFIG="$RT/config.toml" -- inbox --json
[[ ! -f $RT/data/ade/audit/$prev_month.jsonl && -f $RT/data/ade/audit/$this_month.jsonl ]] &&
  pass "0d removes last month and still never the current one" || fail "0d: $(ls "$RT/data/ade/audit")"

# ---------------------------------------------------------------------------------------
step "ade audit tail [--follow]"
run_ade ADE_CONFIG="$RT/config.toml" -- audit tail -n 2 --json
[[ $(wc -l <"$VERIFY_TMP/stdout") -eq 2 ]] && pass "two lines for -n 2" || fail "tail -n 2 printed $(wc -l <"$VERIFY_TMP/stdout") lines"
jq -s -e '.[0].ts <= .[1].ts and all(.[]; .action != null)' "$VERIFY_TMP/stdout" >/dev/null && pass "valid JSONL, oldest first" || fail "tail JSONL malformed"
ADE_CONFIG="$RT/config.toml" "$ADE" audit tail -n 0 --follow --action 'cli.inbox' >"$VERIFY_TMP/follow.out" 2>&1 &
FOLLOW=$!
PIDS+=("$FOLLOW")
sleep 1
for _ in 1 2 3; do ADE_CONFIG="$RT/config.toml" "$ADE" inbox --json >/dev/null; done
for _ in $(seq 40); do
  [[ $(grep -c 'cli.inbox' "$VERIFY_TMP/follow.out") -ge 3 ]] && break
  sleep 0.1
done
kill "$FOLLOW" 2>/dev/null || true
wait "$FOLLOW" 2>/dev/null || true
[[ $(grep -c 'cli.inbox' "$VERIFY_TMP/follow.out") -eq 3 ]] && pass "--follow printed the three entries other ade processes appended, filtered by --action, and nothing from before" ||
  { cat "$VERIFY_TMP/follow.out" >&2; fail "follow output wrong"; }

# ---------------------------------------------------------------------------------------
step "fake forge + fake AI endpoint for --debug-http, cache reset and cycle time"
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
start gl "$VERIFY_TMP/fakegl" -tier premium -agile
GL=$URL
start ai "$VERIFY_TMP/fakeai"
AI=$URL
DEMO="$VERIFY_TMP/demo"
mkdir -p "$DEMO"
sed 's/^weight .*/weight     = { source = "gitlab" }/' "$REPO/scripts/sandbox/manifests/gitlab.ade.toml" >"$DEMO/manifest.ade.toml"
SECRET_GL="fake-token"   # the only credential the fake GitLab accepts
SECRET_AI="sk-fakeAiKey1234567890abcdef"
cat >"$DEMO/config.toml" <<EOF
[user]
handle = "sandbox"
timezone = "UTC"
[forges.sandbox-gitlab]
kind = "gitlab"
url = "$GL"
token_env = "FAKE_GITLAB_TOKEN"
git_protocol = "https"
[update]
on_start = false
[paths]
root = "$DEMO/projects"
[ai]
mode = "auto"
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
confirm_over_usd = 0
[[projects]]
source = "$DEMO/manifest.ade.toml"
EOF
export ADE_CONFIG="$DEMO/config.toml" XDG_DATA_HOME="$DEMO/data" XDG_CACHE_HOME="$DEMO/cache"
export FAKE_GITLAB_TOKEN="$SECRET_GL" FAKE_AI_KEY="$SECRET_AI"

step "--debug-http: redacted request and response lines for forge and AI drivers, no bodies"
run_ade -- sync --debug-http
grep -q '^http → GET ' "$VERIFY_TMP/stderr" && grep -q '^http ← 200 GET ' "$VERIFY_TMP/stderr" &&
  pass "forge requests are logged: request line and response line with status and timing" ||
  { head -20 "$VERIFY_TMP/stderr" >&2; fail "no http lines from the GitLab client"; }
grep -Eq '^http ← 200 GET .* [0-9.]+m?s' "$VERIFY_TMP/stderr" && pass "timing is on the response line" || fail "no timing"
grep -q 'Private-Token: \[redacted\]' "$VERIFY_TMP/stderr" && pass "the credential header is reported as present, value redacted" ||
  fail "credential header not reported"
if grep -q "$SECRET_GL" "$VERIFY_TMP/stderr" "$VERIFY_TMP/stdout"; then fail "the GitLab token leaked into the debug output"; else pass "the GitLab token never appears"; fi
run_ade -- ai models --debug-http
grep -q "^http → GET $AI/models" "$VERIFY_TMP/stderr" && pass "AI driver requests are logged too (openai-compat GET /models)" ||
  { cat "$VERIFY_TMP/stderr" >&2; fail "no http lines from the AI driver"; }
if grep -q "$SECRET_AI" "$VERIFY_TMP/stderr" "$VERIFY_TMP/stdout"; then fail "the AI key leaked into the debug output"; else pass "the AI key never appears"; fi
# A credential in the URL's query string is masked in the debug lines (the command's own
# error text is not the debug output and is left alone), and bodies are never printed.
run_ade -- project add "$GL/ignored.ade.toml?access_token=$SECRET_GL&page=2" --debug-http
grep '^http' "$VERIFY_TMP/stderr" >"$VERIFY_TMP/httplines"
[[ -s $VERIFY_TMP/httplines ]] || fail "no debug lines for the manifest fetch"
if grep -q "$SECRET_GL" "$VERIFY_TMP/httplines"; then fail "a query-string token leaked into the debug lines"; else pass "query-string credentials are masked in the debug lines"; fi
grep -q 'page=2' "$VERIFY_TMP/httplines" && pass "the rest of the query is kept for debugging" || fail "query lost"
if grep -q '401 Unauthorized"' "$VERIFY_TMP/httplines"; then fail "a response body reached the debug lines"; else pass "no response body in the debug lines"; fi
run_ade -- items --json
[[ ! -s $VERIFY_TMP/stderr || ! $(grep -c '^http' "$VERIFY_TMP/stderr") -gt 0 ]] && pass "without the flag nothing is logged" || fail "http lines without --debug-http"

# ---------------------------------------------------------------------------------------
step "cache reset"
run_ade -- sync
run_ade -- items --json
expect_json "setup: the sync filled the cache" "$VERIFY_TMP/stdout" 'length > 0'
run_ade -- cache reset --dry-run --json
expect_json "--dry-run says what would be lost and deletes nothing" "$VERIFY_TMP/stdout" \
  '.dry_run == true and .reset == false and (.path | endswith("cache.db")) and (.review_drafts | type == "number") and (.unread_notifications | type == "number")'
CACHE_DB="$XDG_CACHE_HOME/ade/cache.db"
[[ -f $CACHE_DB ]] && pass "cache.db still there after --dry-run" || fail "dry-run deleted the cache"
run_ade -- cache reset </dev/null
[[ $RC -eq 2 ]] && grep -q -- '--yes' "$VERIFY_TMP/stderr" && pass "without a terminal and without --yes it refuses (exit 2), deleting nothing" ||
  { cat "$VERIFY_TMP/stderr" >&2; fail "reset ran without confirmation (rc=$RC)"; }
run_ade -- cache reset --yes --json
expect_json "--yes deletes, recreates, reindexes the ledger and AI results" "$VERIFY_TMP/stdout" \
  '.reset == true and (.ledger_entries | type == "number") and (.ai_results | type == "number")'
run_ade -- items --json
[[ $RC -ne 0 ]] && grep -q 'ade sync' "$VERIFY_TMP/stderr" && pass "forge data is gone until the next sync" || fail "cache not reset (rc=$RC)"
run_ade -- sync
run_ade -- items --json
expect_json "sync restores the cache" "$VERIFY_TMP/stdout" 'length > 0'
run_ade -- audit --action 'cache.reset' --json
expect_json "the reset is audited" "$VERIFY_TMP/stdout" 'length == 1 and .[0].outcome == "ok"'

# ---------------------------------------------------------------------------------------
step "doctor warns when records.dir is inside a git work tree"
REPO_DIR="$VERIFY_TMP/some-repo"
git init -q "$REPO_DIR"
mkdir -p "$VERIFY_TMP/doc"
cat >"$VERIFY_TMP/doc/config.toml" <<EOF
[user]
handle = "jdoe"
[records]
dir = "$REPO_DIR/ade-records"
EOF
run_ade ADE_CONFIG="$VERIFY_TMP/doc/config.toml" -- doctor --json
expect_json "doctor --json: 'records outside git' is a warning naming the work tree" "$VERIFY_TMP/stdout" \
  '[.[] | select(.name == "records outside git")] | length == 1 and .[0].status == "warn" and (.[0].detail | contains("inside the git work tree")) and (.[0].fix | contains("outside every repository"))'
run_ade ADE_CONFIG="$VERIFY_TMP/doc/config.toml" -- doctor
grep -q 'records outside git' "$VERIFY_TMP/stdout" && pass "the warning is in the human report too" || fail "no warning in the text report"
sed -i "s|^dir = .*|dir = \"$VERIFY_TMP/doc/records\"|" "$VERIFY_TMP/doc/config.toml"
run_ade ADE_CONFIG="$VERIFY_TMP/doc/config.toml" -- doctor --json
expect_json "a records dir outside any repository passes" "$VERIFY_TMP/stdout" \
  '[.[] | select(.name == "records outside git")] | .[0].status == "ok"'

# ---------------------------------------------------------------------------------------
step "editor resolution as doctor reports it"
mkdir -p "$VERIFY_TMP/bin"
for t in code nvim ade-nvim; do printf '#!/bin/sh\n' >"$VERIFY_TMP/bin/$t" && chmod +x "$VERIFY_TMP/bin/$t"; done
editor_row() { # SETTING ENV...: the editor detail of `ade doctor --json`
  local setting=$1
  shift
  printf '[user]\nhandle = "jdoe"\n[host]\neditor = "%s"\n' "$setting" >"$VERIFY_TMP/doc/ed.toml"
  env -u TERM_PROGRAM -u VSCODE_IPC_HOOK_CLI -u EDITOR -u VISUAL "$@" ADE_CONFIG="$VERIFY_TMP/doc/ed.toml" PATH="$VERIFY_TMP/bin:/usr/bin:/bin" \
    "$ADE" doctor --json 2>/dev/null | jq -r '.[] | select(.name == "editor") | .detail'
}
[[ $(editor_row auto TERM_PROGRAM=vscode) == "code ($VERIFY_TMP/bin/code)" ]] && pass "auto inside a VS Code terminal: code" || fail "auto in vscode: $(editor_row auto TERM_PROGRAM=vscode)"
[[ $(editor_row auto VSCODE_IPC_HOOK_CLI=/run/x.sock) == code* ]] && pass "auto from a zellij pane inside VS Code (IPC socket variable): code" || fail "ipc detection"
[[ $(editor_row auto EDITOR=nano) == "nvim ($VERIFY_TMP/bin/nvim)" ]] && pass "auto outside VS Code: nvim before \$EDITOR" || fail "auto outside vscode"
[[ $(editor_row ade-nvim) == "ade-nvim ($VERIFY_TMP/bin/ade-nvim)" ]] && pass "host.editor = ade-nvim picks the wrapper" || fail "ade-nvim"
[[ $(editor_row '$EDITOR' EDITOR=nano) == '$EDITOR (nano)' ]] && pass "host.editor = \$EDITOR uses the environment" || fail "\$EDITOR"
[[ $(editor_row code) == code* ]] && pass "host.editor = code outside VS Code still means code" || fail "code"

# ---------------------------------------------------------------------------------------
step "zellij unlock-first preset inside VS Code"
printf '[user]\nhandle = "jdoe"\n[host]\nmode = "zellij"\n' >"$VERIFY_TMP/doc/z.toml"
run_ade ADE_CONFIG="$VERIFY_TMP/doc/z.toml" ZELLIJ= TERM_PROGRAM=vscode -- launch --dry-run
grep -q "^# zellij config: $VERIFY_TMP/doc/zellij-unlock-first.kdl (unlock-first preset)" "$VERIFY_TMP/stdout" &&
  grep -q -- "^# argv: zellij --config $VERIFY_TMP/doc/zellij-unlock-first.kdl " "$VERIFY_TMP/stdout" &&
  pass "inside VS Code the zellij argv carries --config <config dir>/zellij-unlock-first.kdl" ||
  { cat "$VERIFY_TMP/stdout" "$VERIFY_TMP/stderr" >&2; fail "no preset in the zellij argv"; }
run_ade ADE_CONFIG="$VERIFY_TMP/doc/z.toml" ZELLIJ= TERM_PROGRAM= VSCODE_IPC_HOOK_CLI= -- launch --dry-run
grep -q -- '--config' "$VERIFY_TMP/stdout" && fail "outside VS Code zellij must keep the user's config" || pass "outside VS Code: no --config"
run_ade ADE_CONFIG="$VERIFY_TMP/doc/z.toml" ZELLIJ= TERM_PROGRAM=vscode ZELLIJ_CONFIG_FILE=/home/x/z.kdl -- launch --dry-run
grep -q -- '--config' "$VERIFY_TMP/stdout" && fail "an explicit ZELLIJ_CONFIG_FILE must win" || pass "an explicit \$ZELLIJ_CONFIG_FILE wins over the preset"
if command -v zellij >/dev/null; then
  zellij --config internal/host/zellij/unlock-first.kdl setup --check 2>&1 | grep -q 'CONFIG FILE.*Well defined' &&
    pass "zellij itself accepts the shipped preset (setup --check: Well defined)" || fail "zellij rejects the preset"
fi
grep -q 'default_mode "locked"' internal/host/zellij/unlock-first.kdl && grep -q 'bind "Ctrl g"' internal/host/zellij/unlock-first.kdl &&
  pass "the preset starts locked and unlocks on Ctrl-g" || fail "preset content"

# ---------------------------------------------------------------------------------------
step "cycle time (assigned → merged) and per-parent narrative on the fake forge"
# fakegl -agile: tracker#4 assigned 3 Nov 09:00 (system note), api!3 merged 5 Nov 17:00 -> 56h;
# #10 assigned 4 Nov 09:00, api!4 merged 9 Nov 13:30 -> 124.5h; #12 has no assignment note, so
# it counts from its creation (5 Nov 09:00), api!5 merged 9 Nov 14:30 -> 101.5h. Median 101.5h.
run_ade -- sprint review --json
cp "$VERIFY_TMP/stdout" "$VERIFY_TMP/review.json"
expect_json "cycle summary: three measured items, median 101.5h" "$VERIFY_TMP/review.json" \
  '.cycle == {"items": 3, "median_hours": 101.5}'
expect_json "per item: hours from the assignment note, or from creation when the forge has no note" "$VERIFY_TMP/review.json" \
  '([.items[] | select(.cycle_hours != null) | [(.ref | split("#")[1]), .cycle_hours, .assigned_from]] | sort) == [["10",124.5,"note"],["12",101.5,"created"],["4",56,"note"]]'
expect_json "items without a merged MR have no cycle time (not zero)" "$VERIFY_TMP/review.json" \
  'all(.items[] | select(.merged_mrs == null); .cycle_hours == null)'
run_ade -- sprint review --md
grep -q 'Cycle time (assigned → merged): median 4d 6h over 3 items' "$VERIFY_TMP/stdout" && pass "markdown summary: median 4d 6h over 3 items" ||
  { cat "$VERIFY_TMP/stdout" >&2; fail "no cycle summary line"; }
grep -q 'tracker#4 · done · 1w · Remove the legacy CSV export path · merged api!3 · cycle 2d 8h$' "$VERIFY_TMP/stdout" &&
  grep -q 'tracker#12 .* cycle 4d 6h (from creation)$' "$VERIFY_TMP/stdout" && pass "item lines carry the cycle, with the creation fallback marked" || fail "item lines lack the cycle"
run_ade -- sprint review --md --ai-parents --yes
grep -q '^narrated 3 parents' "$VERIFY_TMP/stderr" && pass "--ai-parents narrates the three parents with merged MRs (and no other)" ||
  { cat "$VERIFY_TMP/stderr" >&2; fail "--ai-parents did not narrate 3 parents"; }
awk '/^- tracker#4 /{getline n; print n}' "$VERIFY_TMP/stdout" | grep -q '^  .\+' && pass "the paragraph is indented under its item in the markdown export" || fail "no paragraph under tracker#4"
[[ $(grep -c '^- tracker#3 ' "$VERIFY_TMP/stdout") == 1 && $(awk '/^- tracker#3 /{getline n; print n}' "$VERIFY_TMP/stdout") == -* ]] && pass "a carried parent gets no paragraph" || fail "carried parent narrated"
run_ade -- sprint review --ai-parents --yes
if grep -q 'Sprint 42 committed' "$VERIFY_TMP/stdout"; then fail "the plain-text export must not carry the per-parent narrative"; else pass "the text export leaves the per-parent narrative out (Markdown only)"; fi
run_ade -- sprint review --json --ai-parents --yes
expect_json "--json carries each narrative on its item" "$VERIFY_TMP/stdout" '[.items[] | select(.narrative != null)] | length == 3'
run_ade -- audit --action 'ai.sprint-parent' --json
expect_json "each parent narrative is an audited AI call (feature sprint-parent)" "$VERIFY_TMP/stdout" 'length >= 3 and all(.[]; .ai.model == "fake-model")'
expect_json "the repeat runs were served from the cache: later calls are hits at no cost" "$VERIFY_TMP/stdout" \
  '([.[] | select(.ai.cache_hit)] | length) >= 6 and all(.[] | select(.ai.cache_hit); .ai.cost_usd == 0)'

# ---------------------------------------------------------------------------------------
step "ade ws restart on the sandbox apps (--no-zellij): the blocked subtree comes back"
[[ -f .env ]] || fail "the sandbox section needs .env with GITLAB_PAT"
(
  set -euo pipefail
  set -a
  . ./.env
  set +a
  WS=$(mktemp -d -p "$VERIFY_TMP" ws.XXXXXX)
  export GOCACHE=${GOCACHE:-$(go env GOCACHE)} GOMODCACHE=${GOMODCACHE:-$(go env GOMODCACHE)}
  export XDG_DATA_HOME="$WS/data" XDG_CACHE_HOME="$WS/cache" ADE_CONFIG="$WS/config.toml"
  unset ZELLIJ ZELLIJ_SESSION_NAME ZELLIJ_PANE_ID TERM_PROGRAM FAKE_GITLAB_TOKEN FAKE_AI_KEY
  cat >"$ADE_CONFIG" <<EOF
[user]
handle = "sandbox"
[host]
mode = "plain"
[forges.sandbox-gitlab]
kind = "gitlab"
token_env = "GITLAB_PAT"
git_protocol = "https"
[paths]
root = "$WS/projects"
[[projects]]
source = "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
EOF
  WEB="$WS/projects/demo-gl/web/main"
  jget() { jq -r "$2" "$1"; }
  health() { curl -fsS --max-time 5 "http://127.0.0.1:$1/healthz"; }
  stop_all() { "$ADE" ws down "$WEB" >/dev/null 2>&1 || true; }
  trap stop_all EXIT

  check "ade project add clones api and web" "$ADE" project add "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
  [[ -f $WS/projects/demo-gl/api/main/main.go && -f $WEB/main.go ]] || fail "the baseline trees lack the sandbox apps (run scripts/sandbox/apps-seed.sh)"
  "$ADE" ws up --no-zellij --detach "$WEB" --json >"$WS/up.json"
  expect_json "up: check, db, api, web ready" "$WS/up.json" 'all(.[]; .outcome == "ready") and ([.[].node] == ["api.check","api.db","api.dev","web.dev"])'
  OLD_API=$(jget "$WS/up.json" '.[] | select(.node == "api.dev") | .pid')
  OLD_WEB=$(jget "$WS/up.json" '.[] | select(.node == "web.dev") | .pid')
  OLD_DB=$(jget "$WS/up.json" '.[] | select(.node == "api.db") | .pid')
  API_PORT=$(jget "$WS/up.json" '.[] | select(.node == "api.dev") | .port')
  WEB_PORT=$(jget "$WS/up.json" '.[] | select(.node == "web.dev") | .port')

  kill -KILL -- "-$OLD_API"
  for _ in $(seq 20); do
    "$ADE" ws status --json >"$WS/blocked.json"
    [[ $(jget "$WS/blocked.json" '.[] | select(.node == "web.dev") | .state') == blocked ]] && break
    sleep 0.5
  done
  expect_json "api was killed behind ADE's back: web.dev is blocked by api.dev" "$WS/blocked.json" \
    '(.[] | select(.node == "web.dev") | .state == "blocked" and .blocked_by == "api.dev")'

  "$ADE" ws restart api.dev "$WEB" --json >"$WS/restart.json"
  expect_json "restart api.dev restarts it and its dependent web.dev, nothing else, both ready" "$WS/restart.json" \
    '[.[].node] == ["api.dev","web.dev"] and all(.[]; .outcome == "ready")'
  expect_json "fresh processes, same ports" "$WS/restart.json" \
    "(.[] | select(.node == \"api.dev\") | .pid != $OLD_API and .port == $API_PORT) and (.[] | select(.node == \"web.dev\") | .pid != $OLD_WEB and .port == $WEB_PORT)"
  health "$API_PORT" >"$WS/api.json"
  expect_json "the restarted api serves /healthz and still reaches the (untouched) db" "$WS/api.json" '.status == "ok" and .db.reachable == true'
  health "$WEB_PORT" >"$WS/web.json"
  expect_json "web was restarted against the fresh api and is serving" "$WS/web.json" ".api.reachable == true and .api.url == \"http://127.0.0.1:$API_PORT\""
  "$ADE" ws status --json >"$WS/after.json"
  expect_json "status: nothing blocked, the db is the same process, each restarted instance still held once" "$WS/after.json" \
    "all(.[]; .state != \"blocked\") and (.[] | select(.node == \"api.db\") | .pid == $OLD_DB) and ([.[] | select(.node == \"api.dev\" or .node == \"web.dev\") | (.holders | length)] == [1,1])"

  "$ADE" ws restart web.dev "$WEB" --json >"$WS/restart2.json"
  expect_json "restarting a leaf restarts only the leaf" "$WS/restart2.json" '[.[].node] == ["web.dev"] and .[0].outcome == "ready"'
  if "$ADE" ws restart nope.dev "$WEB" >"$WS/nope.out" 2>&1; then fail "restart of an unknown node succeeded"; fi
  grep -q 'api.dev' "$WS/nope.out" && pass "an unknown node is an error that lists the workspace's nodes" || { cat "$WS/nope.out" >&2; fail "unhelpful error for an unknown node"; }

  stop_all
  [[ $("$ADE" ws status --json | jq length) == 0 ]] && pass "ws down after restarts releases every instance (no leaked holds)" || fail "instances left"
  ! health "$API_PORT" >/dev/null 2>&1 && pass "api port closed" || fail "api still serving"
)

printf '\nall gaps-config-host checks passed\n'
