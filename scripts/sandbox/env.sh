# Source from the repo root: `. scripts/sandbox/env.sh`
# Isolates ADE state under ./.sandbox so sandbox runs never touch real config or records.
_ade_repo=$(pwd)
export ADE_CONFIG="$_ade_repo/scripts/sandbox/config.toml"
export XDG_DATA_HOME="$_ade_repo/.sandbox/data"
export XDG_CACHE_HOME="$_ade_repo/.sandbox/cache"
if [ -f "$_ade_repo/.env" ]; then
  set -a
  . "$_ade_repo/.env"
  set +a
fi
unset _ade_repo
