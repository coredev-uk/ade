#!/usr/bin/env bash
# Phase 1 acceptance (docs/PLAN.md §8): read-only Home from the forge.
# Run from the repo root inside `nix develop`. Needs .env with GITLAB_PAT.
. scripts/verify/lib.sh

step "unit and fake-server tests (Free and Premium tiers)"
check "GitLab driver against the fake server, both tiers; GraphQL documents validate against the schema" \
  go test ./internal/forge/...
check "domain: stage, court, facets, weights, iteration window" go test ./internal/domain/...
check "sync: fake server Free (milestones) and Premium (iterations, weights)" go test ./internal/forgesync/...
check "records: notes survive snapshot rewrites" go test ./internal/records/...
check "Home renders from the cache while a sync hangs" go test ./internal/tui/... -run 'TestHome'

step "live sandbox (gitlab.com/ade-demo-env, Free tier)"
build_ade
sandbox_gitlab
check "ade sync exits 0" "$ADE" sync
"$ADE" items --json >"$VERIFY_TMP/items.json"
"$ADE" sync --json >"$VERIFY_TMP/sync.json"
I="$VERIFY_TMP/items.json"
item() { printf '.[] | select(.ref == "ade-demo-env/tracker#%s")' "$1"; }
expect_json "tracker#1: child api#1, MR api!1, review, you · 2 unresolved threads" "$I" \
  "[$(item 1)] | .[0] | (.children[0].ref == \"ade-demo-env/api#1\") and (.mrs[0].ref == \"ade-demo-env/api!1\") and .stage == 5 and .court == \"you\" and .reason == \"2 unresolved threads\""
expect_json "tracker#1: type story, app api, weight 5 from label" "$I" \
  "[$(item 1)] | .[0] | .type == \"story\" and .apps == [\"api\"] and .weight == 5"
expect_json "tracker#2: draft MR, label drift (in review vs in progress)" "$I" \
  "[$(item 2)] | .[0] | .stage == 3 and .drift == true and .workflow == \"in review\" and .want_workflow == \"in progress\""
expect_json "tracker#8: assigned, you · start work" "$I" "[$(item 8)] | .[0] | .stage == 0 and .reason == \"start work\""
expect_json "tracker#5: blocked by workflow label" "$I" "[$(item 5)] | .[0] | .court == \"blocked\""
expect_json "tracker#6: unweighted is null (shown as –), never 0" "$I" "[$(item 6)] | .[0] | .weight == null"
expect_json "tracker#11: needs triage (type, app, weight)" "$I" "[$(item 11)] | .[0] | .triage == [\"type\",\"app\",\"weight\"]"
expect_json "tracker#10: done" "$I" "[$(item 10)] | .[0] | .stage == 7"
expect_json "iteration window from milestones: Sprint 42, 2–13 Nov" "$VERIFY_TMP/sync.json" \
  '.[0].window.current.title == "Sprint 42" and .[0].window.current.start == "2026-11-02" and .[0].window.current.due == "2026-11-13"'
# Expected load straight from the forge: weight::N labels on Sprint 42's issues.
WANT=$(curl -fsS -H "PRIVATE-TOKEN: $GITLAB_PAT" \
  "https://gitlab.com/api/v4/projects/ade-demo-env%2Ftracker/issues?milestone=Sprint%2042&per_page=100" |
  jq '[.[].labels[] | select(startswith("weight::")) | ltrimstr("weight::") | tonumber] | add')
expect_json "sprint weight totals match the forge (${WANT}w across Sprint 42's weighted items)" "$I" \
  "[.[] | select(.sprint.title == \"Sprint 42\" and .weight != null) | .weight] | add == $WANT"

step "notes below the snapshot survive a real rewrite"
REC=$(find "$XDG_DATA_HOME/ade/items" -path '*tracker/1/item.md' | head -1)
[[ -n $REC ]] || fail "item record for tracker#1 not written"
printf '\nremember: ask support about burst size\n' >>"$REC"
# Corrupt the ADE-owned snapshot so the next sync must rewrite it.
perl -0pi -e 's/# Rate-limit the export endpoint/# STALE TITLE/' "$REC"
rm -f "$XDG_CACHE_HOME/ade/cache.db"*
check "re-sync after cache loss" "$ADE" sync
grep -q 'remember: ask support about burst size' "$REC" && pass "user note kept" || fail "user note lost"
grep -q '# Rate-limit the export endpoint' "$REC" && pass "snapshot rewritten" || fail "snapshot not rewritten"

step "read-only shape probe"
check "ade doctor --probe" "$ADE" doctor --probe

printf '\nPhase 1: all checks passed\n'
