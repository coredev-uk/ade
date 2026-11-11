---
name: ADE
description: Terminal home for forge work, drawn as a metro diagram in the terminal's own ink.
colors:
  line-scarlet: "#e0453a"
  line-cobalt: "#3b7dd8"
  line-amber: "#e8a33d"
  line-green: "#3fa66b"
  line-violet: "#b05fd0"
  line-teal: "#2bb3b3"
  state-warn: "#e8a33d"
  state-ok: "#3fa66b"
  state-failed: "#e0453a"
  overlay-rule: "#666666"
typography:
  body:
    fontFamily: "the terminal's own monospace font"
    fontWeight: 400
  emphasis:
    fontFamily: "the terminal's own monospace font"
    fontWeight: 700
rounded:
  overlay: "rounded box-drawing corners (╭╮╰╯)"
spacing:
  cell: "1 terminal cell"
  repo-column: "10 cells"
components:
  overlay-dialog:
    textColor: "terminal foreground"
    rounded: "{rounded.overlay}"
    padding: "0 1 cell"
  overlay-drawer:
    textColor: "terminal foreground"
    rounded: "{rounded.overlay}"
    padding: "0 1 cell"
    width: "60 cells (full width below 100 columns)"
---

# Design System: ADE

## Overview

**Creative North Star: "The Interchange"**

Every piece of work is a line on a metro diagram. Stations are stages (or, in the Phase‑0 Home, a repo's worktrees), the current stage is the train, and who must act next is printed beside it. The world lends ADE exactly four things: line inks, a glyph vocabulary, a dense one‑cell rhythm, and the station line as its signature move. Layout, navigation, and controls stay standard terminal conventions (`j/k`, `enter`, `esc`, `?`, overlays).

ADE paints no background and chooses no font. It inherits the user's terminal ground, foreground, and typeface, in dark and light themes alike, and it keeps working under `NO_COLOR`, because glyphs carry every state.

**Key Characteristics:**

- The terminal's own ground, ink, and font. Colour appears only as line inks and state marks.
- A glyph vocabulary is the icon system, with an ASCII fallback (`host.glyphs = "ascii"`).
- Bold for what is selected or needs you; faint (SGR 2) for metadata.
- Section rules carry guide words (`2 repos · 3 trees · updated 8s ago`).
- Overlays (dialog, drawer) are the only bordered surfaces.

## Colors

The base is the user's own terminal palette. ADE adds line inks, one per repo, and three state marks.

### Primary (line inks)

These are assigned per repo by a stable hash of the repo name, so the same repo keeps its colour across projects and sessions:

- **Line Scarlet** (#e0453a)
- **Line Cobalt** (#3b7dd8)
- **Line Amber** (#e8a33d)
- **Line Green** (#3fa66b)
- **Line Violet** (#b05fd0)
- **Line Teal** (#2bb3b3)

Inks colour a repo's stations and its branch connectors, and nothing else.

### Secondary (state marks)

- **Warn Amber** (#e8a33d): uncommitted changes, behind or diverged trees, skipped pulls, warning severity.
- **OK Green** (#3fa66b): success severity marks.
- **Failed Red** (#e0453a): failures, failed fetches, error severity.

### Neutral

- **Terminal foreground / background**: all text and ground, inherited.
- **Overlay Rule** (#666666): the rounded border of dialogs and the drawer.

### Named Rules

**The Quarantine Rule.** Colour lives only in line inks and state marks. Every other character is terminal ink: bold, normal, or faint.

**The Evidence Rule.** No state is colour alone. Each carries a glyph and a word (`✎ uncommitted`, `↓2 behind`, `▲` with a title), so `NO_COLOR` loses no meaning.

## Typography

**Font:** the terminal's own monospace face. ADE never sets or assumes a font.

**Character:** one face at one size. Hierarchy comes only from weight (bold), intensity (faint), and position.

### Hierarchy

- **Emphasis** (bold): the product mark `ADE`, project names in section rules, the selected row's label, the preview title, overlay titles, key names in hints.
- **Body** (normal): tree names, values, toast text, unread inbox titles (bold) vs read ones (normal).
- **Metadata** (faint): guide words, status words that need no action (`up to date`), timestamps, kinds, key descriptions.

### Named Rules

**The No‑Mid‑Word Rule.** Anything cut to fit ends in `…`. Prose that cannot be cut wraps instead.

## Layout

- **Two‑pane, ≥ 100 columns:**
  - header line, blank line
  - list at 64% of the width, then ` │ `, then the preview pane (one cell of right margin)
  - footer line
- **Single‑pane, < 100 columns:** the list fills the width. `tab` swaps it for the selected tree's details. Overlays become full‑width.
- **Fixed rhythm:** every row is one line. Repo names sit in a 10‑cell column; branch stations indent under their repo with `┣━`/`┗━`. Statuses are right‑aligned. Sections are separated by one blank line.
- **Scrolling:** the list scrolls so the selected row always stays visible.
- **Header:**
  - left: scope (`ADE · 2 projects · 4 repos`)
  - centre: update state (`⟳ updating 3/4` / `⟳ updated 40s ago` / the warning title)
  - right: unread count
- **Footer:** a toast when there is one, otherwise key hints.

## Elevation & Depth

The base layer is flat. Depth is one step: overlays composite above the screen. The help dialog is centred; the inbox drawer is right‑aligned and full height. There are no shadows. Overlays occlude what is beneath them completely.

## Shapes

- **Borders:** rounded box‑drawing borders (`╭╮╰╯`), only on overlays.
- **Rules:** section dividers are a single horizontal rule (`─`) between the title and the guide words.
- **Vertical line:** `┃`/`│` marks a line in previews and dialogs (the metro line seen side‑on).

## Components

### Section rule

` <project> ──────── <guide words>`. Bold title, faint rule, faint guide words, right‑aligned.

### Tree line (signature)

- **Columns:** marker (`▸` when selected), repo name (first row of a repo only), station, label, then the right‑aligned status.
- **Stations:**
  - a baseline is `●` in the repo ink
  - a work branch is `┣━◉` (or `┗━◉` for a repo's last branch), with the connector and train in the repo ink
  - a missing repo is a faint `○` with `not cloned · U to update`
- **Status:** `up to date` (faint), `↑n to push`, `↓n behind` (amber), `↑a ↓b diverged` (amber), `no upstream` (faint), plus `✎ uncommitted` (amber), joined by ` · `.

### Preview pane

- **Title:** a bold `project / repo / tree`, then a faint kind line.
- **Key/value block:** faint 10‑cell keys.
- **`Last update`:** this repo's failures and skips, if any.
- **`Recent`:** up to eight inbox rows: severity glyph, title (bold if unread), and a right‑aligned faint age.

### Toast

One footer line: severity glyph and title.

- Non‑failures expire after 5 s.
- Failures stay until `esc`.
- Stacked toasts show `+n earlier`.
- Transient events (for example a no‑op update) appear only as toasts, never in the inbox.

### Inbox drawer

- **Shape:** a rounded overlay, 60 cells wide (full width when narrow), right‑aligned, full height.
- **Contents:** rows of severity glyph, title, and age, with the kind beneath. The selected row expands up to four lines of detail. The empty state explains what will land there.
- **Keys:** `r`, `R`, `esc`.

### Help dialog

A rounded, centred overlay. Each key row starts with a faint `┃`, then the bold key in a 7‑cell column, then the description.

## Do's and Don'ts

### Do:

- **Do** draw every new surface's progress or relationship as a station line (`●━━●━━◉──○`) in the relevant repo's ink.
- **Do** pair every state with a glyph and a word; test under `NO_COLOR=1` and `host.glyphs = "ascii"`.
- **Do** put scope and counts in guide words on section rules, not in separate headers.
- **Do** make overlays full‑width below 100 columns, so no cut fragments show around them.
- **Do** keep update and notification state in the header and footer; never block the list on the network.

### Don't:

- **Don't** paint a background colour or choose a font: the terminal's ground and face belong to the user.
- **Don't** use colour outside line inks and state marks (The Quarantine Rule).
- **Don't** put borders on the base layer. Borders mark overlays only.
- **Don't** truncate mid‑word without `…`. Wrap prose instead (The No‑Mid‑Word Rule).
- **Don't** record no‑op status (for example "all repos up to date") in the inbox. It is a toast.
