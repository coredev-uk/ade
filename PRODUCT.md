# Product

<!-- impeccable:product-schema 1 -->

## Platform

terminal

ADE is a terminal application (TUI + CLI). It is not web, iOS, or Android. It must work in a local terminal, in VS Code's integrated terminal (including over Remote-SSH), and in browser-based terminals (xterm.js) that cloud development environments provide.

## Stack

User-decided: Go with Bubble Tea v2 (`charm.land/bubbletea/v2`, `lipgloss/v2`, `bubbles/v2`) for the TUI. Zellij is the workspace/session host, and a Nix flake supplies the toolchain and runtime tools. ADE ships as one static binary (`CGO_ENABLED=0`).

## Users

- **Primary:** a developer who works mostly in the terminal and wants to stop context-switching into the GitLab/GitHub web UI to find, start, track, review, and report on work. Usually works inside a remote cloud dev environment.
- **Secondary:** their teammates. They share project manifests and run the same tool, each with their own AI provider setup and budget, or with AI disabled.

## Product Purpose

ADE replaces the browser-based forge workflow with one terminal home: assigned issues and their MRs/PRs, iteration context, review threads, and progress, plus actions to start, review, and report on work. AI is an optional accelerator (summaries, classification, standups, plans, agent runs) that is metered against an explicit budget. Success: a full day of issue → branch → MR → review → merge → standup happens without opening the forge web UI, and AI spend stays inside the limits the user set.

## Positioning

Forge TUIs (`glab`, `gh-dash`, lazygit) each cover one slice. ADE models the *work item* end to end: a tracker issue, the child issue in the code project, its branch, worktree, MR, pipeline, review threads, and the terminal workspace where the work happens. It treats AI spend as a budgeted resource with per-action cost, caching, and a hard off-switch, not as a free side effect.

## Operating Context

- Forges: GitLab (first) and GitHub, including self-hosted GitLab.
- **Tracker-project pattern:** planning issues and boards live in a dedicated tracker project. When someone takes a tracker issue, a child issue is created in the affected code project, and the MR/PR is opened from that child. Today teams link parent and child inconsistently, so ADE establishes and enforces the convention.
- Iterations/sprints come from different sources per team: GitLab iterations, milestones, labels, GitHub Projects iteration fields, or a configured cadence.
- Agile rituals: daily standup (previous working day + today), sprint review.
- Environments: local Linux/macOS terminals; remote CDEs reached through VS Code Remote-SSH or a browser terminal; Nix available in the environment image. Work and personal use run on different machines, each with its own user config (forges, AI profiles, budgets). Shared manifests travel through git; records stay on their machine.
- AI access varies per user: Claude subscription via an agent CLI (omp, Claude Code), direct provider API keys (Anthropic, OpenAI, Moonshot/Kimi), or an OpenAI-compatible gateway (e.g. LiteLLM) that mixes providers and enforces spend limits.
- Companion tools: zellij, lazygit, Neovim or VS Code as the editor, agent CLIs (omp, claude, codex).

## Capabilities and Constraints

- Configuration has two layers: **user-local config** (identity, forge credentials via commands/env, AI profiles, budgets) and **shareable project manifests** (tracker, repos, tasks, workspaces, conventions). Manifests never contain secrets. ADE keeps manifests, repo clones, and the forge cache up to date.
- Every feature works with AI fully disabled. AI only adds summaries, drafts, and automation; it is never required for core functions.
- Every terminal action must also work when ADE runs inside VS Code's integrated terminal (key conflicts, `code` CLI as editor) and without zellij (plain host, VS Code tasks).
- Every non-TUI command offers machine-readable output (`--json`) so scripts and VS Code tasks can drive it.
- Repos can depend on other repos at checkout time (web work needs an api tree) and at run time (web's dev server needs api's dev server ready). Manifests declare both graphs. ADE resolves which tree each dependency uses (same work item's branch, an explicit pin, or a default-branch baseline), starts services in dependency order with readiness checks and per-instance ports, shares identical instances across workspaces, and rejects cycles.
- Everything ADE does is recorded locally: an append-only audit log of every forge write, AI call (with model, tokens, and cost), and agent run, plus a per-work-item record (forge snapshot, AI outputs, plans, run records, personal notes) kept as plain Markdown. AI output is durable, so paid work is never lost or bought twice. These records hold real work data and never live inside a repository.
- AI budgets come from each provider's own billing source: LiteLLM key spend and budget, Moonshot balance, OpenRouter key limits, and Anthropic/OpenAI admin cost reports where an admin key exists. ADE merges these with locally metered per-request cost. Subscriptions without a quota API (Claude via omp) use list-price usage against a soft cap.
- There is no per-project data-sensitivity policy: work runs in closed environments, and AI egress is whatever each machine's profiles point at (user decision). ADE has no telemetry and records where every AI call went.
- GitLab support is built from the official API docs; no paid instance is available for development. Paid-tier features are capability-probed at runtime and degrade cleanly on Free.
- Privacy: committed code, fixtures, and docs contain no real organisation, host, project, or person data. Test data is synthetic.
- Undecided: authoritative remaining-quota reporting for subscription-based AI (only usage is observable today); GitHub parity timing.

## Evidence on Hand

None yet: greenfield repository. Do not invent users, teams, adoption numbers, or benchmarks.

## Product Principles

1. **The work item is the unit.** Every screen and action revolves around one tracker issue → child issue → branch → MR chain and who needs to act next.
2. **Terminal-native, host-agnostic.** Zellij when available. VS Code terminals and plain shells are first-class, not fallbacks that rot.
3. **AI is metered, cached, and optional.** Show cost before spending it, never spend twice on unchanged input, and degrade cleanly to manual.
4. **Cache-first and fast; records are forever.** The UI renders from local state immediately and syncs in the background, and the network never blocks navigation. The cache is disposable. The audit log and item records are not.
5. **Conventions as code.** Branch names, child-issue links, MR templates, and workspaces come from shared manifests, so a team behaves consistently.

## Accessibility & Inclusion

Respect `NO_COLOR` and light/dark terminal themes. Never encode state by colour alone (pair it with glyph or text). Offer an ASCII glyph fallback (no Nerd Font requirement). Stay usable at 80×24. Everything is fully keyboard-operable, and mouse support is optional.
