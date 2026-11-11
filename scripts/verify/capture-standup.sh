#!/usr/bin/env bash
# Capture the Standup page (S) for visual review with VHS. Output:
# .impeccable/review/standup-{desktop,narrow,nocolor}-<state>.png, states:
# empty, loading, down (forge unreachable: cached items + warnings), ready (before the
# narrative), confirm (ai.mode = ask), off (ai.mode = off), populated, scrolled, narrative.
SURF=standup KEY=S
. scripts/verify/capture-agile-lib.sh
page_body() {
  snap 2500ms populated
  printf 'Type "G"\n'
  snap 500ms scrolled
  printf 'Type "g"\nType "n"\n'
  snap 3500ms narrative
}
capture_set desktop 14 ""
capture_set narrow 30 ""
capture_set nocolor 14 1
verify_shots
ls "$OUT"/standup-*.png
