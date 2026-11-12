#!/usr/bin/env bash
# Round 3 acceptance: markdown in the dashboard (glamour), syntax-highlighted
# diffs (chroma), the textarea editor and viewport log (bubbles), and the
# teatest golden files. Run from the repo root inside `nix develop`. Needs no
# credentials: the end-to-end part runs the real binary against the in-process
# fake GitLab (scripts/verify/fakegl). Nothing leaves the machine.
. scripts/verify/lib.sh

FAKE_PID=""
GOLDEN_BACKUP=""
cleanup() {
  [[ -n $FAKE_PID ]] && kill "$FAKE_PID" 2>/dev/null || true
  [[ -n $GOLDEN_BACKUP && -f $GOLDEN_BACKUP ]] && cp "$GOLDEN_BACKUP" internal/overview/testdata/golden/pane_80x24.golden
  rm -rf "$VERIFY_TMP"
}
trap cleanup EXIT

step "markdown, syntax highlighting, editor"
check "mdrender: width, no background, no OSC 8, ascii, NO_COLOR, hanging items" go test ./internal/mdrender/...
check "syntax: colour by language, no mark inks, NO_COLOR keeps weight, patch" go test ./internal/syntax/...
check "tui: markdown preview, AI summary, diff highlighting, pager default, textarea editor" \
  go test ./internal/tui/... -run 'Markdown|AISummary|DiffLine|LangPath|PagerPatch|Editor|Reply|Comment|Review'
check "overview pane: markdown description, acceptance, notes" go test ./internal/overview/...

step "golden files (teatest)"
check "tui goldens match: Home, Home preview, MR tabs, Start work; 80x24 and 160x45; NO_COLOR; ascii" \
  go test ./internal/tui/ -run TestGolden -count=1
check "overview pane goldens match" go test ./internal/overview/ -run TestGolden -count=1
n=$(find internal/tui/testdata/golden -name '*.golden' | wc -l)
[[ $n -ge 40 ]] && pass "$n tui golden files" || fail "only $n tui golden files"
ls internal/tui/testdata/golden/home_80x24.golden internal/tui/testdata/golden/home_160x45.golden \
  internal/tui/testdata/golden/home_nocolor_80x24.golden internal/tui/testdata/golden/home_ascii_80x24.golden \
  internal/tui/testdata/golden/mr_overview_80x24.golden internal/tui/testdata/golden/mr_diff_160x45.golden \
  internal/tui/testdata/golden/startwork_choose_80x24.golden internal/overview/testdata/golden/pane_160x45.golden >/dev/null &&
  pass "the named screens exist at both sizes" || fail "missing golden files"
if grep -lE $'\x1b\\[[0-9;]*(3[0-7]|38|4[0-7]|48|9[0-7]|10[0-7])[;m]' internal/tui/testdata/golden/*nocolor* internal/overview/testdata/golden/*nocolor* >/dev/null 2>&1; then
  fail "a NO_COLOR golden holds a colour sequence"
else
  pass "no NO_COLOR golden holds a colour sequence"
fi

# -update rewrites a golden file, and a changed UI fails without it.
GOLDEN_BACKUP="$VERIFY_TMP/pane.golden"
cp internal/overview/testdata/golden/pane_80x24.golden "$GOLDEN_BACKUP"
printf 'tampered\n' >internal/overview/testdata/golden/pane_80x24.golden
if go test ./internal/overview/ -run TestGoldenPane -count=1 >/dev/null 2>&1; then fail "a changed golden file passed"; else pass "a changed golden file fails the test"; fi
check "-update regenerates it" go test ./internal/overview/ -run TestGoldenPane -count=1 -update
cmp -s "$GOLDEN_BACKUP" internal/overview/testdata/golden/pane_80x24.golden && pass "the regenerated file is the committed one" || fail "-update produced a different file"
GOLDEN_BACKUP=""

step "the real binary against the fake GitLab: ade item <ref> --watch renders the description as markdown"
build_ade
go build -o "$VERIFY_TMP/fakegl" ./scripts/verify/fakegl
"$VERIFY_TMP/fakegl" -tier free >"$VERIFY_TMP/fake.url" 2>"$VERIFY_TMP/fake.err" &
FAKE_PID=$!
for _ in $(seq 50); do
  [[ -s $VERIFY_TMP/fake.url ]] && break
  sleep 0.1
done
FAKE_URL=$(head -1 "$VERIFY_TMP/fake.url")
[[ -n $FAKE_URL ]] || fail "the fake GitLab did not start: $(cat "$VERIFY_TMP/fake.err")"
export XDG_DATA_HOME="$VERIFY_TMP/data" XDG_CACHE_HOME="$VERIFY_TMP/cache" ADE_CONFIG="$VERIFY_TMP/config.toml" FAKE_GITLAB_TOKEN=fake-token
cat >"$ADE_CONFIG" <<CFG
[user]
handle = "sandbox"
[forges.sandbox-gitlab]
kind = "gitlab"
url = "$FAKE_URL"
token_env = "FAKE_GITLAB_TOKEN"
[update]
on_start = false
[paths]
root = "$VERIFY_TMP/projects"
[[projects]]
source = "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
CFG
"$ADE" sync >/dev/null || fail "sync"
REF=ade-demo-env/tracker#1
COLUMNS=60 NO_COLOR=1 "$ADE" item "$REF" --watch >"$VERIFY_TMP/pane.txt" || fail "ade item --watch"
grep -q "Acceptance" "$VERIFY_TMP/pane.txt" && grep -q "\[x\] Agree limits with support" "$VERIFY_TMP/pane.txt" && pass "the acceptance list renders as task boxes" || { cat "$VERIFY_TMP/pane.txt" >&2; fail "acceptance list"; }
grep -q "^ Tenants can trigger unbounded CSV exports" "$VERIFY_TMP/pane.txt" && pass "the description paragraph is there, one cell in" || fail "description paragraph"
if grep -qE '(^|[^`])(##|\*\*)|\[ \] - ' "$VERIFY_TMP/pane.txt"; then fail "raw markdown marks reach the pane"; else pass "no raw markdown marks in the pane"; fi
over=$(awk '{ if (length($0) > 60) n++ } END { print n+0 }' "$VERIFY_TMP/pane.txt")
[[ $over == 0 ]] && pass "no line is wider than COLUMNS=60" || fail "$over lines wider than 60"
sed -i 's/^\[user\]/[host]\nglyphs = "ascii"\n[user]/' "$ADE_CONFIG"
COLUMNS=60 "$ADE" item "$REF" --watch >"$VERIFY_TMP/pane-ascii.txt" || fail "ade item --watch (ascii)"
if LC_ALL=C grep -qP '[^\x00-\x7F]' "$VERIFY_TMP/pane-ascii.txt"; then
  LC_ALL=C grep -nP '[^\x00-\x7F]' "$VERIFY_TMP/pane-ascii.txt" >&2
  fail "the ascii glyph set drew non-ASCII"
else
  pass "host.glyphs = ascii: the whole pane, markdown included, is ASCII"
fi

step "build and lint"
check "go build ./..." go build ./...
check "golangci-lint on the touched packages" golangci-lint run ./internal/tui/... ./internal/overview/... ./internal/mdrender/... ./internal/syntax/... ./internal/goldentest/...
printf '\nr3-tuirender: all checks passed\n'
