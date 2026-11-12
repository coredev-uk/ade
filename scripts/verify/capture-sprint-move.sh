#!/usr/bin/env bash
# Capture the sprint-move dialog (`>` on a Home row, and `>`/`M` on the Sprint
# review page) for visual review with VHS. Output: .impeccable/review/sprintmove-*.png
#
#   scripts/verify/capture-sprint-move.sh        # desktop, narrow, no-colour sets
#
# States: the dialog reading, the confirm list on a Free forge (milestones, the
# child following; the child left alone), the written result, the "no next
# sprint" error after moving to the last one, the confirm on a Premium forge (state "iteration")
# (iterations), and the Sprint review page with its selected carried item, the
# `>` dialog for it and the `M` dialog for every unfinished item of yours.
# Data: the in-process fake GitLab in both tiers. Nothing leaves the machine
# and no real forge is written. The fakes' sprints are 2026-11-02..11-13 (42)
# and 11-16..11-27 (43): run it while 42 is current (the sandbox calendar).
. scripts/verify/capture-ai-lib.sh
ai_capture_init sprintmove
start_service FREE_URL fakegl -tier free
start_service AGILE_URL fakegl -tier premium -agile
G=ade-demo-env

# use_variant NAME URL MANIFEST: a config for one forge and manifest.
use_variant() {
  local name=$1 url=$2 manifest=$3
  FORGE_URL=$url use_config "$name" off 2.00 "$OMP_OK"
  sed -i "s#$REPO/scripts/sandbox/manifests/gitlab.ade.toml#$manifest#" "$ADE_CONFIG"
}

sed -e 's/^weight .*/weight     = { source = "auto" }/' \
  -e 's/^iterations = .*/iterations = { source = "gitlab-iterations", cadence = "Delivery" }/' \
  scripts/sandbox/manifests/gitlab.ade.toml >"$VERIFY_TMP/premium.ade.toml"

api() { # URL METHOD PATH [curl args]
  local url=$1 method=$2 path=$3
  shift 3
  curl -fsS -X "$method" -H "PRIVATE-TOKEN: $FAKE_GITLAB_TOKEN" "$url/api/v4/$path" "$@"
}

# reset_free: tracker#1 and its child api#1 back in Sprint 42, fresh cache.
reset_free() {
  local id
  id=$(api "$FREE_URL" GET "groups/$G/milestones" | jq -r '.[] | select(.title == "Sprint 42") | .id')
  api "$FREE_URL" PUT "projects/$G%2Ftracker/issues/1" -d "milestone_id=$id" >/dev/null
  api "$FREE_URL" PUT "projects/$G%2Fapi/issues/1" -d "milestone_id=$id" >/dev/null
  rm -rf "$XDG_DATA_HOME/ade/items" "$XDG_CACHE_HOME"/ade/cache.db*
  ADE_CONFIG=$FREE_CONFIG "$ADE" sync >/dev/null
}

use_variant free "$FREE_URL" "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
FREE_CONFIG=$ADE_CONFIG
BEFORE_CAPTURE=reset_free
move_free() {
  printf 'Type ">"\n'
  shot reading 150ms
  shot confirm 1500ms
  printf 'Type "c"\n'
  shot child-left-alone 500ms
  printf 'Type "c"\nEnter\n'
  shot done 1800ms
  printf 'Escape\nSleep 3000ms\nType ">"\n'
  shot no-next 1800ms
  printf 'Escape\n'
}
sets move_free

BEFORE_CAPTURE=
use_variant premium "$FAKE_URL" "$VERIFY_TMP/premium.ade.toml"
PREMIUM_CONFIG=$ADE_CONFIG
rm -rf "$XDG_DATA_HOME/ade/items" "$XDG_CACHE_HOME"/ade/cache.db*
ADE_CONFIG=$PREMIUM_CONFIG "$ADE" sync >/dev/null
move_premium() {
  printf 'Type ">"\n'
  shot iteration 1800ms
  printf 'Escape\n'
}
sets move_premium

use_variant agile "$AGILE_URL" "$VERIFY_TMP/premium.ade.toml"
rm -rf "$XDG_DATA_HOME/ade/items" "$XDG_CACHE_HOME"/ade/cache.db*
"$ADE" sync >/dev/null
move_review() {
  printf 'Type "V"\nSleep 3500ms\nType "J"\nSleep 300ms\nType "K"\n'
  shot selected 300ms # J then K: the page has scrolled to the first carried item
  printf 'Type "J"\n'
  shot next-item 500ms
  printf 'Type ">"\n'
  shot one 1800ms
  printf 'Escape\nSleep 300ms\nType "M"\n'
  shot mine 1800ms
  printf 'Escape\n'
}
sets move_review
verify_shots
ls "$OUT"/sprintmove-*.png
