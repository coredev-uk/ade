#!/usr/bin/env bash
# Reads CHANGELOG.md for the release workflow (.github/workflows/release.yml).
#
#   changelog.sh version      the version of the newest section when it is a
#                             release heading (`## v1.2.3`), nothing while the
#                             newest section is still `## Unreleased`
#   changelog.sh notes <tag>  the body of the `## <tag>` section, for the
#                             GitHub release notes; exits 1 when there is none
set -euo pipefail

file=${CHANGELOG:-CHANGELOG.md}

case ${1:-} in
version)
  awk '/^## / { if ($2 ~ /^v[0-9]+\.[0-9]+\.[0-9]+$/ && NF == 2) print $2; exit }' "$file"
  ;;
notes)
  tag=${2:?usage: changelog.sh notes <tag>}
  awk -v tag="$tag" '
    /^## / { if (found) exit; if ($2 == tag && NF == 2) { found = 1; next } }
    found { body[++n] = $0 }
    END {
      if (!found) exit 1
      first = 1; while (first <= n && body[first] == "") first++
      last = n; while (last >= first && body[last] == "") last--
      for (i = first; i <= last; i++) print body[i]
    }
  ' "$file"
  ;;
*)
  echo "usage: changelog.sh version | notes <tag>" >&2
  exit 2
  ;;
esac
