#!/usr/bin/env bash
# Capture the Notes dialog (`n`) and the Weight dialog (`=`) on a Home row, and
# the weight suggestion in the Classify dialog (`c`), for visual review with
# VHS. Output: .impeccable/review/noteweight-*.png
#
#   scripts/verify/capture-notes-weight.sh        # desktop, narrow, no-colour sets
#
# States: notes (with text, empty, after the editor saved), weight on a
# Premium forge (the weight field, the child following), weight on a Free forge
# (the label swapped), the suggestion pre-filled, the written result, and the
# Classify dialog with a suggested weight. Data: the in-process fake GitLab in
# both tiers; the "editor" is a script that appends a line. Nothing leaves the
# machine and no real forge is written.
. scripts/verify/capture-ai-lib.sh
ai_capture_init noteweight
go build -o "$VERIFY_TMP/fakegl" ./scripts/verify/fakegl
start_service FREE_URL fakegl -tier free
G=ade-demo-env

# The "editor": appends a line to the record it is given.
cat >"$VERIFY_TMP/editor.sh" <<'EOF'
#!/bin/sh
printf '\n- added in the editor: ask support before changing the limit\n' >>"$1"
EOF
cat >"$VERIFY_TMP/seed-notes.sh" <<'EOF'
#!/bin/sh
cat >>"$1" <<'NOTES'
Support wants the limit per tenant, not per token (50 req/min).

- check how the dashboard shows the remaining quota
- the legacy CSV path stays until Friday's release
NOTES
EOF
cat >"$VERIFY_TMP/seed-long.sh" <<'EOF'
#!/bin/sh
for i in $(seq 1 60); do printf 'Line %d of a long working note: the legacy CSV path stays until the release.\n' "$i" >>"$1"; done
EOF
chmod +x "$VERIFY_TMP/editor.sh" "$VERIFY_TMP/seed-notes.sh" "$VERIFY_TMP/seed-long.sh"

# use_variant NAME URL MANIFEST: a config for one forge and manifest, with the
# editor the tape can drive.
use_variant() {
  local name=$1 url=$2 manifest=$3
  FORGE_URL=$url use_config "$name" "${AI_MODE:-off}" 2.00 "$OMP_OK"
  sed -i "s#$REPO/scripts/sandbox/manifests/gitlab.ade.toml#$manifest#" "$ADE_CONFIG"
  printf '\n[host]\neditor = "$EDITOR"\n' >>"$ADE_CONFIG"
  sed -i "s#^exec #export VISUAL=$VERIFY_TMP/editor.sh\nexec #" "$VERIFY_TMP/run.sh"
}

sed 's/^weight .*/weight     = { source = "auto" }/' scripts/sandbox/manifests/gitlab.ade.toml >"$VERIFY_TMP/auto.ade.toml"

api() { # URL METHOD PATH [curl args]
  local url=$1 method=$2 path=$3
  shift 3
  curl -fsS -X "$method" -H "PRIVATE-TOKEN: $FAKE_GITLAB_TOKEN" "$url/api/v4/$path" "$@" >/dev/null
}
gql_weight() { # URL IID WEIGHT|null
  curl -fsS -H "PRIVATE-TOKEN: $FAKE_GITLAB_TOKEN" -H 'Content-Type: application/json' "$1/api/graphql" \
    -d "{\"query\":\"mutation(\$p: ID!, \$i: String!, \$w: Int) { issueSetWeight(input:{projectPath:\$p, iid:\$i, weight:\$w}) { errors } }\",\"variables\":{\"p\":\"$G/$3\",\"i\":\"$2\",\"w\":$4}}" >/dev/null
}

# reset_premium: tracker#1 and its child api#1 at weight 3 again, fresh records
# with the seeded notes.
reset_premium() {
  gql_weight "$FAKE_URL" 1 tracker 3
  gql_weight "$FAKE_URL" 1 api 3
  rm -rf "$XDG_DATA_HOME/ade/items" "$XDG_CACHE_HOME"/ade/cache.db*
  ADE_CONFIG=$PREMIUM_CONFIG "$ADE" sync >/dev/null
  ADE_CONFIG=$PREMIUM_CONFIG VISUAL=$VERIFY_TMP/seed-notes.sh "$ADE" item "$G/tracker#1" --edit >/dev/null
  ADE_CONFIG=$PREMIUM_CONFIG VISUAL=$VERIFY_TMP/seed-long.sh "$ADE" item "$G/tracker#3" --edit >/dev/null
}

# Two closed bugs on the web app with weights and title words in common with
# tracker#11, so a suggestion has something to compare with.
for t in "Exports page totals drift on filter" "Stale totals in the exports table"; do
  iid=$(curl -fsS -X POST -H "PRIVATE-TOKEN: $FAKE_GITLAB_TOKEN" "$FREE_URL/api/v4/projects/$G%2Ftracker/issues" \
    --data-urlencode "title=$t" --data-urlencode "labels=type::bug,app::web,weight::3" | jq -r .iid)
  api "$FREE_URL" PUT "projects/$G%2Ftracker/issues/$iid" -d state_event=close
done

# reset_free: tracker#1 and api#1 on weight::5 labels, two closed bugs with
# weights to compare with, tracker#11 labelled so only its weight is open.
reset_free() {
  local f=$FREE_URL
  api "$f" PUT "projects/$G%2Ftracker/issues/1" --data-urlencode "remove_labels=weight::1,weight::2,weight::3,weight::5,weight::8"
  api "$f" PUT "projects/$G%2Ftracker/issues/1" --data-urlencode "add_labels=weight::5"
  api "$f" PUT "projects/$G%2Fapi/issues/1" --data-urlencode "remove_labels=weight::1,weight::2,weight::3,weight::5,weight::8"
  api "$f" PUT "projects/$G%2Fapi/issues/1" --data-urlencode "add_labels=weight::5"
  api "$f" PUT "projects/$G%2Ftracker/issues/9" --data-urlencode "add_labels=weight::1"
  api "$f" PUT "projects/$G%2Ftracker/issues/10" --data-urlencode "add_labels=weight::2"
  api "$f" PUT "projects/$G%2Ftracker/issues/11" --data-urlencode "remove_labels=weight::1,weight::2,weight::3,weight::5,weight::8"
  api "$f" PUT "projects/$G%2Ftracker/issues/11" --data-urlencode "add_labels=type::bug,app::web"
  rm -rf "$XDG_DATA_HOME/ade/items" "$XDG_CACHE_HOME"/ade/cache.db*
  ADE_CONFIG=$FREE_CONFIG "$ADE" sync >/dev/null
}

notes_body() {
  printf 'Type "n"\n'
  shot notes 900ms
  printf 'Escape\nSleep 400ms\nType "j"\nSleep 400ms\nType "n"\n'
  shot empty 700ms
  printf 'Escape\nSleep 400ms\nType "k"\nSleep 400ms\nType "n"\nSleep 400ms\nType "e"\n'
  shot after-edit 1200ms
  printf 'Escape\nSleep 400ms\nType "jj"\nSleep 400ms\nType "n"\n'
  shot long 900ms
  printf 'Type "jjjjjj"\n'
  shot long-scrolled 500ms
  printf 'Escape\n'
}
use_variant premium "$FAKE_URL" "$VERIFY_TMP/auto.ade.toml"
PREMIUM_CONFIG=$ADE_CONFIG
BEFORE_CAPTURE=reset_premium
sets notes_body

weight_premium() {
  printf 'Type "="\n'
  shot field-premium 1200ms
  printf 'Type "5"\n'
  shot typed 500ms
  printf 'Type "c"\n'
  shot child-left-alone 500ms
  printf 'Type "c"\nEnter\n'
  shot done 1500ms
  printf 'Escape\nSleep 3000ms\nType "="\nSleep 600ms\nType "5"\n'
  shot same-value 500ms
  printf 'Enter\n'
  shot unchanged 1500ms
  printf 'Escape\n'
}
sets weight_premium

# The Free forge: the label path, with the closed issues the suggestion needs.
use_variant free "$FREE_URL" "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
FREE_CONFIG=$ADE_CONFIG
BEFORE_CAPTURE=reset_free
weight_free() {
  printf 'Type "="\n'
  shot field-free 1200ms
  printf 'Type "2"\n'
  shot label-swap 500ms
  printf 'Type "n"\n'
  shot none 400ms
  printf 'Escape\nSleep 400ms\nType "jjjjj"\nSleep 400ms\nType "="\nSleep 2500ms\nEscape\nSleep 1500ms\nType "="\n'
  shot prefilled 1200ms
  printf 'Escape\n'
}
sets weight_free

# Tracker#11 with no type or app label: the weight dialog says why nothing is suggested.
reset_free_unclassified() {
  reset_free
  api "$FREE_URL" PUT "projects/$G%2Ftracker/issues/11" --data-urlencode "remove_labels=type::bug,app::web"
  rm -rf "$XDG_DATA_HOME/ade/items" "$XDG_CACHE_HOME"/ade/cache.db*
  ADE_CONFIG=$FREE_CONFIG "$ADE" sync >/dev/null
}
BEFORE_CAPTURE=reset_free_unclassified
weight_none() {
  printf 'Type "jjjjj"\nSleep 400ms\nType "="\n'
  shot no-suggestion 1200ms
  printf 'Escape\n'
}
sets weight_none

# The classify suggestion comes with the model's type and app (the omp stub).
AI_MODE=auto use_variant freeai "$FREE_URL" "$REPO/scripts/sandbox/manifests/gitlab.ade.toml"
FREE_CONFIG=$ADE_CONFIG
BEFORE_CAPTURE=reset_free_unclassified
classify_weight() {
  # The first dialog starts the closed-issue history read in the background; the
  # second one has it (the suggestion never waits for the forge).
  printf 'Type "jjjjj"\nSleep 400ms\nType "c"\nSleep 2500ms\nEscape\nSleep 1500ms\nType "c"\n'
  shot suggestion 1500ms
  printf 'Type "="\n'
  shot handover 1200ms
  printf 'Escape\n'
}
sets classify_weight
verify_shots
ls "$OUT"/noteweight-*.png
