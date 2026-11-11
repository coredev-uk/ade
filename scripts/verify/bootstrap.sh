#!/usr/bin/env bash
# `ade project bootstrap` acceptance (docs/manifest.md "Bootstrapping a manifest"): generate a team manifest
# from the GitLab and GitHub sandboxes and prove it is equivalent to the hand-written ones in
# scripts/sandbox/manifests/ on tracker, repos + paths + apps, label patterns, workflow stage map + done,
# iteration source and weight source; tasks, groups and conventions are choices a forge cannot reveal, so
# their differences are reported, not failed.
#
# Run from the repo root inside `nix develop`:
#   bash scripts/verify/bootstrap.sh
# Needs .env with GITLAB_PAT, `gh` logged in (ADE reads its token with `gh auth token`), git and jq. Never
# touches ~/.config/ade: all state is isolated.
#
# Forge writes (sandbox only): one `--publish` on the GitLab sandbox tracker opens (or updates) the merge
# request `ade/bootstrap-manifest`. It is idempotent: a second publish reuses the same merge request and commits
# nothing. The merge request is left open on purpose, so re-runs reuse it instead of piling up closed ones.
. scripts/verify/lib.sh

step "unit and fake-server tests"
check "bootstrap: facet detection, workflow mapping, tracker choice, tasks, GitLab Free + Premium, GitHub, publish" \
  go test ./internal/bootstrap/...
check "drivers: repos, labels, cadences and file commits against both fakes; GraphQL documents validate" \
  go test ./internal/forge/...
check "cli: bootstrap writes a valid manifest, never overwrites, dry runs, publish audit" \
  go test ./internal/cli/ -run Bootstrap

step "live GitLab sandbox (ade-demo-env): generate"
build_ade
sandbox_gitlab
GL_WANT="$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
GL_GEN="$VERIFY_TMP/gen.ade.toml"
check "ade project bootstrap --group ade-demo-env --yes --out gen.ade.toml" \
  "$ADE" project bootstrap --group ade-demo-env --yes --out "$GL_GEN"
[[ -s $GL_GEN ]] && pass "the manifest is written" || fail "no manifest at $GL_GEN"
check "ade config check --manifest validates it" "$ADE" config check --manifest "$GL_GEN"
if "$ADE" project bootstrap --group ade-demo-env --yes --out "$GL_GEN" >"$VERIFY_TMP/clobber.out" 2>&1; then
  fail "a second bootstrap overwrote the manifest"
fi
grep -q -- '--force' "$VERIFY_TMP/clobber.out" && pass "an existing file is never replaced without --force" ||
  fail "refusal does not mention --force: $(cat "$VERIFY_TMP/clobber.out")"

go build -o "$VERIFY_TMP/manifestdiff" ./scripts/verify/manifestdiff
if "$VERIFY_TMP/manifestdiff" "$GL_WANT" "$GL_GEN" >"$VERIFY_TMP/gl.diff"; then
  pass "equivalent to scripts/sandbox/manifests/gitlab.ade.toml on every compared field"
else
  cat "$VERIFY_TMP/gl.diff" >&2
  fail "the generated GitLab manifest differs from the hand-written one"
fi
echo "-- manifestdiff (hand-written vs generated):"
sed 's/^/   /' "$VERIFY_TMP/gl.diff"
echo "-- generated tasks and groups:"
grep -E '^\[repos\.[a-z]+\.tasks|^cmd|^\[groups\]|^dev = |^kind' "$GL_GEN" | sed 's/^/   /'

step "the generated manifest drives a fresh machine: init, sync, items"
NEWHOME=$(mktemp -d)
trap 'rm -rf "$VERIFY_TMP" "$NEWHOME"' EXIT
teammate() { # a new machine: no XDG overrides, no ADE_CONFIG, an empty HOME
  env -u XDG_CONFIG_HOME -u XDG_DATA_HOME -u XDG_CACHE_HOME -u ADE_CONFIG HOME="$NEWHOME" "$ADE" "$@"
}
check "ade init --yes --project gen.ade.toml (token verified, repos cloned, first sync)" \
  teammate init --yes --forge gitlab --token-env GITLAB_PAT --git-protocol https --project "$GL_GEN"
grep -Eq '^\s*\[forges\.sandbox-gitlab\]' "$NEWHOME/.config/ade/config.toml" && pass "forge key taken from the generated manifest" ||
  fail "forge key not taken from the manifest"
check "ade sync exits 0" teammate sync
teammate items --json >"$VERIFY_TMP/items.json"
expect_json "tracker#1: child api#1, MR api!1, in review (stage 5), you · 2 unresolved threads" "$VERIFY_TMP/items.json" \
  '[.[] | select(.ref == "ade-demo-env/tracker#1")] | .[0] | (.children[0].ref == "ade-demo-env/api#1") and (.mrs[0].ref == "ade-demo-env/api!1") and .stage == 5 and .court == "you" and .reason == "2 unresolved threads"'
expect_json "tracker#1: type story, app api, weight 5 (label), from the generated patterns" "$VERIFY_TMP/items.json" \
  '[.[] | select(.ref == "ade-demo-env/tracker#1")] | .[0] | .type == "story" and .apps == ["api"] and .weight == 5'
expect_json "tracker#5 blocked, #10 done, #11 needs triage: the stage map and facets behave as hand-written" "$VERIFY_TMP/items.json" \
  '([.[] | select(.ref == "ade-demo-env/tracker#5")][0].court == "blocked") and ([.[] | select(.ref == "ade-demo-env/tracker#10")][0].stage == 7) and ([.[] | select(.ref == "ade-demo-env/tracker#11")][0].triage == ["type","app","weight"])'

step "publish: a dry run first, then one real publish on the sandbox tracker"
GL_AUDIT=$XDG_DATA_HOME/ade/audit
"$ADE" project bootstrap --group ade-demo-env --yes --out "$VERIFY_TMP/pub.ade.toml" --publish --dry-run >"$VERIFY_TMP/dry.out"
grep -q 'publish (dry run: nothing is written)' "$VERIFY_TMP/dry.out" && grep -q 'title: Add the ADE project manifest' "$VERIFY_TMP/dry.out" &&
  grep -q -- "ade init --project 'git+https://gitlab.com/ade-demo-env/tracker.git#.ade.toml'" "$VERIFY_TMP/dry.out" &&
  pass "--publish --dry-run shows the merge request it would open" || { cat "$VERIFY_TMP/dry.out" >&2; fail "dry run output"; }
sed -n '/^publish (dry run/,/^$/p' "$VERIFY_TMP/dry.out" | sed 's/^/   /'
[[ ! -e $VERIFY_TMP/pub.ade.toml ]] && pass "--dry-run wrote no local file" || fail "--dry-run wrote a file"
[[ $("$ADE" audit --action 'forge.*' --json | jq '[.[] | select(.action != "forge.mr.create" or .dry_run != true)] | length') == 0 ]] &&
  pass "the dry run made no audited forge write" || fail "dry run audited a write"

gl() { curl -fsS -H "PRIVATE-TOKEN: $GITLAB_PAT" "https://gitlab.com/api/v4$1"; }
open_mrs() { gl "/projects/ade-demo-env%2Ftracker/merge_requests?state=opened&source_branch=ade%2Fbootstrap-manifest" | jq length; }
"$ADE" project bootstrap --group ade-demo-env --yes --out "$VERIFY_TMP/pub.ade.toml" --publish --json >"$VERIFY_TMP/pub1.json" 2>"$VERIFY_TMP/pub1.err" ||
  { cat "$VERIFY_TMP/pub1.err" >&2; fail "ade project bootstrap --publish"; }
MR1=$(jq -r '.published.mr.ref' "$VERIFY_TMP/pub1.json")
[[ $MR1 == ade-demo-env/tracker!* ]] && pass "merge request $MR1 is open on the tracker" || fail "no merge request in the result"
[[ $(open_mrs) == 1 ]] && pass "exactly one open merge request for ade/bootstrap-manifest" || fail "open merge requests: $(open_mrs)"
gl "/projects/ade-demo-env%2Ftracker/repository/files/.ade.toml/raw?ref=ade%2Fbootstrap-manifest" >"$VERIFY_TMP/branch.ade.toml"
cmp -s "$VERIFY_TMP/branch.ade.toml" "$VERIFY_TMP/pub.ade.toml" && pass "the branch carries exactly the generated manifest" || fail "branch content differs from the local file"
check "the published manifest validates" "$ADE" config check --manifest "$VERIFY_TMP/branch.ade.toml"

# A second publish. The sandbox is shared, so its issues can move between the two runs and change what is
# generated (a count in a comment); the idempotency claim is therefore: same merge request, no new
# branch or MR, and a commit only when the generated manifest actually changed.
W1=$("$ADE" audit --action 'forge.*' --json | jq 'length')
C1=$("$ADE" audit --action forge.file.commit --json | jq length)
"$ADE" project bootstrap --group ade-demo-env --yes --out "$VERIFY_TMP/pub2.ade.toml" --publish --json >"$VERIFY_TMP/pub2.json" 2>"$VERIFY_TMP/pub2.err" ||
  { cat "$VERIFY_TMP/pub2.err" >&2; fail "second publish"; }
[[ $(jq -r '.published.mr.ref' "$VERIFY_TMP/pub2.json") == "$MR1" ]] && pass "a second publish reuses $MR1" || fail "second publish opened another merge request"
[[ $(open_mrs) == 1 ]] && pass "still exactly one open merge request" || fail "open merge requests after the re-run: $(open_mrs)"
W2=$("$ADE" audit --action 'forge.*' --json | jq 'length')
if cmp -s "$VERIFY_TMP/pub.ade.toml" "$VERIFY_TMP/pub2.ade.toml"; then
  [[ $W2 == "$W1" ]] && pass "the manifest is unchanged, so the re-run wrote nothing to the forge (audit)" || fail "the re-run audited a forge write"
else
  [[ $W2 == $((W1 + 1)) && $("$ADE" audit --action forge.file.commit --json | jq length) == $((C1 + 1)) ]] &&
    pass "the forge changed in between: exactly one more commit, on the same merge request" || fail "re-run wrote $((W2 - W1)) forge actions for a changed manifest"
fi

step "live GitHub sandbox (coredev-uk, ade-sandbox-*): generate"
command -v gh >/dev/null || fail "gh is not installed"
gh auth status >/dev/null 2>&1 || fail "gh is not logged in (gh auth login)"
GH_DIR="$VERIFY_TMP/github"
mkdir -p "$GH_DIR"
export XDG_DATA_HOME="$GH_DIR/data" XDG_CACHE_HOME="$GH_DIR/cache" ADE_CONFIG="$GH_DIR/config.toml"
cat >"$ADE_CONFIG" <<EOF
[user]
handle = "sandbox"
[forges.sandbox-github]
kind = "github"
token_cmd = "gh auth token"
git_protocol = "ssh"
[paths]
root = "$GH_DIR/projects"
EOF
GH_WANT="$REPO/scripts/sandbox/manifests/github.ade.toml"
GH_GEN="$GH_DIR/gen.ade.toml"
check "ade project bootstrap --group coredev-uk --match 'ade-sandbox-*' --yes" \
  "$ADE" project bootstrap --group coredev-uk --match 'ade-sandbox-*' --yes --out "$GH_GEN"
check "ade config check --manifest validates it" "$ADE" config check --manifest "$GH_GEN"
if "$VERIFY_TMP/manifestdiff" "$GH_WANT" "$GH_GEN" >"$VERIFY_TMP/gh.diff"; then
  pass "equivalent to scripts/sandbox/manifests/github.ade.toml on every compared field"
else
  cat "$VERIFY_TMP/gh.diff" >&2
  fail "the generated GitHub manifest differs from the hand-written one"
fi
echo "-- manifestdiff (hand-written vs generated):"
sed 's/^/   /' "$VERIFY_TMP/gh.diff"
grep -q 'MR templates found in' "$GH_GEN" && pass "the pull request template is noted in the manifest" || fail "no template note"
[[ $("$ADE" audit --action 'forge.*' --json | jq 'length') == 0 ]] && pass "reading GitHub made no forge write" || fail "GitHub bootstrap wrote to the forge"

printf '\nbootstrap: all checks passed\n'
