# ADE command and configuration reference

Setup from zero is in [setup.md](setup.md); the spec is [PLAN.md](PLAN.md).

## Commands

| Command | What it does |
|---|---|
| `ade init` | Onboarding: writes the user config, verifies the token, adds your team's project(s), runs the first sync and `ade doctor`, prints next steps. Prompts in a terminal; scriptable with `--yes`. Self-hosted GitLab: `--host gitlab.example.com/gitlab` (path prefix kept), `--ca-file <pem>` (trusted for the API, manifest fetches and git over https) and `--ssh-port <n>` (clone URLs `ssh://git@host:n/…`); in a terminal a typed host is also asked for the CA and the ssh port. `--ai-gateway <url> --ai-key-env <VAR> [--ai-model-explain\|classify\|draft\|review <model>]` also writes a LiteLLM gateway profile (in a terminal the models are picked from the gateway's `/v1/models`). |
| `ade version` | Prints the version. |
| `ade project add <manifest>` | Registers a project and clones every repo as `<root>/<project>/<repo>/{.bare,main}`. Idempotent. |
| `ade project bootstrap --forge <name> --group <path>` | Generates a team manifest (`.ade.toml`) from an existing GitLab group or GitHub owner: tracker, repos and app values, label patterns, workflow stages, iterations, weight source and run tasks from each repo's root files, validated before it is written. `--publish` opens a merge request adding it to the tracker repo (`--dry-run` shows it first). Never overwrites without `--force`; see [manifest.md](manifest.md#bootstrapping-a-manifest). |
| `ade project list` / `status [name]` | Lists projects, repos, worktrees, ahead/behind, and dirty state. |
| `ade project remove <name> [--dry-run] [--yes] [--force]` | Unregisters a project and deletes its clones. Refuses (exit 2) when a worktree has uncommitted changes or unpushed commits, and says where; `--force` deletes anyway. |
| `ade gc [--dry-run] [--yes]` | Removes worktrees whose merge request is merged or closed, only when clean and pushed; every tree that stays says why. |
| `ade update` | Refreshes manifests, fetches every repo, and fast‑forwards clean trees (never merges, rebases, or stashes). Exits 2 if any repo failed. |
| `ade review <group/project!N> [--no-zellij] [--dry-run]` | Opens the MR in a review workspace tab: the source branch is checked out as a worktree (made when missing) and the tab uses the `review` layout (the diff against the target branch through delta, lazygit, a shell). `--no-zellij` opens no tab and prints the worktree and the `cd … && git diff … \| delta` command. `ade review add\|list\|rm\|submit` collect and send line comments as one batch. |
| `ade item <ref> --watch` | The issue-overview pane: the item live from the cache (stations and stage, court and reason, weight and sprint, each child's MR with pipeline and unresolved threads, description and acceptance with task checkboxes, your notes), refreshed within a moment of a cache or record change (fsnotify; polling every 2 s where unavailable). Runs in every dev tab; on a pipe it prints once. |
| `ade item <ref> [--edit]` | Shows an item's record: the snapshot ADE rewrites on each sync, and your notes below `## Notes`, which ADE never touches. `--edit` opens the notes in your editor (`host.editor`, else `$VISUAL`/`$EDITOR`). No network. Your notes are added to agent plan/run/address prompts and to `ade ai summarize`, `draft-reply` and `review` inputs (`--no-notes` leaves them out; they are part of the cache key, and previews say `includes your notes`). |
| `ade issue move <ref> [--to next\|<sprint title or key>] [--with-children\|--no-children] [--dry-run] [--json]` | Moves a tracker issue to the next sprint (or the sprint `--to` names), audited as `forge.issue.sprint`, and raises `issue.iteration_changed` at once. Sets the GitLab iteration (Premium), the milestone (Free, GitHub) or swaps the `Sprint::N` label, by `tracker.iterations.source`. `next` is the first sprint starting after the issue's own sprint; ADE creates no sprint and the error says what to create. Child issues that carry the same sprint follow; `--with-children` moves every open child, `--no-children` none. ADE never closes issues. |
| `ade issue weight <ref> <n\|none> [--dry-run] [--no-child]` | Sets or clears an issue's weight, always explicitly and audited as `forge.issue.weight`. Writes the GitLab weight field where the forge licenses it (Premium), else swaps the `weight::N` label and removes the old one. The child issue start work made follows unless `--no-child`. `ade ai classify` suggests a weight from similar closed issues. |
| `ade ai summarize\|classify\|reply\|review\|models [--profile <name>]` | AI actions on one item through the active profile (pre-flight, caps, cache and audit apply to all). `classify` uses the profile's `classify` model (falls back to `explain`) and asks the gateway for a JSON-schema answer; `models` lists the gateway's models and marks which role uses which. Roles and a LiteLLM example: [setup.md](setup.md#through-a-litellm-gateway). |
| `ade inbox [--unread]` / `inbox show <id>` / `inbox read …` | Notifications. |
| `ade audit [--since 1d] [--action 'git.*']` | The append‑only action log. |
| `ade audit tail [-n 10] [--follow] [--action …]` | The newest entries, then (with `--follow`) every entry any `ade` process appends, until interrupted. `--json` prints JSONL. |
| `ade config check` / `ade doctor` | Validates config and manifests (with their `*.local.toml` overrides); checks tools, paths (warns when `records.dir` is inside a git work tree), file watching (fsnotify, or polling), the resolved editor, forge tokens, each forge's `ca_file`, the GitLab version and edition (with a warning per feature this release lacks: bulk publish, reviewers, work-item GraphQL, cadences) and the headless-CDE group (display, VS Code remote CLI, OSC 52 clipboard, nix, direnv). |
| `ade config schema [--manifest] [--write DIR]` | Prints the JSON Schema of the user config (or of a manifest) generated from the Go structs, for editor completion in taplo / VS Code Even Better TOML. The committed copies are in `schema/`. |
| `ade cache reset [--yes] [--dry-run]` | Deletes `cache.db` and rebuilds the AI result cache and the spend ledger from the records. Says how many unsubmitted review drafts and unread notifications are lost and asks first (`--yes` skips the question); refuses while workspace instances run. |
| `ade ws restart <node> [worktree]` | Restarts a node and everything in the workspace that requires it (dependents first, then back up in dependency order); other instances keep running. Use it on a blocked service. |
| `ade vscode workspace <item-ref\|worktree> [--open]` | Writes a multi-root VS Code workspace (`<n>-<slug>.code-workspace`, in the item's records directory) with every repo of the project: the item's worktrees where it has them, baselines elsewhere, plus the run graph as tasks. `--open` runs `code`; otherwise it prints the path. Home `v` does the same. |
| `ade notify send --title … [--kind … --subject …]` | Records a notification; a running dashboard shows it as a pop‑up. |

Every command takes `--json`. `--debug-http` (global) logs every forge and AI HTTP request to stderr, with credentials and bodies never shown.

## Configuration

- **User config:** `~/.config/ade/config.toml`, or `$ADE_CONFIG`. It holds forges and their credential *commands*, `[[projects]]`, and the `[update]` and `[notify]` settings.
- **Project manifests:** `*.ade.toml`, shareable. They define repos, the dependency graphs, and team conventions.
- **Local overrides:** `config.local.toml` and `<name>.ade.local.toml` beside either file, never shared: tables merge key by key, everything else replaces (PLAN.md §2).
- **Editor:** `host.editor` (`auto`, `nvim`, `ade-nvim`, `code`, `$EDITOR`) picks the editor everywhere ADE opens one; `auto` is `code` inside a VS Code terminal, else nvim, else `$EDITOR`.
- **Dev environment:** `s` (start work) and `w` open a zellij tab per worktree: a main pane (your editor, or `omp` started with an ADE-generated *implement* skill that carries the ticket, MR, conventions and your notes and stops for review without pushing, or a shell), the item's live overview (`ade item <ref> --watch`), lazygit, a shell, and one pane per service (none starts with the tab: a pane waits for Enter, Ctrl-C stops one, Enter starts it again; the `services` pane starts them all). `host.layout = "code" | "agent" | "manual"` is your default main pane (it beats the manifest workspace's `layout`); `a` in the start dialog, `ade start --layout` and `W` on Home pick the other one for a tab. See PLAN.md §6.6.
- **Fresh without a sync:** dialogs (`c` classify, `s` start work, `=` weight, `n` notes, `>` sprint move) read only their own issue and never wait for a running sync; after a write, and after `ade start` or the start dialog created a child and MR, that one item is read again and its cache row replaced, so Home and `ade item --watch` show it at once (no `ade sync`). The next full sync reconciles anything the one-item read could not see.
- **Commit prefix:** `conventions.commit_prefix = "GL-{parent}"` in the manifest prefixes the first commit of the MR only: ADE's agent commit while the branch has none ahead of the target, the GitHub start commit and the squash message it sends. It is written per worktree (`ade.parent`, `ade.commitPrefix`, `ade.target`) for your own commit-msg hook; manifest.md has the hook.
- **Schema:** `schema/{config,manifest}.schema.json`; put `#:schema <path or URL>` on the first line of a TOML file.
- **Self-hosted forges:** `[forges.<name>]` takes `ca_file` (PEM bundle; `SSL_CERT_FILE` also works), `ssh_port` and `max_concurrency` (default 4) besides `url`, which may carry a path prefix. Manifest: `repos.<r>.devshell = "auto" | "direnv" | "nix" | "none"` runs a repo's panes, tasks and services inside its dev shell ([manifest.md](manifest.md#dev-shell-per-repo)). Setup: [setup.md](setup.md#self-hosted-gitlab).
- **Palette:** `:` opens the command palette on Home, Worktrees and the MR page; every key of each page's help is an entry, so a terminal that swallows keys (a web tty) still reaches every action by name.

## Sandbox

Synthetic GitLab and GitHub data for development. Nothing here touches your real config:

```sh
. scripts/sandbox/env.sh        # isolates state under ./.sandbox, reads GITLAB_PAT from .env
go run ./cmd/ade project add scripts/sandbox/manifests/gitlab.ade.toml
go run ./cmd/ade project add scripts/sandbox/manifests/github.ade.toml
go run ./cmd/ade dash
```

`scripts/sandbox/{gitlab,github}-seed.sh` (re)create the remote sandbox data idempotently.

## Develop

```sh
go test ./...
golangci-lint run ./...
nix flake check
```
