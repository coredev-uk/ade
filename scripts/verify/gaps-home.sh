#!/usr/bin/env bash
# Gap round, docs/PLAN.md 6.1 Home: search, palette, filter picker, r/o/p/n/=,
# Activity, previous sprint, three regions, row badges, load vs your average.
# Run from the repo root inside `nix develop`. The live step needs .env with
# GITLAB_PAT; it reads only (`ade items --json`).
. scripts/verify/lib.sh

step "model tests: each key and section"
check "search: fuzzy filter as you type, esc clears, enter keeps; forge search is debounced, async, asked once" \
  go test ./internal/tui/ -run 'TestSearch|TestFuzzy'
check "palette (:) lists every action and runs it; picker (f) offers type/app/workflow values" \
  go test ./internal/tui/ -run 'TestPalette|TestFilterPicker'
check "p project filter, o browser (plain host prints the URL), r refresh or restart blocked subtree" \
  go test ./internal/tui/ -run 'TestProjectFilter|TestOOpensBrowser|TestRRefreshes|TestRWithout'
check "Activity collapsed by default, Previous sprint section, row badge and epic, three regions" \
  go test ./internal/tui/ -run 'TestActivity|TestPreviousSprint|TestRowBadge|TestThreeRegion|TestNarrowHomeWithSearch'
check "header: committed, done, left and your average" go test ./internal/tui/ -run 'TestSprintRule|TestProjectRule|TestHome'
check "your per-sprint average counts only your issues (agile)" go test ./internal/agile/ -run TestBuildMyVelocity
check "help lists every Home key" go test ./internal/tui/ -run TestHelpListsEveryHomeKey

step "live: Home's inputs from the sandbox (read-only)"
build_ade
sandbox_gitlab
check "ade sync exits 0" "$ADE" sync
"$ADE" items --json >"$VERIFY_TMP/items.json"
"$ADE" sync --json >"$VERIFY_TMP/sync.json"
I="$VERIFY_TMP/items.json"
expect_json "tracker#1 has an open MR with 2 unresolved threads (the row's thread count; the sandbox has no CI, so the pipeline glyph is covered by the model tests)" "$I" \
  '[.[] | select(.ref == "ade-demo-env/tracker#1")] | .[0] | [.mrs[] | select(.state == "opened")] | (map(.unresolved) | add) == 2'
expect_json "items carry type, app and workflow values for the filter picker" "$I" \
  '([.[] | select(.type != null and .type != "")] | length) > 0 and ([.[] | select(.workflow != null and .workflow != "")] | length) > 0'
expect_json "the window has a current sprint (the header load needs it)" "$VERIFY_TMP/sync.json" \
  '.[0].window.current.title != null'

printf '\nHome gaps: all checks passed\n'
