#!/usr/bin/env bash
# Phase 5 acceptance (docs/PLAN.md §8): standup, sprint review, velocity, exports.
# Run from the repo root inside `nix develop`. Needs .env with GITLAB_PAT.
. scripts/verify/lib.sh

step "unit and fake-server tests"
check "calendar: a Monday looks back to Friday; holidays and custom workdays" go test ./internal/agile/ -run 'PrevWorkday|NewCalendar|WorkdaysBetween'
check "standup over a Monday uses Friday (fixed clock), folds feed, cache, audit" go test ./internal/agile/ -run 'Standup'
check "review: committed/added/done/carried by count and weight, kept-open done, burn-down" go test ./internal/agile/ -run 'Review'
check "velocity: an unweighted sprint is left out of the average, never a zero" go test ./internal/agile/ -run 'Velocity|ZeroWeight'
check "review against the Free and Premium fake GitLab" go test ./internal/agile/ -run 'Fake|AgainstPremium|AgainstFree|ScopeAdded'
check "GitLab driver: label, milestone and iteration events, project events" go test ./internal/forge/gitlab/ -run 'LabelEvents|SprintEvents|ProjectEvents|EventsOf'
check "OSC 52 copy helper" go test ./internal/clipboard/...
check "Standup and Sprint review pages: glyph sets, widths, copy, keys" go test ./internal/tui/ -run 'Standup|Agile|ReviewPage|ReviewKeys|Scroll|Stale|CopyStandup|KeysOpen'

step "live sandbox (gitlab.com/ade-demo-env, Free tier)"
build_ade
sandbox_gitlab
check "ade sync exits 0" "$ADE" sync

T="ade-demo-env%2Ftracker"
api() { curl -fsS -H "PRIVATE-TOKEN: $GITLAB_PAT" "https://gitlab.com/api/v4/$1"; }

step "standup: the previous working day"
standup_day() { "$ADE" standup --json --date "$1" | jq -r .yesterday; }
[[ $(standup_day 2026-11-09) == 2026-11-06 ]] && pass "Monday 2026-11-09 -> Friday 2026-11-06" || fail "Monday did not use Friday"
[[ $(standup_day 2026-11-10) == 2026-11-09 ]] && pass "Tuesday 2026-11-10 -> Monday 2026-11-09" || fail "Tuesday did not use Monday"
[[ $(standup_day 2026-11-14) == 2026-11-13 ]] && pass "Saturday 2026-11-14 -> Friday 2026-11-13" || fail "Saturday did not use Friday"
"$ADE" standup --json --date 2026-11-09 >"$VERIFY_TMP/standup.json"
expect_json "standup JSON: date, sections are lists" "$VERIFY_TMP/standup.json" \
  '.date == "2026-11-09" and (.done | type == "array") and (.today | type == "array") and (.blockers | type == "array")'
expect_json "today lists the sandbox items in your court, none done" "$VERIFY_TMP/standup.json" \
  '[.today[] | select(.ref == "ade-demo-env/tracker#1")] | length == 1'
expect_json "blockers include tracker#5 (blocked label)" "$VERIFY_TMP/standup.json" \
  '[.blockers[] | select(.ref == "ade-demo-env/tracker#5")] | length >= 1'
"$ADE" standup --date 2026-11-09 >"$VERIFY_TMP/standup.txt"
grep -q 'Yesterday · Fri 6 Nov' "$VERIFY_TMP/standup.txt" && pass "text output names Fri 6 Nov" || fail "text output lacks Fri 6 Nov"
"$ADE" standup --md --date 2026-11-09 >"$VERIFY_TMP/standup.md"
grep -q '^## Today' "$VERIFY_TMP/standup.md" && pass "--md is Markdown" || fail "--md has no Markdown headings"
if ! "$ADE" standup --date nonsense >/dev/null 2>&1; then pass "a bad --date is rejected"; else fail "bad --date accepted"; fi

step "sprint review: totals match the forge"
"$ADE" sprint review --json >"$VERIFY_TMP/review.json"
R="$VERIFY_TMP/review.json"
api "projects/$T/issues?milestone=Sprint%2042&state=all&scope=all&per_page=100" >"$VERIFY_TMP/s42.json"
# Expected, computed from the API alone: weight = largest weight::N label, done = closed or labelled workflow::done.
EXP='[.[] | {done: (.state == "closed" or (.labels | index("workflow::done") != null)),
             w: ([.labels[] | select(startswith("weight::")) | ltrimstr("weight::") | tonumber] | max)}]'
exp() { jq -r "$EXP | $1" "$VERIFY_TMP/s42.json"; }
exp_total=$(exp 'length')
exp_done=$(exp '[.[] | select(.done)] | length')
exp_done_w=$(exp '[.[] | select(.done and .w != null) | .w] | add // 0')
exp_carried=$(exp '[.[] | select(.done | not)] | length')
exp_carried_w=$(exp '[.[] | select((.done | not) and .w != null) | .w] | add // 0')
exp_unweighted=$(exp '[.[] | select(.w == null)] | length')
echo "  expected from the API: $exp_total issues, done $exp_done ($exp_done_w w), carried $exp_carried ($exp_carried_w w), $exp_unweighted unweighted"
expect_json "sprint is Sprint 42 and measured by weight" "$R" '.sprint.title == "Sprint 42" and .basis == "weight"'
expect_json "committed + added = every issue of the sprint ($exp_total)" "$R" "(.committed.count + .added.count) == $exp_total"
expect_json "done: $exp_done items, ${exp_done_w}w" "$R" ".done.count == $exp_done and .done.weight == $exp_done_w"
expect_json "carried: $exp_carried items, ${exp_carried_w}w" "$R" ".carried.count == $exp_carried and .carried.weight == $exp_carried_w"
expect_json "done + carried = committed + added, in count and weight" "$R" \
  '(.done.count + .carried.count) == (.committed.count + .added.count) and (.done.weight + .carried.weight) == (.committed.weight + .added.weight)'
expect_json "tracker#10 (closed, labelled workflow::done) counts as done" "$R" \
  '[.items[] | select(.ref == "ade-demo-env/tracker#10")] | .[0].done == true'
step "sprint review: unweighted work is never a zero"
expect_json "tracker#6 is unweighted (null), counted apart ($exp_unweighted unweighted in total)" "$R" \
  "([.items[] | select(.ref == \"ade-demo-env/tracker#6\")] | .[0].weight == null) and ((.done.unweighted + .carried.unweighted) == $exp_unweighted)"
expect_json "weight totals only add weighted items" "$R" \
  '(.done.weighted + .done.unweighted) == .done.count and (.carried.weighted + .carried.unweighted) == .carried.count'

step "velocity over closed sprints"
"$ADE" sprint velocity --json >"$VERIFY_TMP/velocity.json"
V="$VERIFY_TMP/velocity.json"
expect_json "no sprint reports a weight velocity without weighted work" "$V" \
  '[.sprints[] | select(.velocity != null and .scope.weighted == 0)] | length == 0'
expect_json "a sprint with only unweighted work has velocity null, not 0" "$V" \
  '[.sprints[] | select(.scope.weighted == 0) | .velocity] | all(. == null)'
# Expected average from the API alone: the last 3 dated milestones that ended before Sprint 42,
# weight done over the ones with weighted work (items done over all of them when none is weighted).
api "groups/ade-demo-env/milestones?per_page=100" >"$VERIFY_TMP/ms.json"
jq -r '[.[] | select(.start_date != null and .due_date != null and .due_date < "2026-11-02")] | sort_by(.start_date) | .[-3:] | .[].title' \
  "$VERIFY_TMP/ms.json" >"$VERIFY_TMP/past.txt"
echo "  past sprints per the API: $(paste -sd, "$VERIFY_TMP/past.txt")"
: >"$VERIFY_TMP/past.jsonl"
while IFS= read -r title; do
  api "projects/$T/issues?milestone=$(jq -rn --arg t "$title" '$t | @uri')&state=all&scope=all&per_page=100" |
    jq -c "$EXP | {weighted: ([.[] | select(.w != null)] | length), done_w: ([.[] | select(.done and .w != null) | .w] | add // 0), done_n: ([.[] | select(.done)] | length)}" \
      >>"$VERIFY_TMP/past.jsonl"
done <"$VERIFY_TMP/past.txt"
exp_avg=$(jq -rs 'if length == 0 then "null"
  elif any(.[]; .weighted > 0) then ([.[] | select(.weighted > 0) | .done_w] | add / length)
  else ([.[] | .done_n] | add / length) end' "$VERIFY_TMP/past.jsonl")
exp_basis=$(jq -rs 'if any(.[]; .weighted > 0) then "weight" else "count" end' "$VERIFY_TMP/past.jsonl")
echo "  expected velocity: $exp_avg per sprint by $exp_basis"
expect_json "velocity basis and average match the forge ($exp_avg by $exp_basis)" "$V" \
  ".basis == \"$exp_basis\" and ((.average - ($exp_avg)) | fabs) < 0.001"
check "velocity prints" "$ADE" sprint velocity
"$ADE" sprint velocity --md >"$VERIFY_TMP/velocity.md"
grep -q '^# Velocity' "$VERIFY_TMP/velocity.md" && pass "velocity --md is Markdown" || fail "velocity --md"

step "exports"
"$ADE" sprint review --md >"$VERIFY_TMP/review.md"
grep -q '^# Sprint review · Sprint 42' "$VERIFY_TMP/review.md" && pass "review --md heading" || fail "review --md heading"
grep -q -- "- Done: ${exp_done_w}w · ${exp_done} item" "$VERIFY_TMP/review.md" && pass "review --md carries the done total" || fail "review --md lacks the done total"
if command -v script >/dev/null 2>&1; then
  if script -qec "$ADE standup --date 2026-11-09 --copy" /dev/null | grep -q $'\033]52;c;'; then
    pass "--copy sends an OSC 52 clipboard request to the terminal"
  else
    fail "--copy sent no OSC 52 sequence"
  fi
else
  echo "  SKIP  --copy (no script(1) to provide a pty)"
fi

step "AI narrative (omp on claude-haiku-4-5; at most \$0.20 in total)"
SANDBOX_CONFIG=$ADE_CONFIG
ai_config() { # FILE MODE
  cp "$SANDBOX_CONFIG" "$1"
  cat >>"$1" <<EOF2
[ai]
mode = "$2"
profile = "sub"
[ai.profiles.sub]
driver = "omp"
models = { explain = "anthropic/claude-haiku-4-5", draft = "anthropic/claude-haiku-4-5", review = "anthropic/claude-haiku-4-5" }
billing = { source = "omp", weekly_soft_usd = 10 }
[budget]
call_usd = 0.1
daily_usd = 0.2
EOF2
}
ai_config "$VERIFY_TMP/ai-off.toml" off
ADE_CONFIG=$VERIFY_TMP/ai-off.toml "$ADE" standup --date 2026-11-09 --ai --yes --json >"$VERIFY_TMP/off.json" 2>"$VERIFY_TMP/off.err" \
  && pass "standup --ai with ai.mode = off still succeeds" || fail "standup --ai failed with ai.mode = off"
expect_json "...and stays deterministic (no narrative, a warning says why)" "$VERIFY_TMP/off.json" \
  '(.narrative // "") == "" and ([.warnings[]? | select(contains("AI is off"))] | length == 1)'
grep -q 'AI narrative skipped' "$VERIFY_TMP/off.err" && pass "the skip is announced on stderr" || fail "no skip warning on stderr"

if ! command -v omp >/dev/null; then
  echo "  SKIP  omp is not on PATH: no live narrative"
else
  ai_config "$VERIFY_TMP/ai.toml" auto
  export ADE_CONFIG=$VERIFY_TMP/ai.toml
  "$ADE" standup --date 2026-11-12 --ai --yes --json >"$VERIFY_TMP/n1.json" || fail "standup --ai"
  expect_json "standup narrative is written" "$VERIFY_TMP/n1.json" '(.narrative | length) > 40'
  "$ADE" standup --date 2026-11-12 --ai --yes --json >"$VERIFY_TMP/n2.json" || fail "standup --ai (again)"
  [[ $(jq -r .narrative "$VERIFY_TMP/n1.json") == "$(jq -r .narrative "$VERIFY_TMP/n2.json")" ]] && pass "second standup narrative is the cached text" || fail "narrative changed between calls"
  "$ADE" sprint review --ai --yes --json >"$VERIFY_TMP/r1.json" || fail "sprint review --ai"
  expect_json "sprint review narrative is written" "$VERIFY_TMP/r1.json" '(.narrative | length) > 40'
  "$ADE" sprint review --ai --yes --json >"$VERIFY_TMP/r2.json" || fail "sprint review --ai (again)"
  "$ADE" audit --action 'ai.standup' --json >"$VERIFY_TMP/a1.json"
  "$ADE" audit --action 'ai.sprint-review' --json >"$VERIFY_TMP/a2.json"
  expect_json "standup: one paid call then one \$0 cache hit, both audited" "$VERIFY_TMP/a1.json" \
    'length == 2 and .[0].ai.cache_hit == false and .[0].ai.cost_usd > 0 and .[1].ai.cache_hit == true and (.[1].ai.cost_usd // 0) == 0 and (.[0].ai.artifact | contains("items/_standup/"))'
  expect_json "sprint review: one paid call then one \$0 cache hit, both audited" "$VERIFY_TMP/a2.json" \
    'length == 2 and .[0].ai.cache_hit == false and .[0].ai.cost_usd > 0 and .[1].ai.cache_hit == true and (.[1].ai.cost_usd // 0) == 0 and (.[0].ai.artifact | contains("items/_sprint/"))'
  SPEND=$(jq -rs '[.[][] | .ai.cost_usd // 0] | add' "$VERIFY_TMP/a1.json" "$VERIFY_TMP/a2.json")
  printf '  omp spend this step: $%s of $0.20 allowed\n' "$SPEND"
  [[ $(jq -n "$SPEND <= 0.20") == true ]] && pass "narrative spend within the \$0.20 cap" || fail "narrative spend $SPEND over the cap"
fi

printf '\nPhase 5: all checks passed\n'
