#!/usr/bin/env bash
# Phase 7, team half (docs/PLAN.md §8 row 7): a teammate goes from zero to a dashboard
# via `ade init`, and ADE ships as a release binary and a flake package.
#
# GitHub parity checks live in phase7-github.sh, sourced at the end when present.
# Run from the repo root inside `nix develop`. Needs .env with GITLAB_PAT, git, jq.
# Never touches the real ~/.config/ade: the onboarding runs under a fresh HOME.
. scripts/verify/lib.sh

[[ -f .env ]] || fail "needs .env with GITLAB_PAT"
set -a
. ./.env
set +a
[[ -n ${GITLAB_PAT:-} ]] || fail "GITLAB_PAT is empty in .env"

step "unit tests"
check "onboarding: config render, safe replace + backup, host/token parsing, scripted init against the fake forge" \
  go test ./internal/onboard/... ./internal/cli/ -run 'TestInit|TestGather|TestVersion|TestRender|TestWriteConfig|TestNormalizeHost|TestParseTokenSource|TestForgeName'

step "teammate onboarding: scripted ade init in a fresh HOME against the GitLab sandbox"
VERSION_TAG="9.9.9-verify"
go build -ldflags "-X github.com/coredev-uk/ade/internal/cli.Version=$VERSION_TAG" -o "$VERIFY_TMP/ade" ./cmd/ade
ADE="$VERIFY_TMP/ade"
[[ $("$ADE" version) == "ade $VERSION_TAG" ]] && pass "ade version prints the injected version" ||
  fail "ade version = $("$ADE" version), want ade $VERSION_TAG"

REAL_CFG="${XDG_CONFIG_HOME:-$HOME/.config}/ade/config.toml"
real_before=$(stat -c '%Y %s' "$REAL_CFG" 2>/dev/null || echo absent)

NEWHOME=$(mktemp -d)
trap 'rm -rf "$VERIFY_TMP" "$NEWHOME"' EXIT
# A new machine: no XDG overrides, no ADE_CONFIG, an empty HOME. Git still needs a
# committer identity for nothing here (clones only), so none is set.
teammate() {
  env -u XDG_CONFIG_HOME -u XDG_DATA_HOME -u XDG_CACHE_HOME -u ADE_CONFIG HOME="$NEWHOME" "$ADE" "$@"
}
MANIFEST="$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
INIT_ARGS=(init --yes --forge gitlab --token-env GITLAB_PAT --git-protocol https --project "$MANIFEST")

check "ade init exits 0 (token verified, projects cloned, first sync, doctor)" \
  teammate "${INIT_ARGS[@]}"
CFG="$NEWHOME/.config/ade/config.toml"
[[ -f $CFG ]] && pass "config written under the fresh HOME" || fail "no config at $CFG"
[[ $(stat -c %a "$CFG") == 600 ]] && pass "config is owner-only (0600)" || fail "config mode is $(stat -c %a "$CFG")"
grep -q 'token_env = "GITLAB_PAT"' "$CFG" && ! grep -qF "$GITLAB_PAT" "$CFG" &&
  pass "config names the token variable and never holds the token" || fail "config token handling wrong"
grep -Eq '^\s*\[forges\.sandbox-gitlab\]' "$CFG" && pass "forge key taken from the manifest (sandbox-gitlab)" ||
  fail "forge key not taken from the manifest"
[[ -d $NEWHOME/ade/demo-gl/api/main ]] && pass "api cloned with its baseline worktree" ||
  fail "api not cloned under $NEWHOME/ade"
[[ -d $NEWHOME/ade/demo-gl/web/main ]] && pass "web cloned with its baseline worktree" || fail "web not cloned"

teammate items --json >"$VERIFY_TMP/items.json"
expect_json "dashboard data present: ade items --json is non-empty" "$VERIFY_TMP/items.json" 'length > 0'
expect_json "tracker#1 is there with its child and MR" "$VERIFY_TMP/items.json" \
  '[.[] | select(.ref == "ade-demo-env/tracker#1")] | .[0] | (.children[0].ref == "ade-demo-env/api#1") and (.mrs[0].ref == "ade-demo-env/api!1")'
teammate doctor --json >"$VERIFY_TMP/doctor.json"
expect_json "ade doctor reports no errors" "$VERIFY_TMP/doctor.json" '[.[] | select(.status == "error")] | length == 0'
check "ade doctor exits 0" teammate doctor

step "init never clobbers a config"
before=$(cat "$CFG")
if teammate "${INIT_ARGS[@]}" >"$VERIFY_TMP/rerun.out" 2>&1; then fail "second ade init overwrote an existing config"; fi
grep -q -- '--force' "$VERIFY_TMP/rerun.out" && pass "re-run refused and points at --force" ||
  fail "refusal does not mention --force: $(cat "$VERIFY_TMP/rerun.out")"
[[ $(cat "$CFG") == "$before" ]] && pass "config unchanged by the refused run" || fail "config changed without --force"
check "ade init --force replaces it" teammate "${INIT_ARGS[@]}" --force
ls "$CFG".bak-* >/dev/null 2>&1 && pass "a backup of the previous config was kept" || fail "no backup next to $CFG"
check "the project is still registered once" bash -c "[[ \$(grep -Ec '^\s*\[\[projects\]\]' '$CFG') == 1 ]]"

step "a rejected token writes nothing"
BADHOME=$(mktemp -d)
if env -u XDG_CONFIG_HOME -u XDG_DATA_HOME -u XDG_CACHE_HOME -u ADE_CONFIG HOME="$BADHOME" BAD_TOKEN=glpat-invalid \
  "$ADE" init --yes --forge gitlab --token-env BAD_TOKEN --git-protocol https --project "$MANIFEST" >"$VERIFY_TMP/bad.out" 2>&1; then
  rm -rf "$BADHOME"
  fail "init succeeded with an invalid token"
fi
[[ ! -e $BADHOME/.config/ade/config.toml ]] && pass "no config written for a rejected token" || fail "config written despite the rejected token"
rm -rf "$BADHOME"

step "the real config was not touched"
real_after=$(stat -c '%Y %s' "$REAL_CFG" 2>/dev/null || echo absent)
[[ $real_before == "$real_after" ]] && pass "$REAL_CFG unchanged" || fail "$REAL_CFG changed during verification"

step "release build (goreleaser)"
check "goreleaser check" goreleaser check
check "goreleaser build --snapshot --clean --single-target" \
  goreleaser build --snapshot --clean --single-target --output "$VERIFY_TMP/ade-release"
[[ -x $VERIFY_TMP/ade-release ]] || fail "goreleaser produced no binary"
REL=$("$VERIFY_TMP/ade-release" version)
[[ $REL == "ade 0.0.0-SNAPSHOT-"* || $REL =~ ^ade\ [0-9]+\.[0-9]+\.[0-9]+ ]] &&
  pass "release binary prints the injected version ($REL)" || fail "release binary version = $REL"
check "release binary is statically linked (CGO off)" bash -c "! ldd '$VERIFY_TMP/ade-release' 2>&1 | grep -q 'libc'"

step "flake package and app"
if [[ ${ADE_VERIFY_NIX:-1} == 1 ]]; then
  check "nix build .#default" nix build .#default --no-link
  check "nix run .#default -- version" bash -c "[[ \$(nix run .#default -- version) == ade\ * ]]"
else
  pass "skipped (ADE_VERIFY_NIX=0)"
fi

if [[ -f scripts/verify/phase7-github.sh ]]; then
  # shellcheck source=/dev/null
  . scripts/verify/phase7-github.sh
else
  pass "GitHub checks not present yet (scripts/verify/phase7-github.sh)"
fi

printf '\nPhase 7: all checks passed\n'
