# Changelog

## v0.1.0

First release. ADE is a terminal dashboard and CLI that replaces the GitLab web workflow (GitHub for personal projects), built around zellij.

- **Home:** your work items as station lines (assigned → merged) with court, reason, weight and sprint load; reviews requested, sprint, next and previous sprint; search, command palette, filters, activity, notes, weights, classify, move to the next sprint.
- **Start work and the work tab:** child issue, branch, draft MR and worktree in the chosen repos, then a zellij tab with your editor, a shell or an omp session carrying the issue, a live issue overview, lazygit and service panes you start on demand (dependencies first, `API_URL` wired). `v` opens a multi-root VS Code workspace for the project.
- **MR page and review:** overview with AI summary, threads, highlighted diff, CI; reply, resolve, approve, ready, merge; line comments and agent review drafts sent as one batch.
- **Agents:** plan, approve, run, review in lazygit, push, within a per-run cost cap; transcripts stay in the item record.
- **Agile:** standup and sprint review by count and weight (unweighted never counts as 0), velocity, burn-down, cycle time, optional AI narrative.
- **AI and budget:** omp, Anthropic and OpenAI-compatible drivers (LiteLLM gateway with a classification model role), result cache, ledger over the audit log, caps and pre-flight, budget and log pages.
- **Setup:** `ade init`, `ade project bootstrap` (manifest from a forge group), `ade setup` for Coder startup scripts, self-hosted GitLab (custom CA, path prefix, SSH port, version gating), headless CDEs (VS Code remote CLI, web tty).
- **Notifications** live inside the dashboard only: pop-ups, a notification centre, unread count and row markers.
- **ade-nvim:** an isolated Neovim with the snacks.nvim picker (`nix run github:coredev-uk/ade#ade-nvim`).

Install: `nix profile install github:coredev-uk/ade`, or download a release archive. Setup guide: [docs/setup.md](docs/setup.md).
