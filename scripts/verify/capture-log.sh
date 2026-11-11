#!/usr/bin/env bash
# Capture the Log page (the audit trail) for visual review with VHS.
# Output: .impeccable/review/log-*.png
#
#   scripts/verify/capture-log.sh        # desktop, narrow, no-colour sets
#
# States: populated (newest first, detail pane), the action groups (forge.*,
# ai.*, budget.*, errors only), the free-text filter, a wider window, empty
# (no audit lines in the window) and a read error (an unreadable audit file).
# Data: synthetic audit entries (capture-ai-lib.sh); no model call, no network.
# The audit log is reseeded before every attempt, so each set shows the same
# counts: the seed plus the one entry the dash run itself writes.
. scripts/verify/capture-ai-lib.sh
ai_capture_init log
use_config log auto 2.00
seed_ai
BEFORE_CAPTURE=reseed_ai

# Each group is the cumulative number of `a` presses from "all": forge.* 1,
# ai.* 2, review.* 3, budget.* 4, git.* 5, errors only 6. `c` clears first, so
# a frame never depends on where the last one left the cycle.
group() { # STATE PRESSES
  printf 'Type "c"\nSleep 150ms\n'
  presses a "$2"
  shot "group-$1" 600ms
}
populated() {
  printf 'Type "L"\n'
  shot populated 1500ms
  presses j 3
  printf 'Tab\n'
  shot detail 700ms
  printf 'Tab\n'
  group forge 1
  group ai 2
  printf 'Tab\n'
  shot group-ai-detail 700ms # an ai.* entry: its artifact path wraps at the slashes
  printf 'Tab\n'
  group budget 4
  group errors 6
  printf 'Type "c"\nType "/"\nType "api!2"\n'
  shot filter-typing 600ms
  printf 'Enter\n'
  shot filter-text 600ms
  printf 'Type "c"\nType "s"\n'
  shot window-week 700ms
}
sets populated

# Empty: a data dir with no audit lines yet.
BEFORE_CAPTURE=
export XDG_DATA_HOME="$VERIFY_TMP/data-empty" XDG_CACHE_HOME="$VERIFY_TMP/cache-empty"
use_config logempty auto 2.00
empty() {
  printf 'Type "L"\n'
  shot empty 1500ms
}
sets empty

# Error: the month file exists but cannot be read.
# (a clean synthetic path: the error message names the file)
DEMO=/tmp/ade-demo
rm -rf "$DEMO"
export XDG_DATA_HOME="$DEMO/data" XDG_CACHE_HOME="$DEMO/cache"
mkdir -p "$XDG_DATA_HOME/ade/audit"
f="$XDG_DATA_HOME/ade/audit/$(date -u +%Y-%m).jsonl"
: >"$f"
chmod 000 "$f"
use_config logerr auto 2.00
failed() {
  printf 'Type "L"\n'
  shot error 1500ms
}
sets failed
chmod 600 "$f"
rm -rf "$DEMO"
verify_shots
ls "$OUT"/log-*.png
