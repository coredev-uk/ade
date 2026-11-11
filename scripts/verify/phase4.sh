#!/usr/bin/env bash
# Phase 4 acceptance (docs/PLAN.md §8): AI explain + budget + agent review.
# Run from the repo root inside `nix develop .#gateway` (it adds litellm and
# postgres). Needs .env with GITLAB_PAT and a logged-in `omp`.
#
# Real AI spend is omp only (claude-haiku-4-5, a few thousandths of a dollar
# per call, hard-capped at $0.50 per call by budget.call_usd) and the script
# asserts its own total stays under $0.25. The gateway part spends nothing:
# a local LiteLLM proxy answers from `mock_response`.
#
# Live state left behind on the sandbox: a few resolved review threads on
# api!2 per run, as in phase 2.
. scripts/verify/lib.sh

MAX_TOTAL_USD=0.25

step "unit and fake-server tests"
check "drivers (omp, anthropic, openai-compat), budget-stop classification by body, result cache + reindex, caps, pre-flight, agent review drafts" \
  go test ./internal/ai/...
check "billing sources: litellm, omp, anthropic, openai, moonshot, openrouter (httptest fakes, never live)" \
  go test ./internal/billing/...
check "ledger over the audit log, periods, review rates; budget report; AI artifacts; config" \
  go test ./internal/ledger/... ./internal/budget/... ./internal/records/... ./internal/config/...
check "ade ai summarize/review/triage and budget caps against the fake GitLab" go test ./internal/cli/... -run 'TestAI|TestPick|TestClosed'
check "TUI Budget and Log pages, History tab" go test ./internal/tui/... -run 'Budget|Log|History'

step "tools"
for t in litellm initdb pg_ctl curl jq omp; do
  command -v "$t" >/dev/null && pass "$t on PATH" || fail "$t not found: run inside \`nix develop .#gateway\`"
done

build_ade
sandbox_gitlab
GRP=ade-demo-env
MR=$GRP/api!2 # limit/bucket.go -> limit/token_bucket.go: Allow() never consumes a token
GWDIR="$VERIFY_TMP/gw"
gateway_cleanup() { scripts/gateway/gateway.sh down "$GWDIR" >/dev/null 2>&1 || true; }
trap 'gateway_cleanup; rm -rf "$VERIFY_TMP"' EXIT

step "local LiteLLM gateway (no external spend)"
scripts/gateway/gateway.sh up "$GWDIR" 0.01 >/dev/null || fail "gateway did not start"
. "$GWDIR/env"
pass "proxy at $ADE_GATEWAY_ROOT with a virtual key of max_budget 0.01"

SANDBOX_CONFIG=$ADE_CONFIG
write_config() { # FILE AI_MODE DAILY_USD
  cp "$SANDBOX_CONFIG" "$1"
  cat >>"$1" <<EOF
[ai]
mode = "$2"
profile = "sub"
[ai.profiles.sub]
driver = "omp"
models = { explain = "anthropic/claude-haiku-4-5", draft = "anthropic/claude-haiku-4-5", review = "anthropic/claude-haiku-4-5" }
billing = { source = "omp", weekly_soft_usd = 10 }
[ai.profiles.gw]
driver = "openai-compat"
base_url = "$ADE_GATEWAY_URL"
key_env = "ADE_GATEWAY_KEY"
models = { explain = "$ADE_GATEWAY_MODEL", draft = "$ADE_GATEWAY_MODEL", review = "$ADE_GATEWAY_MODEL" }
billing = { source = "litellm" }
[budget]
call_usd = 0.5
daily_usd = $3
weekly_usd = 1.5
EOF
}
write_config "$VERIFY_TMP/ai.toml" auto 1.0
export ADE_CONFIG=$VERIFY_TMP/ai.toml
check "config validates (profiles, billing sources, caps)" "$ADE" config check

ade_json() { # FILE ARGS...: run ade with --json into FILE; show stderr on failure
  local out=$1
  shift
  "$ADE" "$@" --json >"$out" 2>"$VERIFY_TMP/err" || {
    cat "$VERIFY_TMP/err" "$out" >&2
    fail "ade $*"
  }
}
audit_count() { "$ADE" audit --action "$1" --json | jq 'length'; }
cost_of() { jq -r '.cost_usd' "$1"; }

check "ade sync exits 0 (the sandbox is reachable)" "$ADE" sync

step "summary: pre-flight, one paid call, then free cache hits (also after losing cache.db)"
ade_json "$VERIFY_TMP/dry.json" ai summarize "$MR" --dry-run
expect_json "dry run shows an estimate and sends nothing" "$VERIFY_TMP/dry.json" \
  '.dry_run == true and (.cached // false) == false and .preflight.priced == true and .preflight.estimate_usd > 0 and .preflight.estimate_usd < 0.5'
[[ $(audit_count 'ai.*') == 0 ]] && pass "no ai audit entry for the dry run" || fail "dry run was audited as a call"

ade_json "$VERIFY_TMP/s1.json" ai summarize "$MR"
expect_json "first summary is a paid call under the per-call cap, reported by omp" "$VERIFY_TMP/s1.json" \
  '.cache_hit == false and .cost_usd > 0 and .cost_usd < 0.5 and (.text | length) > 20 and (.artifact | contains("/mr-2/ai/"))'
ade_json "$VERIFY_TMP/s2.json" ai summarize "$MR"
expect_json "second summary of the unchanged MR is a cache hit at \$0" "$VERIFY_TMP/s2.json" \
  '.cache_hit == true and .cost_usd == 0 and .saved_usd > 0'
[[ $(jq -r .text "$VERIFY_TMP/s1.json") == "$(jq -r .text "$VERIFY_TMP/s2.json")" ]] && pass "the cached text is the paid text" || fail "cached text differs"

rm -f "$XDG_CACHE_HOME"/ade/cache.db*
check "ade reindex rebuilds the AI result index and the ledger from the records and audit log" "$ADE" reindex
ade_json "$VERIFY_TMP/s3.json" ai summarize "$MR"
expect_json "after deleting cache.db and reindexing, the summary is still a \$0 cache hit" "$VERIFY_TMP/s3.json" \
  '.cache_hit == true and .cost_usd == 0'
ade_json "$VERIFY_TMP/aud.json" audit --action 'ai.summarize'
expect_json "audit: one paid call, then two \$0 hits" "$VERIFY_TMP/aud.json" \
  'length == 3 and (.[0].ai.cache_hit // false) == false and .[0].ai.cost_source == "reported" and .[1].ai.cache_hit and .[2].ai.cache_hit and .[1].ai.cost_usd == 0 and (.[0].ai.artifact | length) > 0 and .[0].ai.dest_host != null'
ART=$(jq -r '.[0].ai.artifact' "$VERIFY_TMP/aud.json")
head -12 "$XDG_DATA_HOME/ade/$ART" >"$VERIFY_TMP/artifact.txt"
for key in 'feature: summarize' 'profile: sub' 'prompt_version: summarize-v1' 'input_hash: ' 'cost_usd: ' "audit_id: $(jq -r '.[0].id' "$VERIFY_TMP/aud.json")"; do
  grep -q "$key" "$VERIFY_TMP/artifact.txt" && pass "artifact frontmatter has '$key'" || fail "artifact lacks '$key'"
done

step "agent review: structured drafts, triage, one batch; denied are never posted"
ade_json "$VERIFY_TMP/threads0.json" mr show "$MR"
for pass in 1 2 3 4; do
  ade_json "$VERIFY_TMP/review.json" ai review "$MR" --refresh
  P=$("$ADE" ai triage "$MR" --json | jq '[.[] | select(.source == "agent" and .state == "pending")] | length')
  [[ $P -ge 3 ]] && break
done
[[ $P -ge 3 ]] && pass "review pass $pass left $P pending agent drafts" || fail "only $P agent drafts after 4 passes"
ade_json "$VERIFY_TMP/drafts.json" ai triage "$MR"
D="$VERIFY_TMP/drafts.json"
expect_json "every draft is structured (path, line, side, severity, body), pending, source agent" "$D" \
  'all(.[]; .source == "agent" and .state == "pending" and (.path | length) > 0 and .line > 0 and (.side == "new" or .side == "old") and (.severity | IN("blocking","suggestion","question","nit")) and (.body | length) > 0)'
expect_json "every draft sits on a line of the MR's diff (validated before it was saved)" "$D" \
  'all(.[]; .path == "limit/token_bucket.go" and .line >= 1 and .line <= 11)'
expect_json "the review artifact is cached and no draft is sent yet" "$VERIFY_TMP/review.json" '.drafts | length >= 1'
ade_json "$VERIFY_TMP/threads1.json" mr show "$MR"
expect_json "nothing reached the forge from the review pass" "$VERIFY_TMP/threads1.json" \
  "(.threads | length) == $(jq '.threads | length' "$VERIFY_TMP/threads0.json")"
check "pending agent drafts are not submittable" bash -c "! $ADE review submit '$MR' 2>&1 | grep -q published"

ACCEPT_ID=$(jq -r '[.[] | select(.state == "pending")][0].id' "$D")
DENY_ID=$(jq -r '[.[] | select(.state == "pending")][1].id' "$D")
EDIT_ID=$(jq -r '[.[] | select(.state == "pending")][2].id' "$D")
ACCEPT_BODY=$(jq -r --arg id "$ACCEPT_ID" '.[] | select(.id == $id) | .body' "$D")
DENY_BODY=$(jq -r --arg id "$DENY_ID" '.[] | select(.id == $id) | .body' "$D")
EDIT_BODY=$(jq -r --arg id "$EDIT_ID" '.[] | select(.id == $id) | .body' "$D")
TAG="p4-$$-$(date +%s)"
EDITED="[$TAG] Edited by the reviewer: please consume a token under a lock."
check "accept one draft" "$ADE" ai triage accept "$MR" "$ACCEPT_ID"
check "deny another, with a reason" "$ADE" ai triage deny "$MR" "$DENY_ID" --reason noisy
check "edit a third (the edit is what gets posted)" "$ADE" ai triage edit "$MR" "$EDIT_ID" --body "$EDITED"
ade_json "$VERIFY_TMP/triaged.json" ai triage "$MR"
expect_json "triage states: accepted, denied, accepted (edited), rest still pending" "$VERIFY_TMP/triaged.json" \
  "([.[] | select(.state == \"accepted\")] | length) == 2 and ([.[] | select(.state == \"denied\")] | length) == 1 and ([.[] | select(.state == \"pending\")] | length) == $((P - 3))"
PUB0=$(audit_count 'review.submit')
ade_json "$VERIFY_TMP/submit.json" review submit "$MR"
expect_json "the two accepted drafts went out together" "$VERIFY_TMP/submit.json" '.uploaded == 2 and .published == 2'
ade_json "$VERIFY_TMP/threads2.json" mr show "$MR"
T="$VERIFY_TMP/threads2.json"
expect_json "exactly two new discussions on the forge" "$T" \
  "(.threads | length) == $(jq '.threads | length' "$VERIFY_TMP/threads0.json") + 2"
ALL=$(jq -r '[.threads[].notes[].body] | join("\n---\n")' "$T")
[[ $ALL == *"$EDITED"* ]] && pass "the edited text was posted, as edited" || fail "edited draft not posted"
[[ $ALL == *"$ACCEPT_BODY"* ]] && pass "the accepted draft was posted" || fail "accepted draft not posted"
[[ $ALL != *"$DENY_BODY"* ]] && pass "the denied draft was never posted" || fail "a denied draft reached the forge"
[[ $ALL != *"$EDIT_BODY"* || $EDIT_BODY == "$EDITED" ]] && pass "the pre-edit text was never posted" || fail "pre-edit text posted"
[[ $(audit_count 'review.submit') == $((PUB0 + 1)) ]] && pass "one submit, one batch" || fail "submit count"
for a in review.comment.draft review.comment.accept review.comment.deny review.comment.edit ai.review; do
  [[ $(audit_count "$a") -ge 1 ]] && pass "audit has $a" || fail "audit lacks $a"
done
# Leave this run's threads resolved: the new ones are those not in threads0.
for id in $(jq -r --slurpfile old "$VERIFY_TMP/threads0.json" '[.threads[].id] - [$old[0].threads[].id] | .[]' "$T"); do
  "$ADE" mr resolve "$MR" "$id" >/dev/null
done
ade_json "$VERIFY_TMP/budget1.json" budget --offline
expect_json "triage rates for the profile are on the budget report (1 accepted, 1 edited, 1 denied)" "$VERIFY_TMP/budget1.json" \
  '[.review_rates[] | select(.profile == "sub")][0] | .accepted >= 1 and .edited >= 1 and .denied >= 1'

step "classify: suggestions only until confirmed; applied labels reverted"
TRACKER11=$GRP/tracker#11
# gitlab.com rate-limits the shared sandbox token now and then: retry on 429.
gl() { # curl args...
  local i
  for i in 1 2 3 4 5 6; do
    curl -fsS -H "PRIVATE-TOKEN: $GITLAB_PAT" "$@" 2>/dev/null && return 0
    sleep $((i * 5))
  done
  return 1
}
issue_labels() { gl "https://gitlab.com/api/v4/projects/$GRP%2Ftracker/issues/11" | jq -c '.labels | sort'; }
revert_11() { # the sandbox keeps #11 as the untriaged item
  gl -X PUT "https://gitlab.com/api/v4/projects/$GRP%2Ftracker/issues/11" \
    --data-urlencode "remove_labels=type::story,type::bug,type::chore,app::api,app::web" >/dev/null
}
trap 'revert_11 || true; gateway_cleanup; rm -rf "$VERIFY_TMP"' EXIT
[[ $(issue_labels) == '[]' ]] && pass "tracker#11 starts without labels" || fail "tracker#11 already has labels: $(issue_labels)"
ade_json "$VERIFY_TMP/cl.json" ai classify "$TRACKER11"
expect_json "AI suggests a type and an app from the team's values, applies nothing" "$VERIFY_TMP/cl.json" \
  '.applied == false and .suggestion.source == "ai" and (.suggestion.labels | length) >= 1 and all(.suggestion.labels[]; test("^(type|app)::"))'
[[ $(issue_labels) == '[]' ]] && pass "suggesting changed no label" || fail "labels changed by a suggestion"
set +e
echo | "$ADE" ai classify "$TRACKER11" --apply >/dev/null 2>"$VERIFY_TMP/cl.err"
RC=$?
set -e
[[ $RC == 5 && $(issue_labels) == '[]' ]] && pass "--apply without a confirmation is refused (exit 5), nothing written" || fail "apply without --yes (rc $RC)"
"$ADE" ai classify "$TRACKER11" --apply --yes --json >"$VERIFY_TMP/cl2.json"
expect_json "--apply --yes adds exactly the suggested labels" "$VERIFY_TMP/cl2.json" '.applied == true'
[[ $(issue_labels) == "$(jq -c '.suggestion.labels | sort' "$VERIFY_TMP/cl2.json")" ]] && pass "the forge now carries the suggested labels" || fail "forge labels differ from the suggestion"
[[ $(audit_count 'forge.issue.labels') == 1 ]] && pass "the write is audited as forge.issue.labels" || fail "label write not audited"
revert_11
[[ $(issue_labels) == '[]' ]] && pass "tracker#11 reverted to untriaged" || fail "could not revert tracker#11"
ade_json "$VERIFY_TMP/cl3.json" ai classify "$TRACKER11" --no-ai
expect_json "with --no-ai (or ai.mode off) the suggestion is deterministic keywords, no cost" "$VERIFY_TMP/cl3.json" \
  '.suggestion.source == "keywords" and .cost_usd == 0'

step "caps and the kill switch"
write_config "$VERIFY_TMP/capped.toml" auto 0.0001
set +e
ADE_CONFIG=$VERIFY_TMP/capped.toml "$ADE" ai summarize "$MR" --refresh >"$VERIFY_TMP/capped.out" 2>"$VERIFY_TMP/capped.err"
RC=$?
set -e
[[ $RC == 4 ]] && pass "a cap breach exits 4" || fail "cap breach exit code $RC"
grep -q 'daily cap reached' "$VERIFY_TMP/capped.err" && grep -q 'budget.daily_usd' "$VERIFY_TMP/capped.err" && pass "the message names the cap and the config key" || {
  cat "$VERIFY_TMP/capped.err" >&2
  fail "unclear cap message"
}
[[ $(audit_count 'budget.stop') == 1 ]] && pass "the stop is audited as budget.stop" || fail "no budget.stop for the local cap"
ADE_CONFIG=$VERIFY_TMP/capped.toml "$ADE" ai summarize "$MR" --refresh --override-budget --dry-run >/dev/null && pass "--override-budget is accepted (audited when a call goes through)" || fail "override flag"

write_config "$VERIFY_TMP/off.toml" off 1.0
set +e
ADE_CONFIG=$VERIFY_TMP/off.toml "$ADE" ai summarize "$MR" >/dev/null 2>"$VERIFY_TMP/off.err"
RC=$?
set -e
[[ $RC != 0 ]] && grep -q 'ai.mode' "$VERIFY_TMP/off.err" && pass "ai.mode = off refuses AI commands" || fail "ai.mode off (rc $RC)"
ADE_CONFIG=$VERIFY_TMP/off.toml "$ADE" mr show "$MR" >/dev/null && pass "ai.mode = off leaves reading and reviewing alone" || fail "mr show broke with ai.mode off"
ADE_CONFIG=$VERIFY_TMP/off.toml "$ADE" budget --offline >/dev/null && pass "the budget report still works with ai.mode = off" || fail "budget with ai.mode off"

step "gateway: budgeted virtual key, /key/info matches the budget page, over-budget is budget.stop"
GW=(ai summarize "$MR" --profile gw --refresh --yes)
ade_json "$VERIFY_TMP/gdry.json" ai summarize "$MR" --profile gw --refresh --dry-run
expect_json "pre-flight prices come from the gateway's /model_group/info" "$VERIFY_TMP/gdry.json" \
  '.preflight.priced == true and .preflight.estimate_usd > 0 and .preflight.driver == "openai-compat"'
ade_json "$VERIFY_TMP/g1.json" "${GW[@]}"
expect_json "first gateway call is paid from the gateway's cost header" "$VERIFY_TMP/g1.json" '.cost_usd > 0 and .profile == "gw" and .cache_hit == false'
GE=$("$ADE" audit --action 'ai.summarize' --json | jq '[.[] | select(.ai.profile == "gw")][0]')
[[ $(jq -r .ai.cost_source <<<"$GE") == gateway && $(jq -r '.params.key_max_budget' <<<"$GE") == 0.01 ]] && pass "cost source is gateway and the key's max_budget came back in the headers" || fail "gateway audit entry: $GE"

key_info() { curl -fsS "$ADE_GATEWAY_ROOT/key/info" -H "Authorization: Bearer $ADE_GATEWAY_KEY"; }
# The proxy writes spend in batches (proxy_batch_write_at: 1s): wait for it.
settle() { # EXPECTED_SPEND
  local i s
  for i in $(seq 1 40); do
    s=$(key_info | jq -r '.info.spend')
    awk -v a="$s" -v b="$1" 'BEGIN { exit !((a - b) < 1e-7 && (b - a) < 1e-7) }' && return 0
    sleep 0.5
  done
  return 1
}
LEDGER_GW() { "$ADE" budget --offline --json | jq '[.profiles[] | select(.name == "gw")][0].week_usd'; }
settle "$(LEDGER_GW)" && pass "the proxy's recorded spend settles to the ledger's ($(LEDGER_GW))" || fail "gateway spend never matched the ledger: $(key_info | jq -c .info.spend) vs $(LEDGER_GW)"

# Call until the proxy itself refuses. No `ade budget` ran, so no cached
# snapshot lets ADE's own pre-flight get there first: this is the provider's
# error, classified by its body.
STOPPED=0
for i in $(seq 1 15); do
  set +e
  "$ADE" "${GW[@]}" >"$VERIFY_TMP/g.out" 2>"$VERIFY_TMP/g.err"
  RC=$?
  set -e
  [[ $RC == 0 ]] && continue
  [[ $RC == 4 ]] && STOPPED=1 && break
  cat "$VERIFY_TMP/g.err" >&2
  fail "gateway call failed with exit $RC (not a budget stop)"
done
[[ $STOPPED == 1 ]] && pass "an over-budget call stopped with exit 4 after $i call(s)" || fail "the gateway never refused"
grep -qi 'budget' "$VERIFY_TMP/g.err" && pass "message: $(head -c 160 "$VERIFY_TMP/g.err")" || fail "no budget message"
ade_json "$VERIFY_TMP/stops.json" audit --action 'budget.stop'
expect_json "the provider refusal is classified as budget.stop (source gateway, by body)" "$VERIFY_TMP/stops.json" \
  '[.[] | select(.params.source == "gateway")] | length >= 1 and (.[0].error | test("Budget has been exceeded"; "i")) and (.[0].params.profile == "gw")'
# What the proxy returned on the wire, status aside:
HTTP=$(curl -s -o "$VERIFY_TMP/wire.json" -w '%{http_code}' "$ADE_GATEWAY_URL/chat/completions" -H "Authorization: Bearer $ADE_GATEWAY_KEY" -H 'Content-Type: application/json' -d "{\"model\":\"$ADE_GATEWAY_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")
pass "for reference, this LiteLLM answers an over-budget key with HTTP $HTTP: $(jq -c '.error | {type, code}' "$VERIFY_TMP/wire.json")"

settle "$(LEDGER_GW)" && pass "after the stop, the proxy's spend still equals the ledger's" || fail "spend drifted"
ade_json "$VERIFY_TMP/budget2.json" budget --refresh
INFO=$(key_info)
B="$VERIFY_TMP/budget2.json"
SPEND=$(jq -r '.info.spend' <<<"$INFO")
MAXB=$(jq -r '.info.max_budget' <<<"$INFO")
expect_json "budget page provider spend equals /key/info spend ($SPEND)" "$B" \
  "[.profiles[] | select(.name == \"gw\")][0].provider.snapshot.spend_usd - $SPEND | (. < 1e-9 and . > -1e-9)"
expect_json "budget page cap equals /key/info max_budget ($MAXB), scope key, source litellm" "$B" \
  "[.profiles[] | select(.name == \"gw\")][0].provider | .snapshot.limit_usd == $MAXB and .snapshot.scope == \"key\" and .snapshot.source == \"litellm\""
expect_json "the merged figure adds nothing local after a fresh snapshot, and remaining is max_budget - spend" "$B" \
  "[.profiles[] | select(.name == \"gw\")][0].provider | .local_delta_usd == 0 and ((.remaining_usd - ($MAXB - $SPEND)) | (. < 1e-9 and . > -1e-9))"
expect_json "the stop is listed on the budget page" "$B" '[.stops[] | select(.profile == "gw")] | length >= 1'

# With the snapshot now cached, ADE's own pre-flight refuses before sending.
SPEND_BEFORE=$SPEND
set +e
"$ADE" "${GW[@]}" >/dev/null 2>"$VERIFY_TMP/g2.err"
RC=$?
set -e
[[ $RC == 4 ]] && pass "with the cached snapshot, ADE refuses locally too (exit 4)" || fail "local pre-flight exit $RC"
grep -q "provider's own budget" "$VERIFY_TMP/g2.err" && pass "…saying the gateway's budget is used up" || {
  cat "$VERIFY_TMP/g2.err" >&2
  fail "no provider-budget message"
}
[[ $(key_info | jq -r '.info.spend') == "$SPEND_BEFORE" ]] && pass "…and no request reached the proxy" || fail "spend moved"

step "AI spend this run"
ade_json "$VERIFY_TMP/budget3.json" budget --offline
OMP_USD=$(jq '[.profiles[] | select(.name == "sub")][0].week_usd' "$VERIFY_TMP/budget3.json")
printf '  omp (real, list-price) spend this run: $%s of $%s allowed\n' "$OMP_USD" "$MAX_TOTAL_USD"
awk -v a="$OMP_USD" -v m="$MAX_TOTAL_USD" 'BEGIN { exit !(a < m) }' && pass "under the $MAX_TOTAL_USD budget for this script" || fail "AI spend $OMP_USD over $MAX_TOTAL_USD"
printf '  gateway (mock model, no real spend): $%s on the key, max_budget %s\n' "$SPEND" "$MAXB"

printf '\nPhase 4: all checks passed\n'
