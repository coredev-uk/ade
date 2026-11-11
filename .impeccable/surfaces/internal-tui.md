---
version: 1
slug: "internal-tui"
primary_target: "internal/tui"
related_targets: []
---

# ADE Home (dashboard)

Mode: Operate. Audience: a terminal-first developer starting or resuming the day; job: see what needs them next, start or resume work, read what changed. Runs in any terminal incl. VS Code's and browser terminals; inherits the terminal's background and font.

## Direction contract

THESIS: Every work item is a metro line: its stages are stations, the current stage is the train, and who must act is printed beside it. Refuses the category default of a status-word column in a bordered-pane table.

OWN-WORLD: Terminal's own ground, ink and font. Colour only as line inks (one stable ink per repo), state marks (amber warn, green ok, red failed) and the train. Glyphs are the icon system (no images in a terminal), one vocabulary with an ASCII fallback (host.glyphs = ascii): stations ● done/baseline, ◉ train/current, ◎ interchange (cross-repo), ○ to come; connectors ━ travelled, ─ ahead, ┣━/┗━ branch off a line, ┃ vertical line in previews and dialogs; state ✓ ok, ▲ warn, ✕ failed, ● info/unread, ✎ uncommitted, ↑/↓ ahead/behind, ⟳ updating, ▸ selection. Rounded borders only on overlays (dialogs, drawer). Bold for the selected row and the court; faint for metadata.

STORY: The user reads, in one glance, which items wait on them and why (court + reason), how far each has travelled, and what changed (inbox count, update progress); they act with single keys.

FIRST VIEWPORT: Header line (project tabs, sprint and days left, update progress, unread count). Left 64%: My work rows (ref, title, track, court · reason) with a stage legend above the tracks and guide words in each section rule; then Reviews, Sprint open, Sprint next. Right: preview of the selected item with a vertical line of stations, threads, tasks. Footer key hints. Start work and Inbox open as overlays (centred dialog, right drawer).

FORM: Interchange (fused from the dealt transit-diagram challenger; won both axes over the assigned direction, which was 7th of 7 grounded candidates). seed:d7d4354c3b45c57c

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

Raises: every state shows its evidence; guide words head every list and colour stays quarantined to tracks and marks; columns sit on whole-cell widths and reflow by whole columns, never truncating mid-word without an ellipsis.
