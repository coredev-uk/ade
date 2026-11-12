#!/usr/bin/env bash
# Capture the Work-tab overview pane (`ade item <ref> --watch`) for visual
# review with VHS. Output: .impeccable/review/overview-*.png
#
#   scripts/verify/capture-overview.sh      # desktop, narrow, no-colour, ascii sets
#
# States: top (stage, child issues and their MRs, description), scrolled (j)
# and the end (G) where the content outgrows the window (narrow, tall, ascii),
# the MR as the ref (same work item). Data: the
# in-process fake GitLab (Premium); nothing leaves the machine and no real forge
# is written. The ascii set runs with host.glyphs = "ascii".
. scripts/verify/capture-ai-lib.sh
ai_capture_init overview
G=ade-demo-env

# watch_run REF [ASCII]: point run.sh at the overview of REF.
watch_run() {
  local ref=$1 ascii=${2:-}
  cp "$VERIFY_TMP/cfg-base.toml" "$VERIFY_TMP/cfg-watch.toml"
  [[ -z $ascii ]] || printf '\n[host]\nglyphs = "ascii"\n' >>"$VERIFY_TMP/cfg-watch.toml"
  sed -i "s|^exec .*|exec \"$ADE\" item \"$ref\" --watch|; s|ADE_CONFIG=\"[^\"]*\"|ADE_CONFIG=\"$VERIFY_TMP/cfg-watch.toml\"|" "$VERIFY_TMP/run.sh"
}

top_tape() { shot top 1200ms; }
scroll_tape() {
  shot top 1200ms
  printf 'Type "j"\nType "j"\nType "j"\n'
  shot scrolled 400ms
  printf 'Type "G"\n'
  shot end 400ms
}

# The pane shows the whole work item whichever ref names it (tracker issue,
# child issue or MR): the MR ref is captured once to prove it.
watch_run "$G/tracker#1"
SET_NAME=desktop; capture desktop 14 "" "$(top_tape)"
SET_NAME=nocolor; capture nocolor 14 1 "$(top_tape)"
SET_NAME=narrow; capture narrow 30 "" "$(scroll_tape)"
SET_NAME=tall; capture tall 22 "" "$(scroll_tape)"
watch_run "$G/tracker#1" ascii
SET_NAME=ascii; capture ascii 22 1 "$(scroll_tape)"
watch_run "$G/api!1"
SET_NAME=mr; capture mr 14 "" "$(top_tape)"
verify_shots
ls "$OUT"/overview-*.png
