#!/usr/bin/env bash
# Round 3: self-hosted GitLab and headless CDE. Proves, with the in-process fake
# GitLab served over TLS under a path prefix (self-signed CA), that:
#   - the GitLab driver trusts ca_file, honours SSL_CERT_FILE and the URL prefix
#   - version gating and 404 degradation never crash a read
#   - clone URLs, git CA settings and manifest fetch work for prefix/port/CA
#   - the editor resolves the VS Code remote CLI (and nothing in a web tty)
#   - a worktree's .envrc is entered by `ade task run`-style task execution (real direnv)
# Run from the repo root inside `nix develop`.
. "$(dirname "$0")/lib.sh"
cd "$REPO"

step "GitLab driver: CA file, path prefix, version, degradation, concurrency"
check "self-hosted fake over TLS + prefix" \
  go test ./internal/forge/gitlab/ -count=1 \
  -run 'TestOpenTrustsCAFileUnderPathPrefix|TestPathPrefixIsRequired|TestVersion|TestParseVersion|TestOldInstanceDegrades|TestOpenMaxConcurrency|TestPacingWithRetryAfterOnly'

step "CA pool and SSL_CERT_FILE"
check "tlsca" go test ./internal/tlsca/ -count=1

step "clone URL matrix, git CA env, manifest over https with CA"
check "project" go test ./internal/project/ -count=1 -run 'TestCloneURL|TestGitClientCarriesForgeCA|TestManifestFromHTTPSWithCA'
check "git env" go test ./internal/git/ -count=1

step "ade init renders ca_file and ssh_port"
check "onboard" go test ./internal/onboard/ ./internal/cli/ -count=1 -run 'Render|Gather|Init'

step "editor matrix: VS Code remote CLI, stale IPC socket, web tty"
check "editor" go test ./internal/editor/ -count=1

step "devshell: direnv-wrapped task sees the .envrc variable"
if command -v direnv >/dev/null; then
  check "direnv wraps task execution" go test ./internal/workspace/ -count=1 -run 'TestTaskRunsInsideDirenv|TestTaskDevshellNoneIsRespected|TestBuildLayoutRunsPanesInTheDevShell|TestServicesOutsideReposAreNotWrapped'
else
  fail "direnv is not on PATH: the devshell proof needs it"
fi
check "devshell detection" go test ./internal/devshell/ -count=1

step "doctor: version, ca_file, cde group"
check "doctor" go test ./internal/doctor/ -count=1

step "palette: every key of the MR, Worktrees and Home help is reachable by name"
check "palette" go test ./internal/tui/ -count=1 -run 'Palette|HelpListsEveryHomeKey'

step "end to end: ade init against the fake over https, self-signed CA, /gitlab prefix"
build_ade
go build -o "$VERIFY_TMP/fakegl" ./scripts/verify/fakegl
E=$VERIFY_TMP/e2e
mkdir -p "$E/remotes" "$E/home"
"$VERIFY_TMP/fakegl" -tier premium -tls -path-prefix /gitlab -ca-out "$E/ca.pem" -version 16.11.2-ee \
  -git-root "$E/remotes" -git-apps "$REPO/scripts/sandbox/apps" >"$E/gl.url" 2>"$E/gl.err" &
FAKE_PID=$!
trap 'kill $FAKE_PID 2>/dev/null || true; rm -rf "$VERIFY_TMP"' EXIT
for _ in $(seq 50); do [[ -s $E/gl.url ]] && break; sleep 0.1; done
URL=$(head -1 "$E/gl.url")
[[ $URL == https://127.0.0.1:*/gitlab ]] || fail "unexpected fake URL '$URL': $(cat "$E/gl.err")"
HOSTPATH=${URL#https://}
sed -e 's/^weight .*/weight     = { source = "gitlab" }/' \
  -e 's/^iterations = .*/iterations = { source = "milestones", overlap = 10 }/' \
  scripts/sandbox/manifests/gitlab.ade.toml >"$E/manifest.ade.toml"
# A fresh machine: only HOME is set; no SSL_CERT_FILE, so only ca_file can make TLS work.
eade() {
  env -u XDG_CONFIG_HOME -u XDG_DATA_HOME -u XDG_CACHE_HOME -u ADE_CONFIG -u SSL_CERT_FILE -u GIT_SSL_CAINFO \
    HOME="$E/home" FAKE_GITLAB_TOKEN=fake-token \
    GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid \
    "$ADE" "$@"
}
check "without the CA, init is refused" bash -c "! env -u SSL_CERT_FILE -u XDG_CONFIG_HOME -u ADE_CONFIG \
  HOME='$E/nohome' FAKE_GITLAB_TOKEN=fake-token '$ADE' init --yes --forge gitlab --host '$HOSTPATH' \
  --token-env FAKE_GITLAB_TOKEN --git-protocol https --handle sandbox --root '$E/nohome/ade' >/dev/null 2>&1"
check "ade init --yes --host HOST/gitlab --ca-file CA --git-protocol https" \
  eade init --yes --forge gitlab --host "$HOSTPATH" --ca-file "$E/ca.pem" --token-env FAKE_GITLAB_TOKEN \
  --git-protocol https --handle sandbox --root "$E/home/ade" --project "$E/manifest.ade.toml"
CFG=$E/home/.config/ade/config.toml
check "config keeps the prefix and the CA path" bash -c "grep -q 'url = \"$URL\"' '$CFG' && grep -q 'ca_file = \"$E/ca.pem\"' '$CFG'"
check "git clone went through the CA over https (baseline worktrees exist)" \
  bash -c "test -d '$E/home/ade/demo-gl/api/main' && test -d '$E/home/ade/demo-gl/web/main'"
check "the clone's origin is the prefixed https URL" \
  bash -c "git -C '$E/home/ade/demo-gl/api/main' remote get-url origin | grep -q '^$URL/'"
check "ade sync" eade sync
eade doctor >"$E/doctor.txt" 2>&1 || {
  cat "$E/doctor.txt" >&2
  fail "ade doctor reported errors"
}
check "doctor prints the GitLab version" grep -q '16\.11\.2' "$E/doctor.txt"
check "doctor checks ca_file" grep -q 'ca_file' "$E/doctor.txt"
kill "$FAKE_PID" 2>/dev/null || true

echo
echo "r3-selfhosted-cde: all checks passed"
