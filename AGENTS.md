# ADE: agent instructions

ADE is an agentic development environment for the terminal: a Bubble Tea dashboard and CLI over GitLab (first) and GitHub, with zellij work tabs, worktrees and agent runs. Go module `github.com/coredev-uk/ade`, one static binary (`CGO_ENABLED=0`). These rules are for every agent and harness; they apply to people too.

## Workflow

1. Work on a branch of its own, never on `main`: `<type>/<slug>` from `origin/main`, where `<type>` is the Conventional Commit type (`feat/code-server-session`). A worktree per change keeps parallel work apart.
2. Implement, with the docs and tests that describe the change in the same PR. Do not edit `CHANGELOG.md`: release-please writes it from the PR title (Releases below).
3. Run the checks (Commands below) before you push: `nix flake check`, `golangci-lint run ./...` and `nix build` are what CI runs.
4. Push and open the PR with a Conventional Commit title. It becomes the changelog line, so write what the user sees (`fix(tui): notes written in the editor show in the detail pane`), not what the code does. The description says what changed and why, the commands you ran with their result, and anything left.
5. Merging is the maintainer's call (@coredev-uk). Merging release-please's release PR releases (Releases below).

## Boundaries

- Always: one change per branch and PR; run the checks before a PR; update the docs with the code; regenerate `docs/schema/` when a config or manifest struct changes; run anything that talks to a forge against the sandbox (`. scripts/sandbox/env.sh`, which needs sandbox tokens in `.env`) or, with no tokens, against the fake GitLab (`nix develop -c bash scripts/demo.sh`: a seeded dashboard, isolated config and data, nothing leaves the machine), never your own config.
- Ask first: cutting a release (merging the release PR); changing a default users already rely on (a breaking change: `feat!:` with a `BREAKING CHANGE:` footer); bumping Go, nixpkgs (`flake.lock`) or a Go dependency; editing `.github/workflows/`; anything that writes to a real forge.
- Never: commit or push to `main`; push a `v*` tag by hand to work around the release workflow; print, log or commit tokens, `.env` or `*.local.toml`; commit captured real forge traffic, hosts or project names (fixtures are synthetic); hand-edit `docs/schema/*.json` or golden files instead of regenerating them; edit `CHANGELOG.md` or `.release-please-manifest.json` by hand.

## Commands

Tools come from the flake: `nix develop -c <cmd>`, or let direnv load it (`.envrc` is `use flake`). Go, gopls, golangci-lint, gofumpt, goreleaser, vhs, jq, ripgrep and the runtime tools (git, zellij, lazygit, delta, neovim, glab, gh) are on its PATH.

- CI (`.github/workflows/ci.yml`): `nix flake check` (the whole `go test ./...` in the Nix sandbox), `nix develop -c golangci-lint run ./...` (gofumpt included), `nix build`.
- Fast loop: `go test ./internal/<pkg>` and `go test ./internal/<pkg> -run '^TestName$'`; `go vet ./...`; `gofumpt -w <files>`.
- Generated files:
  - JSON Schemas: `go test ./internal/config -run TestCommittedSchemasAreCurrent -update-schema` (a test fails while `docs/schema/` is stale).
  - Golden TUI frames: `go test ./internal/tui -run TestGolden -update` (also `./internal/overview`); review the diff before committing it.
- Run ADE without forge tokens: `nix develop -c bash scripts/demo.sh` (the real dashboard against a seeded fake GitLab; add `--record` for the VHS tour). The sandbox (`. scripts/sandbox/env.sh`) talks to real forges and needs `GITLAB_PAT` in `.env` and a `gh` login.
- Live API tests: `go test -tags live -run Live -v ./internal/forge/github` (needs a token; read the file header first).
- Screenshots for design review: `scripts/verify/capture*.sh` (VHS, output in `.impeccable/review/`); `scripts/verify/` holds the phase evidence scripts.
- Release config: `goreleaser check`; a local build `goreleaser build --snapshot --clean --single-target`.

## Releases

Releases come from [release-please](https://github.com/googleapis/release-please) (`release-please-config.json`, `.release-please-manifest.json`); nothing is tagged by hand.

- The changelog is the Conventional Commit titles merged since the last tag: `feat` under Features, `fix` under Fixes, `perf` and `revert` too; `docs`, `refactor`, `test`, `build`, `ci` and `chore` are left out. Squash-merge so a PR is one line; with a merge commit every commit of the PR counts. To change or add lines after merging, put a `BEGIN_COMMIT_OVERRIDE` … `END_COMMIT_OVERRIDE` block of Conventional Commit messages in the merged PR's description; the release PR picks it up on the next merge.
- A breaking change (a changed default included) is `feat!:` or `fix!:` with a `BREAKING CHANGE:` footer saying how to keep the old behaviour. Before 1.0 it bumps the minor version, as `feat` does; `fix` alone bumps the patch.
- After every merge to main whose CI passes, `.github/workflows/release.yml` runs release-please, which opens or updates one release PR (`chore: release X.Y.Z`) with the next version and its `CHANGELOG.md` section. Merging it, once CI passes on main, tags the merge commit, creates the GitHub release with that section as its notes, and runs goreleaser in the same workflow to upload the archives (a tag or release made with `GITHUB_TOKEN` starts no other workflow).
- A failed release: fix forward in a PR. If the release exists but goreleaser failed, run the Release workflow by hand with the tag (Actions → Release → Run workflow); do not delete or move a published tag.
- The Nix package (`nix profile install github:coredev-uk/ade`) follows `main`, not tags; the tarballs and the version-pinned schema URLs (`raw.githubusercontent.com/coredev-uk/ade/vX.Y.Z/docs/schema/…`) follow tags.

## Repo map and where to look

- `cmd/ade` entry; `internal/cli` one file per command; `internal/app` wiring; `internal/config` user config, manifests, overlays, JSON Schema; `internal/forge/{gitlab,github}` own HTTP clients, `gitlab/fake` the fake GitLab (tier switch, its own clock); `internal/forgesync` sync; `internal/tui` the dashboard; `internal/workspace`, `internal/host`, `internal/editor` tabs, hosts and editors; `internal/doctor` `ade doctor`. Full layout: `docs/PLAN.md` §7.
- `docs/PLAN.md`: the design and the why, with **As built** notes where the build differs; `docs/setup.md` install, tools, CDEs, troubleshooting; `docs/reference.md` every command and key; `docs/manifest.md` the team manifest; `docs/gitlab-api/` the pinned GitLab contract (endpoints, fields, tier gates, sources); `docs/billing/` spend sources.
- `DESIGN.md` (visual system, read by the TUI theme) and `PRODUCT.md` (platform and users): read them, and use `skill://impeccable` and `skill://design-taste-frontend`, for any TUI change.
- `editors/ade-nvim` the Neovim config the flake wraps as `packages.ade-nvim` (a separate output: about 860 MiB against 32 MiB for `ade`).
- `scripts/sandbox` seeds and isolated config for the GitLab and GitHub sandboxes; `scripts/verify` evidence and capture scripts.

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
