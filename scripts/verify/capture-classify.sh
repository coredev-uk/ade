#!/usr/bin/env bash
# Capture the Classify dialog (`c` on a Home row, PLAN 6.1) for visual review
# with VHS. Output: .impeccable/review/classify-*.png
#
#   scripts/verify/capture-classify.sh        # desktop, narrow, no-colour sets
#
# States: reading the issue (a forge that answers slowly, so the dialog is
# still resolving) and thinking (an omp stub that sleeps), the AI proposal
# (cost line, ticked labels, "why", an ignored value), the pre-flight ask
# (ai.mode = ask with a tiny confirm threshold), the keyword proposal (AI
# off), nothing to classify (an item that has both facets), done, nothing
# ticked, and failure (omp not installed). The fake GitLab serves tracker#11
# with no labels; nothing is sent anywhere and no label is ever written to a
# real forge.
. scripts/verify/capture-ai-lib.sh
ai_capture_init classify
seed_ai

# Confirming a proposal writes labels to the fake GitLab, and every answer is
# cached as an artifact: after each tape take the labels off, drop the AI
# artifacts and the cache, and sync again, so each set starts from the
# untriaged #11 with nothing cached.
reset_state() {
  curl -fsS -X PUT -H "PRIVATE-TOKEN: $FAKE_GITLAB_TOKEN" \
    "$FAKE_URL/api/v4/projects/ade-demo-env%2Ftracker/issues/11" \
    --data-urlencode "remove_labels=type::story,type::bug,type::chore,app::api,app::web" >/dev/null
  rm -rf "$XDG_DATA_HOME/ade/items" "$XDG_CACHE_HOME"/ade/cache.db*
  "$ADE" sync >/dev/null
}
AFTER_CAPTURE=reset_state

# On Home the sixth row is #11 (no type, no app): five `j` get there. The first
# row, #1, has both facets.
to_triage() { printf 'Type "jjjjj"\nSleep 500ms\n'; }

ai_proposal() {
  to_triage
  printf 'Type "c"\n'
  shot thinking 1500ms
  shot proposal 6s
  printf 'Type "j"\nType "x"\n'
  shot unticked 500ms
  printf 'Type "x"\nEnter\n'
  shot done 1500ms
}
# The slow stub keeps the dialog on "thinking" long enough to see it.
use_config slow auto 5.00 "$OMP_SLOW"
sets ai_proposal

# Every answer of this forge takes two seconds, so the dialog is still on
# "reading <ref>" when the frame is taken.
start_service FORGE_SLOW fakegl -tier premium -latency 2s
reading() {
  to_triage
  printf 'Type "c"\n'
  shot reading 700ms
}
FORGE_URL=$FORGE_SLOW use_config reading auto 5.00 "$OMP_OK"
sets reading

ask() {
  to_triage
  printf 'Type "c"\n'
  shot ask 2000ms
  printf 'Type "y"\n'
  shot proposal-after-yes 2500ms
}
use_config ask ask 5.00 "$OMP_OK"
sed -i 's/^confirm_over_usd = .*/confirm_over_usd = 0.0001/' "$ADE_CONFIG"
sets ask

keywords() {
  to_triage
  printf 'Type "c"\n'
  shot proposal-keywords 2000ms
  printf 'Type "x"\nEnter\n'
  shot nothing-ticked 500ms
}
use_config off off 5.00
sets keywords

nothing() {
  printf 'Type "c"\n'
  shot nothing-to-classify 2000ms
}
# Home's first row (#1) is a labelled item.
use_config ok auto 5.00 "$OMP_OK"
sets nothing

failed() {
  to_triage
  printf 'Type "c"\n'
  shot failed 2500ms
}
use_config gone auto 5.00 "$OMP_GONE"
sets failed
verify_shots
ls "$OUT"/classify-*.png
