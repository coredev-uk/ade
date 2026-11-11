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
| `ade project add <manifest>` | Registers a project and clones every repo as `<root>/<project>/<repo>/{.bare,main}`. Idempotent. |
| `ade project list` / `status [name]` | Lists projects, repos, worktrees, ahead/behind, and dirty state. |
| `ade update` | Refreshes manifests, fetches every repo, and fast‑forwards clean trees (never merges, rebases, or stashes). Exits 2 if any repo failed. |
| `ade inbox [--unread]` / `inbox show <id>` / `inbox read …` | Notifications. |
| `ade audit [--since 1d] [--action 'git.*']` | The append‑only action log. |
| `ade config check` / `ade doctor` | Validates config and manifests; checks tools, paths, and forge tokens. |
| `ade notify test` | Sends a test notification through every channel. |

Every command takes `--json`.

## Configuration

- **User config:** `~/.config/ade/config.toml`, or `$ADE_CONFIG`. It holds forges and their credential *commands*, `[[projects]]`, and the `[update]` and `[notify]` settings.
- **Project manifests:** `*.ade.toml`, shareable. They define repos, the dependency graphs, and team conventions.
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
