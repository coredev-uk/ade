# ADE

[![CI](https://github.com/coredev-uk/ade/actions/workflows/ci.yml/badge.svg)](https://github.com/coredev-uk/ade/actions/workflows/ci.yml)

An agentic development environment for the terminal. One dashboard, inside its own [zellij](https://zellij.dev) session, replaces the GitLab/GitHub web workflow for the projects you work on: issues, merge requests and reviews, sprints, per-ticket worktrees and dev tabs (editor or omp agent, lazygit, services), AI summaries and agent runs with hard budget caps, and notifications. It is one static Go binary; GitLab is the work forge, GitHub covers personal projects.

The spec is [docs/PLAN.md](docs/PLAN.md) (phases 0–7 are implemented), product constraints are in [PRODUCT.md](PRODUCT.md), the visual system in [DESIGN.md](DESIGN.md).

## Getting started

Full guide: **[docs/setup.md](docs/setup.md)** (installing Nix, tools, tokens, first run, troubleshooting). Unattended machine setup (Coder workspaces, new laptops): `ade setup`, see [Coder / CDE](docs/setup.md#coder--cde).
Self-hosted GitLab (internal CA, path prefix, custom ssh port) and headless Coder workspaces: [docs/setup.md](docs/setup.md#self-hosted-gitlab); every dashboard action is also reachable by name from `:`.

```sh
# 1. Nix with flakes (skip if you have it): https://nixos.org/download or https://determinate.systems/nix-installer
curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix | sh -s -- install

# 2. Try ADE without installing, or keep it
nix run github:coredev-uk/ade -- version
nix profile install github:coredev-uk/ade

# 3. Put zellij, lazygit and git on PATH (nix profile install nixpkgs#zellij nixpkgs#lazygit nixpkgs#git), then:
export GITLAB_TOKEN=...            # PAT with api, read_repository, write_repository
ade init                           # teammate: asks for forge, host, token source, project manifest
ade doctor                         # what is missing?
ade                                # open the dashboard (zellij session "ade")
```

Team lead creating the shared manifest first: `ade project bootstrap --group <group>`, review, then `--publish`.
No forge handy? `nix develop -c bash scripts/demo.sh` runs everything against a local fake GitLab.

## Documentation

| | |
|---|---|
| [docs/setup.md](docs/setup.md) | Install, tools, tokens, first run, daily loop, troubleshooting |
| [docs/reference.md](docs/reference.md) | Every command, configuration keys, sandbox, developing ADE |
| [docs/manifest.md](docs/manifest.md) | The shared team manifest, conventions, commit prefix and hook |
| [docs/schema/](docs/schema) | JSON Schemas for the config and manifest (editor completion) |
| [docs/billing/](docs/billing/README.md) | AI spend and budget sources |
| [docs/PLAN.md](docs/PLAN.md) | The spec |

## Develop

```sh
nix develop            # Go, zellij, lazygit, git, gh, glab, delta, nvim, golangci-lint, ...
go test ./...
golangci-lint run ./...
nix flake check
```

The dashboard and the overview pane have golden-file tests (`internal/tui/testdata/golden`, `internal/overview/testdata/golden`). After a deliberate UI change, `go test ./internal/tui ./internal/overview -run TestGolden -update` rewrites them; read the diff before committing.
