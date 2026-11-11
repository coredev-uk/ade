#!/usr/bin/env bash
# Capture the Sprint review page (V) for visual review with VHS. Output:
# .impeccable/review/sprint-{desktop,narrow,nocolor}-<state>.png, states:
# empty, loading, down (error), ready, confirm, off, populated, scrolled, narrative,
# mine (your issues only), previous (Sprint 41), count (Sprint 40: nothing weighted).
SURF=sprint KEY=V
. scripts/verify/capture-agile-lib.sh
page_body() {
  snap 3000ms populated
  printf 'Type "G"\n'
  snap 500ms scrolled
  printf 'Type "g"\nType "n"\n'
  snap 3500ms narrative
  printf 'Type "m"\n'
  snap 2500ms mine
  printf 'Type "m"\nType "["\n'
  snap 2500ms previous
  printf 'Type "["\n'
  snap 2500ms count
}
capture_set desktop 14 ""
capture_set narrow 30 ""
capture_set nocolor 14 1
verify_shots
ls "$OUT"/sprint-*.png
