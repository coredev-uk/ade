# Setting up ADE from zero

This guide takes you from a machine without Nix to a running dashboard on your team's project. All names (`acme`, `example.com`, `jdoe`) are synthetic. Command and key reference: [reference.md](reference.md).

Contents: [1 Nix](#1-install-nix) · [2 ADE](#2-get-ade) · [3 Tools](#3-tools-ade-uses) · [4 Tokens](#4-forge-tokens) · [5 First run](#5-first-run) · [6 Offline demo](#6-try-it-offline) · [7 Troubleshooting](#7-troubleshooting)

## 1. Install Nix

Nix is how ADE and its tools are installed reproducibly on Linux and macOS. Pick one installer:

```sh
# Official installer (multi-user; works on Linux and macOS)
sh <(curl -L https://nixos.org/nix/install) --daemon

# Determinate installer (enables flakes for you, has an uninstaller)
curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix | sh -s -- install
```

Open a new terminal afterwards and check `nix --version`.

**Enable flakes** (not needed with the Determinate installer): add this line to `~/.config/nix/nix.conf` (create the file if needed):

```
experimental-features = nix-command flakes
```

**NixOS:** Nix is already there. Set `nix.settings.experimental-features = [ "nix-command" "flakes" ];` in your configuration and rebuild.

## 2. Get ADE

ADE needs no clone to use:

```sh
nix run github:coredev-uk/ade -- version      # try it once
nix profile install github:coredev-uk/ade     # install `ade` on your PATH
nix profile upgrade --all                  # later: update
```

From a clone (contributors):

```sh
git clone https://github.com/coredev-uk/ade && cd ade
nix build                  # ./result/bin/ade
nix develop                # shell with Go and every tool below
go run ./cmd/ade --help
```

**Release binaries.** `.goreleaser.yaml` builds static `ade_<version>_<os>_<arch>.tar.gz` archives (linux and darwin, amd64 and arm64) for GitHub releases, but no release has been published yet: use Nix for now. Once a tag is released, the archive is the only thing you need besides the tools in the next section. Maintainers can try the build locally with `goreleaser build --snapshot --clean --single-target` (in `nix develop`).

## 3. Tools ADE uses

`ade doctor` lists which of these are found and which are missing. `nix develop` in the ADE repo provides all of them except omp and VS Code.

| Tool | Needed for | Without it | Get it |
|---|---|---|---|
| `git` | clones, worktrees, everything | ADE cannot work | `nix profile install nixpkgs#git` (or your OS) |
| `zellij` | the `ade` session and the per-ticket tabs | `ade` still runs the dashboard in your terminal (or in place when `host.mode = "plain"`), but there are no work tabs; `host.mode = "zellij"` refuses to start instead | `nix profile install nixpkgs#zellij` |
| `lazygit` | the git pane of dev tabs | that pane has nothing to run | `nix profile install nixpkgs#lazygit` |
| `omp` | AI summaries, agent plan/run/review (optional; `ai.mode = "off"` disables AI) | no AI features; the rest is unaffected | see the omp project; ADE only needs the `omp` command on `PATH` |
| `code` (VS Code) | `v` opens a multi-root workspace; `host.editor = "code"` | use nvim or `$EDITOR` instead | install VS Code and run "Shell Command: Install 'code' command in PATH"; check with `ade doctor --vscode`, which also prints recommended `settings.json` entries so VS Code does not swallow ADE's keys |
| Neovim | default editor | falls back to `$EDITOR` | `nix profile install nixpkgs#neovim`, or ADE's preconfigured one: `nix run github:coredev-uk/ade#ade-nvim` (a pinned, isolated config under `NVIM_APPNAME=ade-nvim`; set `host.editor = "ade-nvim"`). It ships the snacks.nvim picker: `<space><space>` smart find, `<space>ff` files, `<space>fg` grep, `<space>fb` buffers, `<space>fr` recent, `<space>e` explorer, `<space>gg` lazygit, `<space>ai` ADE items; the full list is in PLAN.md §6.7 |
| `delta` | coloured diffs in review tabs (optional) | plain `git diff` | `nix profile install nixpkgs#delta` |
| `gh` / `glab` | optional; `gh auth token` can supply the GitHub token | none | `nix profile install nixpkgs#gh nixpkgs#glab` |

The dev shell (`nix develop`) contains git, zellij, lazygit, delta, neovim, glab and gh plus the Go toolchain, golangci-lint, vhs and friends. `nix develop .#gateway` adds a local LiteLLM proxy for gateway testing; most people never need it.

## 4. Forge tokens

ADE reads a token from your environment or from a command. **It is never written to the config file, the audit log or the cache.**

### GitLab

Create a personal access token: *User settings → Access tokens* (`https://gitlab.example.com/-/user_settings/personal_access_tokens`). Scopes:

| Scope | Why |
|---|---|
| `api` | read and write issues, merge requests, notes, labels, discussions; creating branches and MRs; the token-scope check in `ade doctor` |
| `read_repository` | clone and fetch over HTTPS |
| `write_repository` | push branches over HTTPS |

Read-only use (browse, no writes) works with `read_api` + `read_user`; `ade doctor` then reports the token as read-only. With `git_protocol = "ssh"` (the default) git uses your SSH key, so the repository scopes only matter for HTTPS.

Give ADE the token one of two ways, in `~/.config/ade/config.toml` (`ade init` writes this for you):

```toml
[forges.work]
kind         = "gitlab"
url          = "https://gitlab.example.com"
token_env    = "GITLAB_TOKEN"            # name of an environment variable, or:
# token_cmd  = "pass show ade/gitlab"    # any command that prints the token: pass, op read, security find-generic-password, ...
git_protocol = "ssh"                     # or "https"
```

`token_cmd` is the better choice: the secret stays in your password manager and nothing has to live in your shell profile.

### GitHub

```toml
[forges.personal]
kind      = "github"
token_cmd = "gh auth token"
```

Run `gh auth login` first. If you use a PAT instead, give it the `repo` scope.

## 5. First run

### Team lead: create the shared manifest (once per team)

A manifest (`.ade.toml`) describes the team's repos, labels, stages, sprints and conventions; see [manifest.md](manifest.md). Generate it from your GitLab group (or GitHub owner):

```sh
export GITLAB_TOKEN=...
ade init --yes --forge gitlab --host gitlab.example.com --token-env GITLAB_TOKEN   # no --project: only writes the forge entry
ade project bootstrap --forge gitlab --group acme --dry-run   # prints the manifest, writes nothing
ade project bootstrap --forge gitlab --group acme             # writes ./.ade.toml; review and edit it
ade project bootstrap --forge gitlab --group acme --publish   # opens an MR adding .ade.toml to the tracker repo
```

Useful flags: `--tracker <repo>` (default: detected), `--name`, `--match <glob>` to limit repos, `--out <file>` (`-` prints), `--force` to overwrite, `--yes` never to prompt. `--forge` names an entry of your `[forges]`, and teammates must use the same name. Merge the MR, then tell the team the manifest address: `git+https://gitlab.example.com/acme/tracker.git#.ade.toml`.

### Teammate: onboard

Interactive (asks for each answer; verifies the token before writing anything):

```sh
ade init
```

Scripted:

```sh
export GITLAB_TOKEN=...
ade init --yes --forge gitlab --host gitlab.example.com --token-env GITLAB_TOKEN \
    --git-protocol ssh --root ~/ade \
    --project 'git+https://gitlab.example.com/acme/tracker.git#.ade.toml'
# or a token command:  --token-cmd 'pass show ade/gitlab'
```

It writes the config, clones every repo as `~/ade/<project>/<repo>/{.bare,main}`, runs the first sync and `ade doctor`. An existing config is kept unless you pass `--force` (then a `config.toml.bak-<timestamp>` stays beside it). `--handle <username>` sets your forge username instead of asking the forge.

### Check, then open

```sh
ade doctor                # tools, paths, config, forge token and scopes; non-zero exit on errors
ade doctor --probe        # also read-only checks of the API shapes ADE depends on
ade                       # attach to (or create) the zellij session "ade" with the dashboard
```

Already inside zellij, `ade` runs the dashboard in your current pane. `ade dash` always runs it in the current terminal.

### The daily loop

1. **Home** shows your issues and MRs by stage. Move with the arrow keys or `j`/`k`; `?` lists every key; `/` searches.
2. **`s` start work** on an item: pick repos, branch/MR mode, child issue behaviour. ADE creates the branch, a worktree and a zellij tab.
3. **The tab** has your main pane (editor, or an `omp` agent, depending on `host.layout`), the item's live overview, lazygit, a shell, and one pane per service. Service panes wait: press **Enter** in a pane to start that service; Ctrl-C stops it. `ade ws restart <node>` restarts one with its dependents.
4. **`v`** opens a multi-root VS Code workspace for the item (all repos of the project).
5. **Enter** on an MR opens the MR page: threads, diff, CI; reply, resolve, approve, mark ready, merge. `ade review <group/project!N>` opens someone else's MR in a review tab; collected line comments go out as one review batch.
6. **Clean up** with `ade gc`, which removes worktrees of merged or closed MRs when they are clean and pushed.

### AI and budgets (optional)

AI goes through a profile in the user config; `omp` uses your existing subscription:

```toml
[ai]
mode    = "ask"                 # off | ask | auto
profile = "sub"

[ai.profiles.sub]
driver  = "omp"
billing = { source = "omp", weekly_soft_usd = 10 }

[budget]
daily_usd     = 2.00
call_usd      = 0.50            # hard cap per call, also passed to omp
agent_run_usd = 1.50            # cap for one agent run
```

Every AI call is shown with its cost before it runs when it exceeds `confirm_over_usd`, and is audited (`ade audit --action 'ai.*'`). Details: PLAN.md §4.3 and [billing/README.md](billing/README.md).

### Notifications

Events (CI failures, review requests, agent runs finishing) appear as pop-ups and in the notification centre inside the dashboard, never as desktop or terminal notifications. Everything is also in `ade inbox`. Choose which kinds pop up:

```toml
[notify]
toast = ["ci.failed", "mr.*", "agent.*"]
```

### Commit prefix

If your team prefixes commits with the ticket (e.g. `GL-412 Fix export`), set `conventions.commit_prefix = "GL-{parent}"` in the manifest. ADE applies it to its own commits and merges and records `ade.commitPrefix` per worktree. A sample `commit-msg` hook for your own commits is in [manifest.md](manifest.md#commit-prefix).

## 6. Try it offline

No account, token or network needed. A fake GitLab with real local git remotes, a stub AI and a temp state directory (deleted on exit):

```sh
git clone https://github.com/coredev-uk/ade && cd ade
zellij                                          # optional but recommended: work tabs need zellij
nix develop -c bash scripts/demo.sh            # run it inside the zellij session to get tabs
```

It never touches your real config. `--record` additionally writes `demo.gif`.

## 7. Troubleshooting

- **First step for anything:** `ade doctor`. Each line says what is wrong and the fix; `--vscode` and `--probe` add the VS Code and API-shape checks.
- **Rate limits.** GitLab.com allows 100 requests per minute per user, shared by everything using your token (the web UI, `glab`, CI). ADE honours `RateLimit-*` and `Retry-After`, uses at most 4 concurrent requests per host and syncs incrementally, so it waits rather than failing. If the dashboard seems slow after heavy use elsewhere, wait a minute.
- **See the traffic:** `ade --debug-http <command>` logs one line per request and response to stderr (method, URL, status, timing, which credential header; never the secret or bodies).
- **Token problems:** `ade doctor` shows the user the token authenticates as and its scopes; a `token_env is empty` error means the variable is not set in the shell that started ADE; a `token_cmd` error prints the command's stderr.
- **Stale or odd data:** `ade cache reset` deletes the cache and rebuilds the AI cache and spend ledger from your records. It tells you what is lost (unsubmitted review drafts, unread notifications) and asks first. It is safe: the forge remains the source of truth.
- **No tabs appear:** zellij is missing or `host.mode` is `plain`/`vscode`; check `ade doctor`.
- **Where files live** (XDG):

| What | Path |
|---|---|
| User config | `~/.config/ade/config.toml` (`$XDG_CONFIG_HOME/ade/`, or `$ADE_CONFIG`) with `config.local.toml` beside it for private overrides |
| Audit log and item records (durable) | `~/.local/share/ade/` (`$XDG_DATA_HOME/ade/`); `records.dir` can move records |
| Cache (disposable) | `~/.cache/ade/cache.db` (`$XDG_CACHE_HOME/ade/`) |
| Clones and worktrees | `~/ade/<project>/<repo>/` (`ade init --root` or `paths.root`) |

Schemas for editor completion: `ade config schema` or [schema/](schema); put `#:schema <path or URL>` on the first line of a TOML file.
