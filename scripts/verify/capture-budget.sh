#!/usr/bin/env bash
# Capture the Budget page and the header AI chip for visual review with VHS.
# Output: .impeccable/review/budget-*.png and aichip-*.png
#
#   scripts/verify/capture-budget.sh        # desktop, narrow, no-colour sets
#
# States: refreshing (the gateway answers after five seconds) and populated
# (settled: caps, two profiles with their provider figures, spend by
# day/feature/model, top items, stops, review triage rates); detail and
# detail-end (a profile's pane, then scrolled to its last line); gateway (the
# gateway profile selected); stale (the gateway read fails, so its three-hour
# old snapshot stays, marked on the row, in the pane and in the status line);
# over-cap (daily cap used up); AI off; empty (no AI calls yet, no gateway
# billing source) and provider-error (no snapshot and the read fails: the
# gateway row selected, its detail pane). The chip frames show the Home header
# at 85% of the day cap (amber), over it (red) and with AI off.
# Data: synthetic audit entries and gateway answers (capture-ai-lib.sh,
# scripts/verify/fakegw); no model call, no network.
. scripts/verify/capture-ai-lib.sh
ai_capture_init budget
rm -f "$OUT"/aichip-*.png
start_service GW_SLOW fakegw -slow 5s
start_service GW_REJECT fakegw -reject

seed_ai
# A gateway snapshot read three hours ago, so the page has a provider figure
# before the read it starts. Reset before every attempt: a settled read
# replaces it, and the next set must start from the same page.
"$ADE" budget --offline >/dev/null
reset_snapshot() {
  local ago reset
  ago=$(date -u -d '3 hours ago' +%Y-%m-%dT%H:%M:%SZ)
  reset=$(date -u -d 'next month' +%Y-%m-01T00:00:00Z)
  sqlite3 "$XDG_CACHE_HOME/ade/cache.db" "INSERT OR REPLACE INTO billing_snapshots (profile, snapshot, fetched_at) VALUES ('gateway', '$(jq -nc --arg a "$ago" --arg r "$reset" '{source:"litellm",as_of:$a,scope:"key",spend_usd:31.2,limit_usd:50,remaining_usd:18.8,window:"30d",reset_at:$r,note:"gateway spend"}')', '$ago')"
}
BEFORE_CAPTURE=reset_snapshot

chip() { # NAME: the Home header
  printf 'Sleep 1500ms\nScreenshot "%s/aichip-%s-%s.png"\n' "$OUT" "$SET_NAME" "$1"
}

# The profile rows follow the three caps: j j j is the first profile, a
# fourth j the gateway.
populated() {
  chip amber
  printf 'Type "b"\n'
  shot refreshing 1200ms
  printf 'Sleep 6s\n'
  shot populated 500ms
  presses j 3
  printf 'Tab\n'
  shot detail 500ms
  presses J 16 100ms
  shot detail-end 500ms
  printf 'Tab\nType "j"\nTab\n'
  shot gateway 500ms
  printf 'Tab\nType "G"\n'
  shot scrolled 500ms
}
GW_URL=$GW_SLOW use_config day auto 1.00 # AI auto, daily cap $1.00: the day's $0.85 is 85%
sets populated

stale() {
  printf 'Type "b"\n'
  shot stale 2500ms
  presses j 4
  shot stale-gateway 500ms
  printf 'Tab\n'
  shot stale-detail 500ms
  presses J 16 100ms
  shot stale-detail-end 500ms
}
GW_URL=$GW_REJECT use_config stale auto 1.00
sets stale

overcap() {
  chip cap
  printf 'Type "b"\n'
  shot over-cap 2500ms
}
use_config over auto 0.50 # $0.85 spent against $0.50: cap reached
sets overcap

modeoff() {
  chip off
  printf 'Type "b"\n'
  shot ai-off 2500ms
}
use_config off off 2.00
sets modeoff

# A fresh data dir: nothing spent, no snapshot ever read.
export XDG_DATA_HOME="$VERIFY_TMP/data-empty" XDG_CACHE_HOME="$VERIFY_TMP/cache-empty"
BEFORE_CAPTURE=
empty() {
  printf 'Type "b"\n'
  shot empty 2500ms
}
GW_BILLING=none use_config empty auto 2.00
sets empty

# The same, with a gateway that rejects its key: no figure, and why.
provider_error() {
  printf 'Type "b"\n'
  shot provider-error 2500ms
  presses j 4
  shot provider-error-gateway 500ms
  printf 'Tab\n'
  shot provider-error-detail 500ms
}
GW_URL=$GW_REJECT use_config gwerr auto 2.00
sets provider_error
verify_shots
ls "$OUT"/budget-*.png "$OUT"/aichip-*.png
