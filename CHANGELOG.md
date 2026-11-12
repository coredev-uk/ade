# Changelog

## Unreleased

- **Breaking default:** an unset `labels.workflow.sync` now means `ask`, not `auto`. ADE no longer moves labels on a shared board unless the manifest says `sync = "auto"`. With `ask`, `ade start` moves the label only with `--move-label` or the dialog toggle, and `ade sync` reports pending moves as warnings and in the inbox. `ade project bootstrap` writes `sync = "ask"`.
- **Personal namespaces:** GitLab trackers in a user namespace sync. Capabilities skip the group endpoints and sample the tracker project, so weights go to the weight field. Sprints come from the tracker project's milestones. `ade doctor --probe` agrees with sync, and the sprint-move hint names the project.
- **`ade start --dry-run`** accounts for the workflow label: the move as a write step, or a note saying why it will not happen.
- **`ade gc`** keeps a branch without an upstream unless HEAD is the head of one of its MRs or sits on a remote branch that still exists after a fetch. A local branch as upstream does not count as pushed, and an unreadable worktree status keeps the tree. The dry run says why each tree counts as pushed.
- **`ade adopt`** brings an existing branch, local or from another clone (`--from`), into the layout and writes the `ade.*` worktree keys, with no forge writes.
- **`ade items --json` and `ade item`** list each item's worktrees: path, branch, dirty, ahead and behind.
- **Agents** run with `ADE_AGENT_RUN=1`, in `ade agent` runs and in the zellij agent pane, so a global `pre-push` hook can stop an agent's own push (sample in docs/manifest.md). Agent prompts carry the expanded commit prefix.
- **Baseline folder:** the docs and the remaining code paths use the folder named after the default branch (`develop/`, not always `main/`).

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
