# ADE: agent instructions

ADE is an agentic development environment for the terminal: a Bubble Tea dashboard and CLI over GitLab (first) and GitHub, with zellij work tabs, worktrees and agent runs. Go module `github.com/coredev-uk/ade`, one static binary (`CGO_ENABLED=0`). These rules are for every agent and harness; they apply to people too.

## Workflow

1. Work on a branch of its own, never on `main`: `<type>/<slug>` from `origin/main`, where `<type>` is the Conventional Commit type (`feat/code-server-session`). A worktree per change keeps parallel work apart.
2. Implement, with the docs, tests and `CHANGELOG.md` bullet that describe the change in the same PR.
3. Run the checks (Commands below) before you push: `nix flake check`, `golangci-lint run ./...` and `nix build` are what CI runs.
4. Push and open the PR with a Conventional Commit title. The description says what changed and why, the commands you ran with their result, and anything left.
5. Merging is the maintainer's call (@coredev-uk). A merged PR whose changelog starts with a new version heading releases on its own (Releases below).

## Boundaries

- Always: one change per branch and PR; run the checks before a PR; update the docs and `CHANGELOG.md` with the code; regenerate `docs/schema/` when a config or manifest struct changes; run anything that talks to a forge against the sandbox (`. scripts/sandbox/env.sh`), never your own config.
- Ask first: cutting a release (renaming `## Unreleased`); changing a default users already rely on (it is a **Breaking** changelog entry); bumping Go, nixpkgs (`flake.lock`) or a Go dependency; editing `.github/workflows/`; anything that writes to a real forge.
- Never: commit or push to `main`; push a `v*` tag by hand to work around the release workflow; print, log or commit tokens, `.env` or `*.local.toml`; commit captured real forge traffic, hosts or project names (fixtures are synthetic); hand-edit `docs/schema/*.json` or golden files instead of regenerating them; edit a released `CHANGELOG.md` section.

## Commands

Tools come from the flake: `nix develop -c <cmd>`, or let direnv load it (`.envrc` is `use flake`). Go, gopls, golangci-lint, gofumpt, goreleaser, vhs, jq, ripgrep and the runtime tools (git, zellij, lazygit, delta, neovim, glab, gh) are on its PATH.

- CI (`.github/workflows/ci.yml`): `nix flake check` (the whole `go test ./...` in the Nix sandbox), `nix develop -c golangci-lint run ./...` (gofumpt included), `nix build`.
- Fast loop: `go test ./internal/<pkg>` and `go test ./internal/<pkg> -run '^TestName$'`; `go vet ./...`; `gofumpt -w <files>`.
- Generated files:
  - JSON Schemas: `go test ./internal/config -run TestCommittedSchemasAreCurrent -update-schema` (a test fails while `docs/schema/` is stale).
  - Golden TUI frames: `go test ./internal/tui -run TestGolden -update` (also `./internal/overview`); review the diff before committing it.
- Live API tests: `go test -tags live -run Live -v ./internal/forge/github` (needs a token; read the file header first).
- Screenshots for design review: `scripts/verify/capture*.sh` (VHS, output in `.impeccable/review/`); `scripts/verify/` holds the phase evidence scripts.
- Release config: `goreleaser check`; a local build `goreleaser build --snapshot --clean --single-target`.

## Releases

Releases come from `CHANGELOG.md`; nothing is tagged by hand.

- Every user-visible change adds a bullet under `## Unreleased` in its PR: bold lead-in, then what the user sees. A changed default is marked **Breaking**, with how to keep the old behaviour. Fixes start `**Fix:**`.
- To release, a PR renames `## Unreleased` to `## vX.Y.Z` and adds a one-paragraph summary under it (see `## v0.2.0`). Before 1.0: minor for features or breaking changes, patch for fixes only.
- When that PR is merged and CI passes on `main`, `.github/workflows/release.yml` sees a version heading on top whose tag does not exist, tags that commit, and runs goreleaser with the section as the release notes (`scripts/release/changelog.sh version | notes <tag>`). Its own tag push starts no other workflow, which is why goreleaser runs in the same job.
- After a release, the next PR with a user-visible change opens a new `## Unreleased` above it. While `## Unreleased` is on top, merges release nothing.
- A failed release: fix forward in a PR. If the tag was pushed but goreleaser failed, re-run the workflow's job; do not delete or move a published tag.
- The Nix package (`nix profile install github:coredev-uk/ade`) follows `main`, not tags; the tarballs and the version-pinned schema URLs (`raw.githubusercontent.com/coredev-uk/ade/vX.Y.Z/docs/schema/…`) follow tags.

## Repo map and where to look

- `cmd/ade` entry; `internal/cli` one file per command; `internal/app` wiring; `internal/config` user config, manifests, overlays, JSON Schema; `internal/forge/{gitlab,github}` own HTTP clients, `gitlab/fake` the fake GitLab (tier switch, its own clock); `internal/forgesync` sync; `internal/tui` the dashboard; `internal/workspace`, `internal/host`, `internal/editor` tabs, hosts and editors; `internal/doctor` `ade doctor`. Full layout: `docs/PLAN.md` §7.
- `docs/PLAN.md`: the design and the why, with **As built** notes where the build differs; `docs/setup.md` install, tools, CDEs, troubleshooting; `docs/reference.md` every command and key; `docs/manifest.md` the team manifest; `docs/gitlab-api/` the pinned GitLab contract (endpoints, fields, tier gates, sources); `docs/billing/` spend sources.
- `DESIGN.md` (visual system, read by the TUI theme) and `PRODUCT.md` (platform and users): read them, and use `skill://impeccable` and `skill://design-taste-frontend`, for any TUI change.
- `editors/ade-nvim` the Neovim config the flake wraps as `packages.ade-nvim` (a separate output: about 860 MiB against 32 MiB for `ade`).
- `scripts/sandbox` seeds and isolated config for the GitLab and GitHub sandboxes; `scripts/verify` evidence and capture scripts; `scripts/release` the changelog reader.

## Hard rules

Rationale: `docs/PLAN.md` §4.3, §5 and §9.

- Secrets never live in config, manifests, records, logs or audit events: a forge names an environment variable or a command that prints the token. `--debug-http` logs method, masked URL, status and which credential header was sent, never its value or a body.
- Read and write tokens: reads (GET/HEAD, GraphQL queries, clone and fetch) use the read token; writes (other methods, mutations, push) use the write token, resolved on the first write only. With a read token set, agent processes start without the write token's `token_env`: hygiene, not a sandbox, so never describe it as one.
- Every forge write goes through a preview or confirm, has `--dry-run`, and is audited. Destructive local steps (`ade gc`, `ade cleanup`) only remove what is clean, pushed and merged or closed.
- GitLab first: depth and polish go to the GitLab driver; GitHub covers the core flows only. Features are gated on probed capabilities, never on the tier, and degrade per capability.
- The GitLab contract is docs-first: a new endpoint or field is pinned in `docs/gitlab-api/` with its source, served by the fake, and GraphQL is validated against the committed schema. Fixtures are synthetic.
- No telemetry. Local records stay outside repos, `0700`, never synced; AI audit events record destination host and bytes.
- The dashboard never blocks on a sync: dialogs read their one item, sync status lives in the header, never a modal.
- Tests are hermetic: isolated `HOME`/XDG dirs (`t.TempDir()`, `ADE_CONFIG`), no network outside `-tags live`, and time from the fake forge's clock (`srv.Now()`, `srv.Advance`) whenever forge timestamps are involved. A window relative to the wall clock (`--since 7d`) against fake data stops matching a week later.
- Pure Go, `CGO_ENABLED=0`: no cgo dependency (SQLite is modernc).

## Process

- Commits and PR titles are Conventional Commits (`feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `ci`, `build`, `chore`, `revert`; scope optional, e.g. `feat(editor): …`).
- Report only checks you ran, with the command. A known-failing test outside your change is named in the PR, not skipped or deleted.
- Docs are plain and factual: what it does, the exact command or key, the edge case. Update `docs/reference.md` for a command or flag, `docs/setup.md` for setup or tools, `docs/PLAN.md` with an **As built** note when the design moved.
