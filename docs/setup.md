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

**Release binaries.** Each [GitHub release](https://github.com/coredev-uk/ade/releases) has static `ade_<version>_<os>_<arch>.tar.gz` archives (linux and darwin, amd64 and arm64) and `checksums.txt`. Without Nix:

```sh
v=0.1.0; os=linux; arch=amd64   # or darwin / arm64
curl -LO https://github.com/coredev-uk/ade/releases/download/v$v/ade_${v}_${os}_${arch}.tar.gz
tar xzf ade_${v}_${os}_${arch}.tar.gz && install -m 0755 ade ~/.local/bin/ade
ade version
```

The archive is the only thing you need besides the tools in the next section. A release is cut by pushing a `v*` tag; `.github/workflows/release.yml` runs goreleaser. Maintainers can try the build locally with `goreleaser build --snapshot --clean --single-target` (in `nix develop`).

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

AI goes through a profile in the user config. Two common shapes: `omp` (uses your existing subscription) and an OpenAI-compatible **gateway** such as LiteLLM (a team key with a budget, models chosen by alias).

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

#### Through a LiteLLM gateway

A complete profile for a team gateway (synthetic hostnames; the key is read from the environment or a command, never stored):

```toml
[ai]
mode    = "ask"
profile = "gateway"

[ai.profiles.gateway]
driver      = "openai-compat"
base_url    = "https://litellm.example.internal/v1"
key_env     = "LITELLM_KEY"               # or: key_cmd = "pass show litellm/ade"
timeout     = "90s"                       # per request; default 5m
extra_headers = { "x-litellm-tags" = "ade", "x-team" = "platform" }
billing     = { source = "litellm" }      # spend and budget come from the gateway's key
models      = { explain = "team-large", classify = "small-classifier", draft = "team-large", review = "team-large" }

[budget]
call_usd         = 0.10
confirm_over_usd = 0.05
```

`ade init` writes this for you:

```
export LITELLM_KEY=...                    # the virtual key your gateway admin issued
ade init --ai-gateway https://litellm.example.internal/v1 --ai-key-env LITELLM_KEY \
         --ai-model-explain team-large --ai-model-classify small-classifier \
         --forge gitlab --host gitlab.example.com --token-env GITLAB_TOKEN --yes
```

In a terminal, leave the `--ai-model-*` flags out: init lists the gateway's `/v1/models` and asks which model each role uses (`classify` and `draft` default to the explain model). The gateway and key are checked before anything is written; an unreachable gateway only warns when every model was given on the command line. `--ai-profile` renames the profile (default `gateway`); `--ai-key-cmd` takes a command instead of a variable.

`ade ai models` lists what the gateway offers and which role uses each model, and warns about a configured model the gateway does not list:

```
small-classifier  classify
team-large        explain, draft, review
```

Profile checks (`ade config check`): `driver = "openai-compat"` with `billing.source = "litellm"` needs a key and a `base_url` (ending in `/v1`); `extra_headers` may not carry `Authorization` (the key does); roles must be one of the names below.

#### Roles

Each AI feature asks for a *role*; the profile maps the role to a model. A role without a model falls back as shown, so one `explain` model is a complete setup.

| Role | Used for | Falls back to |
| --- | --- | --- |
| `explain` | summaries (`ade ai summarize`), thread summaries, standup, sprint review and parent-issue text | (required) |
| `classify` | `ade ai classify` and the classify dialog: short structured answers (a type and app label as JSON) | `explain` |
| `draft` | reply drafts, MR text | `explain` |
| `review` | AI review passes over an MR diff | `draft`, then `explain` |
| `plan`, `implement` | agent plans and runs (through the profile's agent CLI) | `explain` |

Weight suggestions and the keyword classifier used when AI is off never call a model. Pre-flight estimates, the per-call, daily and sprint caps and the audit trail are identical for every role; a classify call is just a cheaper entry in the ledger (`ade audit --action ai.classify`).

**Picking a classify model.** Classification is a few hundred input tokens in and a one-line JSON object out, so the smallest, cheapest instruction-following model your gateway offers is the right choice (for example a "mini", "flash" or "haiku"-class alias or a small self-hosted model). Look for: reliable JSON output, a low price per million tokens, and low latency (the dialog waits for it). A larger model rarely classifies better here, it only costs more. ADE asks for a `json_schema` response format; if the gateway or model refuses it, ADE retries once as plain text, parses the JSON out of the answer, and remembers not to ask again in that session, so a model without structured-output support still works. Judge a candidate with `ade ai classify <issue> --refresh` against issues you labelled by hand, and compare cost per call in `ade budget`.

### Notifications

Events (CI failures, review requests, agent runs finishing) appear as pop-ups and in the notification centre inside the dashboard, never as desktop or terminal notifications. Everything is also in `ade inbox`. Choose which kinds pop up:

```toml
[notify]
toast = ["ci.failed", "mr.*", "agent.*"]
```

### Commit prefix

If your team prefixes the first commit of an MR with the ticket (e.g. `GL-412 Fix export`), set `conventions.commit_prefix = "GL-{parent}"` in the manifest. ADE applies it to its own first commit and to squash messages and records `ade.commitPrefix` and `ade.target` per worktree. A sample `commit-msg` hook for your own commits is in [manifest.md](manifest.md#commit-prefix).

## Coder / CDE

`ade setup` is the unattended first start: no prompts, safe to run on every
workspace boot, fast when nothing changed. It is built for a Coder
`startup_script`, and equally fine on a laptop.

```sh
ade setup --warm --session
```

What one run does, in order (each step prints one line; `--json` prints the summary on stdout):

1. **Config.** With no user config it creates one like `ade init --yes` from flags or `ADE_SETUP_*` variables (below), or copies a team template with `--config-from <path|https-url>` (validated first). An existing config is validated and never overwritten.
2. **Token.** Every configured forge's token is resolved and checked. An empty or rejected token skips the forge steps, says so, and the run exits 3. Setup never waits for input.
3. **Projects.** `project add` for each configured project (clone, baseline worktrees, `post_worktree` hooks; a repeat only fills gaps), then `update` (fetch and fast-forward), then the first `sync`.
4. **Machine.** Writes the zellij unlock-first preset next to the config, checks git, zellij, lazygit, direnv, nix, omp and the VS Code remote `code` CLI, and prints what is missing with one `nix profile install nixpkgs#...` line. `--warm` pre-builds each repo's baseline dev shell (`nix develop <repo>/main -c true`, or `direnv allow` then `direnv exec` for a `.envrc`: asking for `--warm` is asking to trust those repos' `.envrc` files) so the first work tab opens fast.
5. **Session.** `--session` starts the detached zellij session with the dashboard layout when it is not running (a resurrectable one is resurrected), so the VS Code remote terminal or web tty only attaches. `--session-name` picks another name (default `ade`).
6. **Doctor.** A final `ade doctor` summary, informational: it never changes the exit code.

A lock in the cache directory (`setup.lock`) serialises concurrent runs; the second waits and then finds nothing left to do.

Exit codes: `0` complete, `3` partial (token missing or rejected, or a repo or project failed; the rest still ran), `1` failure (invalid config, nothing could be created).

### Variables

Every flag has an `ADE_SETUP_<FLAG>` variable (`--token-env` is `ADE_SETUP_TOKEN_ENV`); flags win. `ADE_SETUP_PROJECT` takes several manifests separated by whitespace or commas.

| Variable | Meaning |
|---|---|
| `ADE_SETUP_FORGE`, `ADE_SETUP_HOST` | forge kind and host (`gitlab`, `gitlab.example.com`) |
| `ADE_SETUP_TOKEN_ENV` / `ADE_SETUP_TOKEN_CMD` | where the token comes from; the value itself is never written |
| `ADE_SETUP_CA_FILE` | PEM bundle of the forge's internal CA (self-hosted GitLab) |
| `ADE_SETUP_GIT_PROTOCOL`, `ADE_SETUP_ROOT`, `ADE_SETUP_HANDLE` | clone protocol, projects root, forge username (default: asked from the forge) |
| `ADE_SETUP_PROJECT` | project manifests (path, https URL or `git+<url>#<path>`) |
| `ADE_SETUP_AI_GATEWAY`, `ADE_SETUP_AI_KEY_ENV`, `ADE_SETUP_AI_MODEL_EXPLAIN`, `..._CLASSIFY`, ... | the LiteLLM gateway profile (see [Through a LiteLLM gateway](#through-a-litellm-gateway)) |
| `ADE_SETUP_CONFIG_FROM`, `ADE_SETUP_WARM`, `ADE_SETUP_SESSION`, `ADE_SETUP_SESSION_NAME` | the other flags |

### Coder template

Synthetic names throughout. The token comes from a Coder secret that reaches the workspace as an environment variable (`GITLAB_TOKEN` below), so it is never in the template text:

```hcl
resource "coder_script" "ade_setup" {
  agent_id     = coder_agent.main.id
  display_name = "ADE setup"
  run_on_start = true
  script       = <<-EOT
    #!/usr/bin/env bash
    # Exit 3 (partial: token missing) must not fail the workspace start.
    ade setup --warm --session || [ $? -eq 3 ]
  EOT
}

resource "coder_agent" "main" {
  # ...
  env = {
    ADE_SETUP_FORGE       = "gitlab"
    ADE_SETUP_HOST        = "gitlab.example.internal"
    ADE_SETUP_CA_FILE     = "/etc/ssl/certs/example-internal-ca.pem"
    ADE_SETUP_TOKEN_ENV   = "GITLAB_TOKEN"
    ADE_SETUP_GIT_PROTOCOL = "https"
    ADE_SETUP_PROJECT     = "git+https://gitlab.example.internal/acme/tracker.git#.ade.toml"
    ADE_SETUP_AI_GATEWAY  = "https://litellm.example.internal"
    ADE_SETUP_AI_KEY_ENV  = "LITELLM_KEY"
    ADE_SETUP_AI_MODEL_EXPLAIN  = "team-large"
    ADE_SETUP_AI_MODEL_CLASSIFY = "team-small"
    # GITLAB_TOKEN and LITELLM_KEY are not set here: they arrive in the
    # workspace environment from Coder secrets configured outside the template.
  }
}
```

A plain `startup_script` works the same way (`startup_script = "ade setup --warm --session || [ $? -eq 3 ]"`). Put `ade` itself on the image or in a Nix profile; a rejected token is fixed by updating the secret and rerunning `ade setup`.

If your team keeps a config template in a repo, replace the variables with `ADE_SETUP_CONFIG_FROM = "https://gitlab.example.internal/acme/ade-config/-/raw/main/config.toml"` (the template must not contain tokens; it names the variables).

### What belongs in a dotfiles repo instead

`ade setup` creates only ADE's own state. Keep personal choices in your dotfiles repo, which Coder applies before the startup script: shell and git config (`user.name`, `user.email`), your `~/.config/zellij/config.kdl` and neovim config (ADE's unlock-first preset applies only to the VS Code terminal), `direnv` and Nix settings, and `~/.config/ade/config.toml` itself when you prefer to manage it by hand (setup then validates and leaves it alone).

### Attaching

In the VS Code remote terminal or the web tty, run `ade`. When the session from `--session` exists, it attaches to it; otherwise it creates it.

### Self-hosted GitLab

```toml
[forges.work]
kind            = "gitlab"
url             = "https://git.example.internal/gitlab"   # a path prefix is fine
ca_file         = "~/.config/ade/corp-ca.pem"            # PEM bundle of your CA, added to the system pool
ssh_port        = 2222                                    # clone URLs become ssh://git@host:2222/group/project.git
max_concurrency = 4                                       # in-flight API requests (default 4)
```

`ca_file` is trusted for ADE's API calls and handed to git for https clones (`http.<scheme://host>.sslCAInfo`, scoped to the forge's host). Exporting `SSL_CERT_FILE` works too. `ade init --host git.example.internal/gitlab --ca-file … --ssh-port 2222` writes these keys. `ade doctor` prints the GitLab version and edition and warns, per feature, when the release is older than ADE's floors (draft-notes bulk publish 15.9, reviewer verdicts 14.0, work items 16.0, iteration cadences 15.0: ADE's tested floors, not a statement of when GitLab shipped them). Features whose endpoint answers 404 are reported once per sync as "not available on this GitLab" and never fail it. Rate limiting needs no `RateLimit-*` headers: `Retry-After` alone paces requests.

### Headless CDE: VS Code remote terminal and web tty

- **VS Code remote terminal.** `host.editor = "auto"` uses the VS Code remote CLI (found on PATH, else under `~/.vscode-server`); a stale `VSCODE_IPC_HOOK_CLI` after a reconnect is replaced by the newest live socket. `v` and `ade vscode workspace --open` open in the connected window.
- **Web tty** (no VS Code client): `v` prints the workspace path and how to open it from your VS Code window; `o` copies the URL through OSC 52 and says so; the editor falls back to nvim.
- **Keys.** A browser takes Ctrl-w, Ctrl-t, Ctrl-n and similar. Coder's web terminal is detected (`CODER=true`, no ssh, not VS Code) and gets zellij's unlock-first preset; otherwise set `ADE_ZELLIJ_PRESET=unlock-first` (`default` opts out; `ADE_WEB_TTY=1` forces the detection).
- `ade doctor` has a `cde` group: display, VS Code remote CLI, OSC 52 note, nix, direnv.

### Dev shells (direnv / nix)

A worktree with an `.envrc` (and direnv on PATH) runs its editor, omp and shell panes and its `ade task run` tasks through `direnv exec <dir>`; else a `flake.nix` with a devShell through `nix develop <dir> -c`. Per repo in the manifest: `repos.<r>.devshell = "auto" | "direnv" | "nix" | "none"` (default auto). Allow the `.envrc` once with `direnv allow`.

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
- **Rate limits.** GitLab.com allows 100 requests per minute per user, shared by everything using your token (the web UI, `glab`, CI). ADE honours `RateLimit-*` and `Retry-After`, uses at most 4 concurrent requests per host and syncs incrementally, so it waits rather than failing. A sync also leaves 30% of the window to dialogs when the server reports it (GitLab.com does), and reads each project's open merge requests in one GraphQL request instead of four REST calls per MR. If the dashboard seems slow after heavy use elsewhere, wait a minute.
- **See the traffic:** `ade --debug-http <command>` logs one line per request and response to stderr (method, URL, status, timing, which credential header; never the secret or bodies).
- **Token problems:** `ade doctor` shows the user the token authenticates as and its scopes; a `token_env is empty` error means the variable is not set in the shell that started ADE; a `token_cmd` error prints the command's stderr.
- **Stale or odd data:** `ade cache reset` deletes the cache and rebuilds the AI cache and spend ledger from your records. It tells you what is lost (unsubmitted review drafts, unread notifications) and asks first. It is safe: the forge remains the source of truth.
- **Updates arrive a couple of seconds late:** the dashboard and `ade item --watch` follow `cache.db`, the item records and the audit log with fsnotify and re-read the moment one changes. Where fsnotify is unavailable (inotify limit reached, some network filesystems) they poll the files every 2 s instead, which works the same but later; `ade doctor` reports `file watching: polling` then. On Linux, raising `fs.inotify.max_user_watches` and `fs.inotify.max_user_instances` restores instant updates.
- **No tabs appear:** zellij is missing or `host.mode` is `plain`/`vscode`; check `ade doctor`.
- **Where files live** (XDG):

| What | Path |
|---|---|
| User config | `~/.config/ade/config.toml` (`$XDG_CONFIG_HOME/ade/`, or `$ADE_CONFIG`) with `config.local.toml` beside it for private overrides |
| Audit log and item records (durable) | `~/.local/share/ade/` (`$XDG_DATA_HOME/ade/`); `records.dir` can move records |
| Cache (disposable) | `~/.cache/ade/cache.db` (`$XDG_CACHE_HOME/ade/`) |
| Clones and worktrees | `~/ade/<project>/<repo>/` (`ade init --root` or `paths.root`) |

Schemas for editor completion: `ade config schema` or [schema/](schema); put `#:schema <path or URL>` on the first line of a TOML file.
