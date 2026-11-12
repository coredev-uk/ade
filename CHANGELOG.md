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
- **Names:** people show as `Name (username)` in the dashboard, MR page, overview, item records, notifications and `ade mr`.
- **Sprint list:** it holds only sprint tickets whose tracker issue has no assignee, and shows each ticket's author. A ticket assigned to someone else reads `assigned to <name>`, not `unassigned`. Assignment matches by user ID first.
- **My work:** a code-repo issue appears under its tracker item when they are tied by GitLab's work-item hierarchy, a `Parent:` line or a `relates_to` link, even when the tracker item is not yours or is outside the sprint window. `ade start` also sets the hierarchy parent when GitLab allows that pairing, and warns without failing when it does not.
- **AI answers** cut at the output limit are retried once with a larger limit. If still cut, they show a note and are not cached. Classify, sprint-parent and thread summaries have larger limits, and their old cached answers are renewed.
- **Weight suggestion** ranks closed issues by the shared title words that matter (inflected stop words no longer match), takes the score-weighted median of the five nearest, says when only one issue matched, and lists the matches with their weights.
- **Bare `ade`** lands on the dashboard: it focuses (or recreates) the `home` tab of the `ade` session, also when run inside zellij.
- **Install hooks:** `[repos.<r>.hooks] install` runs once per worktree (again when the commands change, or with `ade ws install --force`), inside the repo's dev shell.
- **`ade start --branch <repo>=<branch>`** (and `b` in the start dialog) pins a sibling repo to a named branch: an existing branch and its MR are reused, a new name is created. The dry run notes the branch per repo.
- **AI answers cut short** show a note beside the text, never inside it, so a draft reply or a parsed answer never contains it. The same prompt is not re-bought for 30 minutes unless you refresh, and the pre-flight shows the retry's worst case. Summaries, draft replies, standups and sprint reviews are renewed once.
- **Project dotfiles:** a `[dotfiles]` table in the manifest names the team dotfiles repo (the one used with `coder dotfiles`). `ade setup` and `ade dotfiles` clone or update it and run its install script, or symlink its dotfiles into `$HOME`, as Coder does. Inside Coder they skip a repo Coder already applied. `[dotfiles]` in the user config can switch it off or point elsewhere, and `ade doctor` reports it.
- **Classify and weight** work on app-repo issues and closed issues, skip a project whose manifest cannot be read, and show the real error when the issue cannot be read.
- **Facets from the parent:** an app-repo issue or MR without its own type, apps or weight takes them from its tracker issue, on Home rows, review rows and the detail pane.
- **Reviews** no longer list draft MRs. A draft marked ready appears then.
- **Notes on work:** `ade note <ref> <text> [--blocked]` and `N` on Home add a dated local note (never sent to the forge). The side panel shows the newest three, and a row whose newest note is blocked is marked.
- **CI job log** fills the screen, reads the last 512 KiB, and searches with `/`, `n` and `N`.
- **Sprint review** with several projects opens on the first project with sprints instead of failing. `p` switches projects; the CLI says which project it chose.
- **Background services:** opening a workspace for the first time also opens an unfocused `<tab> · services` tab. It runs the install hooks first, then each service in dependency order, with no "waiting to run" panes. A required service with a port and no `ready` check counts as ready when its port accepts connections. `[workspaces.<w>] services = "manual"` keeps the old on-demand strip. A waiting pane reports every 30 seconds and stops with the reason when its dependency or the install failed, or after `--wait-timeout` (10 minutes by default). A lockfile change at the worktree root reruns the install.

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
