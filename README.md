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
| `ade project list` / `status [name]` | Lists projects, repos, worktrees, ahead/behind, and dirty state. |
| `ade update` | Refreshes manifests, fetches every repo, and fast‑forwards clean trees (never merges, rebases, or stashes). Exits 2 if any repo failed. |
| `ade inbox [--unread]` / `inbox show <id>` / `inbox read …` | Notifications. |
| `ade audit [--since 1d] [--action 'git.*']` | The append‑only action log. |
| `ade config check` / `ade doctor` | Validates config and manifests; checks tools, paths, and forge tokens. |
| `ade notify send --title … [--kind … --subject …]` | Records a notification; a running dashboard shows it as a pop‑up. |

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
