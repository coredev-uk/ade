# ADE

An agentic development environment for the terminal: one dashboard, inside its own zellij session, for the projects, repos, and worktrees you work on, and, as later phases land, for their issues, merge requests, reviews, sprints, AI budget, and agent runs. The plan is in [docs/PLAN.md](docs/PLAN.md), and product constraints are in [PRODUCT.md](PRODUCT.md).

Status: **Phase 0**:

- projects
- bare clones + worktrees
- startup/periodic updates
- audit log
- inbox and notifications
- `ade doctor`
- the zellij launcher
- a Phase‑0 dashboard

Forge features (issues, MRs) start in Phase 1.

## Install and onboard

**Install** a release binary (`ade_<version>_<os>_<arch>.tar.gz`, linux and macOS, amd64 and arm64; one static binary, no runtime dependencies besides `git`) from the GitHub releases page, or with Nix:

```sh
nix run github:coredev-uk/ade -- version      # try it
nix profile install github:coredev-uk/ade     # keep it
```

ADE drives `git`, `zellij`, `lazygit`, `delta`, `nvim` and the forge CLIs; `ade doctor` tells you which are missing. Inside this repo, `nix develop` provides all of them.

**Onboard** with one command. Your team keeps its conventions in a shared manifest (see [docs/manifest.md](docs/manifest.md)); you only supply who you are and where your token comes from:

```sh
export GITLAB_TOKEN=...                       # or GITHUB_TOKEN, or use --token-cmd 'gh auth token'
ade init                                      # interactive: asks for each answer
ade init --yes --forge gitlab --host gitlab.example.com --token-env GITLAB_TOKEN \
    --project 'git+https://gitlab.example.com/acme/tracker.git#.ade.toml'
```

`ade init` verifies the token against the forge *before* writing anything, then writes `~/.config/ade/config.toml` (or `$ADE_CONFIG`), clones the project's repos under `~/ade`, runs the first sync and `ade doctor`, and prints next steps. It never overwrites an existing config without `--force`, which keeps a `config.toml.bak-<timestamp>` copy. Tokens are never written to disk: the config stores an environment variable name or a command. Run `ade` afterwards to open the dashboard.

## Run

```sh
nix develop            # Go toolchain, zellij, lazygit, git, gh/glab, nvim, vhs, golangci-lint
go run ./cmd/ade --help
nix build              # result/bin/ade
```

`ade` with no arguments attaches to (or creates) the zellij session `ade` and shows the dashboard there. Inside zellij, or with `host.mode = "plain"`, it runs in place. `ade dash` always runs the dashboard in the current terminal.

## Commands (Phase 0)

| Command | What it does |
|---|---|
| `ade init` | Onboarding: writes the user config, verifies the token, adds your team's project(s), runs the first sync and `ade doctor`, prints next steps. Prompts in a terminal; scriptable with `--yes`. |
| `ade version` | Prints the version. |
| `ade project add <manifest>` | Registers a project and clones every repo as `<root>/<project>/<repo>/{.bare,main}`. Idempotent. |
| `ade project bootstrap --forge <name> --group <path>` | Generates a team manifest (`.ade.toml`) from an existing GitLab group or GitHub owner: tracker, repos and app values, label patterns, workflow stages, iterations, weight source and run tasks from each repo's root files, validated before it is written. `--publish` opens a merge request adding it to the tracker repo (`--dry-run` shows it first). Never overwrites without `--force`; see [docs/manifest.md](docs/manifest.md#bootstrapping-a-manifest). |
| `ade project list` / `status [name]` | Lists projects, repos, worktrees, ahead/behind, and dirty state. |
| `ade project remove <name> [--dry-run] [--yes] [--force]` | Unregisters a project and deletes its clones. Refuses (exit 2) when a worktree has uncommitted changes or unpushed commits, and says where; `--force` deletes anyway. |
| `ade gc [--dry-run] [--yes]` | Removes worktrees whose merge request is merged or closed, only when clean and pushed; every tree that stays says why. |
| `ade update` | Refreshes manifests, fetches every repo, and fast‑forwards clean trees (never merges, rebases, or stashes). Exits 2 if any repo failed. |
| `ade review <group/project!N> [--no-zellij] [--dry-run]` | Opens the MR in a review workspace tab: the source branch is checked out as a worktree (made when missing) and the tab uses the `review` layout (the diff against the target branch through delta, lazygit, a shell). `--no-zellij` opens no tab and prints the worktree and the `cd … && git diff … \| delta` command. `ade review add\|list\|rm\|submit` collect and send line comments as one batch. |
| `ade item <ref> [--edit]` | Shows an item's record: the snapshot ADE rewrites on each sync, and your notes below `## Notes`, which ADE never touches. `--edit` opens the notes in your editor (`host.editor`, else `$VISUAL`/`$EDITOR`). No network. Your notes are added to agent plan/run/address prompts and to `ade ai summarize`, `draft-reply` and `review` inputs (`--no-notes` leaves them out; they are part of the cache key, and previews say `includes your notes`). |
| `ade issue weight <ref> <n\|none> [--dry-run] [--no-child]` | Sets or clears an issue's weight, always explicitly and audited as `forge.issue.weight`. Writes the GitLab weight field where the forge licenses it (Premium), else swaps the `weight::N` label and removes the old one. The child issue start work made follows unless `--no-child`. `ade ai classify` suggests a weight from similar closed issues. |
| `ade inbox [--unread]` / `inbox show <id>` / `inbox read …` | Notifications. |
| `ade audit [--since 1d] [--action 'git.*']` | The append‑only action log. |
| `ade audit tail [-n 10] [--follow] [--action …]` | The newest entries, then (with `--follow`) every entry any `ade` process appends, until interrupted. `--json` prints JSONL. |
| `ade config check` / `ade doctor` | Validates config and manifests (with their `*.local.toml` overrides); checks tools, paths (warns when `records.dir` is inside a git work tree), the resolved editor, and forge tokens. |
| `ade config schema [--manifest] [--write DIR]` | Prints the JSON Schema of the user config (or of a manifest) generated from the Go structs, for editor completion in taplo / VS Code Even Better TOML. The committed copies are in `docs/schema/`. |
| `ade cache reset [--yes] [--dry-run]` | Deletes `cache.db` and rebuilds the AI result cache and the spend ledger from the records. Says how many unsubmitted review drafts and unread notifications are lost and asks first (`--yes` skips the question); refuses while workspace instances run. |
| `ade ws restart <node> [worktree]` | Restarts a node and everything in the workspace that requires it (dependents first, then back up in dependency order); other instances keep running. Use it on a blocked service. |
| `ade notify send --title … [--kind … --subject …]` | Records a notification; a running dashboard shows it as a pop‑up. |

Every command takes `--json`. `--debug-http` (global) logs every forge and AI HTTP request to stderr, with credentials and bodies never shown.

## Configuration

- **User config:** `~/.config/ade/config.toml`, or `$ADE_CONFIG`. It holds forges and their credential *commands*, `[[projects]]`, and the `[update]` and `[notify]` settings.
- **Project manifests:** `*.ade.toml`, shareable. They define repos, the dependency graphs, and team conventions.
- **Local overrides:** `config.local.toml` and `<name>.ade.local.toml` beside either file, never shared: tables merge key by key, everything else replaces (PLAN.md §2).
- **Editor:** `host.editor` (`auto`, `nvim`, `ade-nvim`, `code`, `$EDITOR`) picks the editor everywhere ADE opens one; `auto` is `code` inside a VS Code terminal, else nvim, else `$EDITOR`.
- **Schema:** `docs/schema/{config,manifest}.schema.json`; put `#:schema <path or URL>` on the first line of a TOML file.
- **Reference:** PLAN.md §2. No secrets ever go in either file.

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
