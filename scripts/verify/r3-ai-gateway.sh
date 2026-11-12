#!/usr/bin/env bash
# Round 3 acceptance: LiteLLM-gateway-first AI config with a dedicated
# classify model. Run from the repo root inside `nix develop .#gateway`
# (litellm + postgres). Needs .env with GITLAB_PAT (read-only calls only).
#
# No external spend: the local proxy hosts two mock models (`echo`, which
# answers prose, and `echo-classifier`, which answers a JSON label
# suggestion), so nothing leaves the machine except GitLab sandbox reads.
. scripts/verify/lib.sh

step "unit and httptest-gateway tests"
check "config: classify role, fallback chain, gateway profile validation" go test ./internal/config/...
check "ai: classify hits the classify model, summarize the explain model, structured-output fallback, headers, timeout" \
  go test ./internal/ai/... ./internal/billing/...
check "onboard: gateway profile rendering and URL normalisation" go test ./internal/onboard/... -run "Gateway"
check "cli: ade init --ai-gateway, ade ai models, classify through a gateway" \
  go test ./internal/cli/... -run 'Gateway|AIModels|AIClassifyThrough|InitWrites'

step "tools"
for t in litellm initdb pg_ctl curl jq; do
  command -v "$t" >/dev/null && pass "$t on PATH" || fail "$t not found: run inside \`nix develop .#gateway\`"
done

build_ade
sandbox_gitlab # isolated XDG dirs, a config with the sandbox project, GITLAB_PAT exported
SANDBOX_CONFIG=$ADE_CONFIG
GRP=ade-demo-env
ISSUE=$GRP/tracker#11 # needs triage: no type or app label
MR=$GRP/api!2
GWDIR="$VERIFY_TMP/gw"
trap 'scripts/gateway/gateway.sh down "$GWDIR" >/dev/null 2>&1 || true; rm -rf "$VERIFY_TMP"' EXIT

step "local LiteLLM gateway with two mock models"
scripts/gateway/gateway.sh up "$GWDIR" 5 >/dev/null || fail "gateway did not start"
. "$GWDIR/env"
[[ -n ${ADE_GATEWAY_CLASSIFY_MODEL:-} ]] || fail "gateway.sh did not define the classify mock model"
pass "proxy at $ADE_GATEWAY_ROOT: $ADE_GATEWAY_MODEL (explain) and $ADE_GATEWAY_CLASSIFY_MODEL (classify)"

step "ade init --ai-gateway in a fresh HOME"
FRESH="$VERIFY_TMP/home"
mkdir -p "$FRESH"
set +e
env -u XDG_CONFIG_HOME -u XDG_DATA_HOME -u XDG_CACHE_HOME -u ADE_CONFIG HOME="$FRESH" "$ADE" init --yes \
  --forge gitlab --host https://gitlab.com --token-env GITLAB_PAT --git-protocol https --root "$FRESH/ade" \
  --ai-gateway "${ADE_GATEWAY_URL%/v1}" --ai-key-env ADE_GATEWAY_KEY \
  --ai-model-explain "$ADE_GATEWAY_MODEL" --ai-model-classify "$ADE_GATEWAY_CLASSIFY_MODEL" \
  >"$VERIFY_TMP/init.out" 2>&1
RC=$?
set -e
# Exit 1 is `ade doctor` finding something on this machine, which says nothing about AI.
[[ $RC == 0 || $RC == 1 ]] || {
  cat "$VERIFY_TMP/init.out" >&2
  fail "ade init exited $RC"
}
CFG=$FRESH/.config/ade/config.toml
[[ -f $CFG ]] && pass "config written under the fresh HOME" || fail "no config at $CFG"
grep -q 'gateway ok:' "$VERIFY_TMP/init.out" && pass "init verified the gateway and key against /v1/models" || fail "init did not check the gateway: $(cat "$VERIFY_TMP/init.out")"
grep -q "base_url = \"$ADE_GATEWAY_URL\"" "$CFG" && grep -q 'driver = "openai-compat"' "$CFG" && grep -q 'source = "litellm"' "$CFG" &&
  grep -q "classify = \"$ADE_GATEWAY_CLASSIFY_MODEL\"" "$CFG" && grep -q 'key_env = "ADE_GATEWAY_KEY"' "$CFG" &&
  pass "gateway profile: openai-compat, base_url, key_env, billing litellm, classify and explain models" || {
  cat "$CFG" >&2
  fail "gateway profile is wrong"
}
grep -q "$ADE_GATEWAY_KEY" "$CFG" && fail "the gateway key was written to the config" || pass "the key itself is not in the config"
env -u XDG_CONFIG_HOME -u ADE_CONFIG HOME="$FRESH" "$ADE" config check >"$VERIFY_TMP/check.out" 2>&1 && pass "ade config check accepts the written config" || {
  cat "$VERIFY_TMP/check.out" >&2
  fail "ade config check rejected the written config"
}

step "ade ai models marks the role of each model"
# The sandbox project's config + the [ai] block init wrote.
{ cat "$SANDBOX_CONFIG"; sed -n '/^\[ai\]/,$p' "$CFG"; } >"$VERIFY_TMP/config.gw.toml"
export ADE_CONFIG=$VERIFY_TMP/config.gw.toml
"$ADE" ai models >"$VERIFY_TMP/models.out"
grep -Eq "^$ADE_GATEWAY_CLASSIFY_MODEL +classify$" "$VERIFY_TMP/models.out" && grep -Eq "^$ADE_GATEWAY_MODEL +explain, draft \(via explain\)" "$VERIFY_TMP/models.out" &&
  pass "classify -> $ADE_GATEWAY_CLASSIFY_MODEL, explain (and its fallbacks) -> $ADE_GATEWAY_MODEL" || {
  cat "$VERIFY_TMP/models.out" >&2
  fail "ade ai models output"
}
"$ADE" ai models --json >"$VERIFY_TMP/models.json"
expect_json "--json lists both models and every role's resolution" "$VERIFY_TMP/models.json" \
  '(.models | length) == 2 and ([.roles[] | select(.role == "classify")][0].model == "'"$ADE_GATEWAY_CLASSIFY_MODEL"'") and ([.roles[] | select(.role == "draft")][0].from == "explain")'

step "classify hits the classify model, summarize the explain model"
"$ADE" ai classify "$ISSUE" --refresh --yes --json >"$VERIFY_TMP/classify.json"
expect_json "classify answered by the classify model, parsed into a suggestion" "$VERIFY_TMP/classify.json" \
  '.model == "'"$ADE_GATEWAY_CLASSIFY_MODEL"'" and .suggestion.source == "ai" and .suggestion.type == "bug" and .cost_usd > 0'
"$ADE" ai summarize "$MR" --refresh --yes --json >"$VERIFY_TMP/summarize.json"
expect_json "summarize answered by the explain model" "$VERIFY_TMP/summarize.json" '.model == "'"$ADE_GATEWAY_MODEL"'" and .cost_usd > 0'

"$ADE" audit --action 'ai.*' --json >"$VERIFY_TMP/audit.json"
expect_json "audit: ai.classify -> classify model with structured=true, ai.summarize -> explain model, cost from the gateway" "$VERIFY_TMP/audit.json" \
  '([.[] | select(.action == "ai.classify")][0] | .ai.model == "'"$ADE_GATEWAY_CLASSIFY_MODEL"'" and .params.structured == true and .ai.cost_source == "gateway") and
   ([.[] | select(.action == "ai.summarize")][0] | .ai.model == "'"$ADE_GATEWAY_MODEL"'")'

# The proxy's own view: its spend logs name the model each request used. They
# are written in batches (proxy_batch_write_at: 1s), so wait for them.
spend_logs() { curl -fsS "$ADE_GATEWAY_ROOT/spend/logs" -H "Authorization: Bearer $ADE_GATEWAY_MASTER_KEY"; }
count_model() { jq --arg m "$1" '[.[] | select((.model_group // .model) == $m)] | length' <<<"$LOGS"; }
LOGS=[]
for _ in $(seq 1 40); do
  LOGS=$(spend_logs)
  [[ $(count_model "$ADE_GATEWAY_CLASSIFY_MODEL") -ge 1 && $(count_model "$ADE_GATEWAY_MODEL") -ge 1 ]] && break
  sleep 0.5
done
[[ $(count_model "$ADE_GATEWAY_CLASSIFY_MODEL") == 1 ]] && pass "LiteLLM spend logs: exactly 1 request on $ADE_GATEWAY_CLASSIFY_MODEL (the classify call)" ||
  fail "spend logs for the classify model: $(jq -c '[.[] | {model, model_group}]' <<<"$LOGS")"
[[ $(count_model "$ADE_GATEWAY_MODEL") == 1 ]] && pass "LiteLLM spend logs: exactly 1 request on $ADE_GATEWAY_MODEL (the summarize call)" ||
  fail "spend logs for the explain model: $(jq -c '[.[] | {model, model_group}]' <<<"$LOGS")"

step "classify falls back to explain when the profile sets no classify model"
grep -Ev '^[[:space:]]*classify = ' "$VERIFY_TMP/config.gw.toml" >"$VERIFY_TMP/config.noclassify.toml"
grep -q 'classify = ' "$VERIFY_TMP/config.noclassify.toml" && fail "could not strip the classify model from the profile"
# The explain mock answers prose, so ADE cannot parse a suggestion from it and
# exits non-zero: what matters is which model the call went to (the audit entry
# is written before the answer is parsed).
set +e
ADE_CONFIG=$VERIFY_TMP/config.noclassify.toml "$ADE" ai classify "$ISSUE" --refresh --yes >/dev/null 2>&1
set -e
"$ADE" audit --action 'ai.classify' --json >"$VERIFY_TMP/audit2.json"
expect_json "without models.classify the classify call goes to the explain model" "$VERIFY_TMP/audit2.json" \
  'length == 2 and (sort_by(.ts) | last | .ai.model == "'"$ADE_GATEWAY_MODEL"'")'
echo
echo "r3-ai-gateway: all checks passed"
