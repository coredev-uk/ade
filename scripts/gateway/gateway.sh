#!/usr/bin/env bash
# A local LiteLLM gateway for exercising ADE's gateway profile, budgets and
# billing source (docs/PLAN.md §6.5, docs/billing/litellm.md) without any
# external spend: the model behind it is a LiteLLM `mock_response`, so no
# provider is ever called.
#
#   nix develop .#gateway -c scripts/gateway/gateway.sh up   [STATE_DIR] [MAX_BUDGET_USD]
#   nix develop .#gateway -c scripts/gateway/gateway.sh key  STATE_DIR MAX_BUDGET_USD   # another virtual key
#   nix develop .#gateway -c scripts/gateway/gateway.sh down STATE_DIR
#
# `up` starts Postgres (LiteLLM budgets need a DB; without one they fail
# open) and the proxy with a master key, then creates a virtual key with
# max_budget MAX_BUDGET_USD (default 0.01) that may use the model `echo`.
# It writes STATE_DIR/env, to be sourced:
#
#   ADE_GATEWAY_URL         OpenAI-style base URL (…/v1), the profile's base_url
#   ADE_GATEWAY_KEY         the budgeted virtual key (the profile's key)
#   ADE_GATEWAY_MASTER_KEY  the proxy's master key (setup and inspection only)
#
# Prisma (LiteLLM's DB layer) needs engine binaries that nixpkgs does not
# ship at the version the Python client pins, so they are fetched once from
# binaries.prisma.sh into $ADE_GATEWAY_CACHE (default ~/.cache/ade-gateway)
# and a writable copy of the generated client is kept there too. Fetching
# and running generic Linux binaries assumes network access once and, on
# NixOS, nix-ld.
set -euo pipefail

CACHE=${ADE_GATEWAY_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/ade-gateway}
MODEL=echo
# Per-token prices of the mock model, so each call costs a few thousandths of
# a dollar and a $0.01 key runs dry after a handful of calls.
IN_COST=0.000004
OUT_COST=0.00001

die() {
  echo "gateway: $*" >&2
  exit 1
}

need() { command -v "$1" >/dev/null || die "$1 not found: run inside \`nix develop .#gateway\`"; }

free_port() { python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'; }

# prisma_env: engine binaries + a generated client, exported for litellm.
prisma_env() {
  local commit plat dir lib schema pkg
  commit=$(python3 -c 'from prisma._config import config; print(config.expected_engine_version)')
  case "$(uname -m)" in
    x86_64) plat=debian-openssl-3.0.x ;;
    aarch64 | arm64) plat=linux-arm64-openssl-3.0.x ;;
    *) die "no Prisma engine target for $(uname -m)" ;;
  esac
  dir=$CACHE/prisma-$commit
  if [[ ! -x $dir/query-engine || ! -x $dir/schema-engine || ! -f $dir/libquery_engine.node ]]; then
    echo "gateway: fetching Prisma engines $commit ($plat) into $dir"
    mkdir -p "$dir"
    local f
    for f in schema-engine query-engine prisma-fmt; do
      curl -fsSL "https://binaries.prisma.sh/all_commits/$commit/$plat/$f.gz" | gunzip >"$dir/$f"
      chmod +x "$dir/$f"
    done
    curl -fsSL "https://binaries.prisma.sh/all_commits/$commit/$plat/libquery_engine.so.node.gz" | gunzip >"$dir/libquery_engine.node"
  fi
  export PRISMA_ENGINES_CHECKSUM_IGNORE_MISSING=1
  export PRISMA_SCHEMA_ENGINE_BINARY=$dir/schema-engine PRISMA_QUERY_ENGINE_BINARY=$dir/query-engine
  export PRISMA_QUERY_ENGINE_LIBRARY=$dir/libquery_engine.node PRISMA_FMT_BINARY=$dir/prisma-fmt
  # The client is generated into the prisma package, which lives in the
  # read-only Nix store: generate into a writable copy and put it first on
  # PYTHONPATH.
  lib=$CACHE/pylib-$commit
  if [[ ! -f $lib/prisma/client.py ]]; then
    echo "gateway: generating the Prisma client into $lib"
    rm -rf "$lib"
    mkdir -p "$lib"
    pkg=$(python3 -c 'import prisma, os; print(os.path.dirname(prisma.__file__))')
    cp -rL "$pkg" "$lib/prisma"
    chmod -R u+w "$lib"
    schema=$(python3 -c 'import litellm, os; print(os.path.join(os.path.dirname(litellm.__file__), "proxy", "schema.prisma"))')
    (cd "$lib" && PYTHONPATH=$lib:${PYTHONPATH:-} python3 -m prisma generate --schema="$schema" >"$lib/generate.log" 2>&1) ||
      die "prisma generate failed, see $lib/generate.log"
  fi
  export PYTHONPATH=$lib:${PYTHONPATH:-}
}

wait_http() { # URL SECONDS
  local i
  for ((i = 0; i < $2; i++)); do
    curl -fs "$1" >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

new_key() { # BASE MASTER MAX_BUDGET ALIAS
  curl -fsS -X POST "$1/key/generate" \
    -H "Authorization: Bearer $2" -H 'Content-Type: application/json' \
    -d "{\"key_alias\":\"$4\",\"max_budget\":$3,\"models\":[\"$MODEL\"],\"budget_duration\":\"30d\"}" | jq -r .key
}

cmd_up() {
  need litellm
  need initdb
  need pg_ctl
  need curl
  need jq
  local state=${1:-} budget=${2:-0.01}
  [[ -n $state ]] || state=$(mktemp -d)
  mkdir -p "$state"
  chmod 700 "$state"
  state=$(cd "$state" && pwd)
  [[ -e $state/env ]] && die "$state already holds a gateway: run \`down\` first"
  prisma_env

  local pgport port master
  pgport=$(free_port)
  port=$(free_port)
  master=sk-master-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')

  initdb -D "$state/pg" -U ade --auth=trust >/dev/null
  pg_ctl -D "$state/pg" -o "-p $pgport -k $state -c listen_addresses=127.0.0.1" -l "$state/pg.log" -w start >/dev/null
  createdb -h 127.0.0.1 -p "$pgport" -U ade litellm

  cat >"$state/config.yaml" <<YAML
model_list:
  - model_name: $MODEL
    litellm_params:
      model: openai/$MODEL
      api_key: not-a-real-key
      mock_response: "Mocked gateway reply for ADE tests."
      input_cost_per_token: $IN_COST
      output_cost_per_token: $OUT_COST
general_settings:
  master_key: $master
  proxy_batch_write_at: 1
YAML
  export DATABASE_URL=postgresql://ade@127.0.0.1:$pgport/litellm
  nohup litellm --config "$state/config.yaml" --host 127.0.0.1 --port "$port" >"$state/litellm.log" 2>&1 &
  echo $! >"$state/litellm.pid"
  if ! wait_http "http://127.0.0.1:$port/health/liveliness" 240; then
    tail -20 "$state/litellm.log" >&2
    cmd_down "$state" >/dev/null 2>&1 || true
    die "the proxy did not come up (log: $state/litellm.log)"
  fi

  local base=http://127.0.0.1:$port key
  key=$(new_key "$base" "$master" "$budget" "ade-test-$$")
  [[ -n $key && $key != null ]] || die "could not create the virtual key"
  {
    echo "export ADE_GATEWAY_URL=$base/v1"
    echo "export ADE_GATEWAY_ROOT=$base"
    echo "export ADE_GATEWAY_KEY=$key"
    echo "export ADE_GATEWAY_MASTER_KEY=$master"
    echo "export ADE_GATEWAY_MODEL=$MODEL"
  } >"$state/env"
  chmod 600 "$state/env"
  echo "$state"
}

cmd_key() {
  local state=${1:?usage: key STATE_DIR MAX_BUDGET_USD} budget=${2:?usage: key STATE_DIR MAX_BUDGET_USD}
  # shellcheck disable=SC1091
  . "$state/env"
  new_key "$ADE_GATEWAY_ROOT" "$ADE_GATEWAY_MASTER_KEY" "$budget" "ade-test-$$-$RANDOM"
}

cmd_down() {
  local state=${1:?usage: down STATE_DIR}
  if [[ -f $state/litellm.pid ]]; then
    kill "$(cat "$state/litellm.pid")" 2>/dev/null || true
  fi
  if [[ -d $state/pg ]]; then
    pg_ctl -D "$state/pg" -m immediate stop >/dev/null 2>&1 || true
  fi
  rm -rf "$state"
}

case ${1:-} in
  up) shift && cmd_up "$@" ;;
  key) shift && cmd_key "$@" ;;
  down) shift && cmd_down "$@" ;;
  *) die "usage: gateway.sh up [STATE_DIR] [MAX_BUDGET_USD] | key STATE_DIR MAX_BUDGET_USD | down STATE_DIR" ;;
esac
