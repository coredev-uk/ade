# Shared project manifest

A team describes how it works once, in a **manifest**, and every teammate points ADE at it. The manifest holds
conventions and structure only: no secrets, no per-person paths, nothing that differs between machines.
Each person's own settings (forge host, token source, projects root, handle) stay in their
[user config](../README.md#configuration), which `ade init` writes for them.

| | Manifest (`.ade.toml`) | User config (`~/.config/ade/config.toml`) |
|---|---|---|
| Shared | yes, committed in git | never |
| Holds | tracker, repos, labels, iterations, conventions, tasks, workspaces | identity, forges and token *sources*, root, AI, budgets |
| Edited by | the team, through review | each person |

All examples below are synthetic (`acme`, `example.com`).

## Where it lives

Commit it to the **tracker repository** (the project that holds the issues the team plans in):

```
acme/tracker
└── .ade.toml
```

Teammates register it by git source, which ADE keeps current on every `ade update` (shallow fetch of the file):

```sh
ade project add 'git+https://gitlab.example.com/acme/tracker.git#.ade.toml'
```

Other sources work too: a local path, or a plain `https://` URL to the raw file. A manifest can be moved
later without touching anyone's clone; they re-add the new source.

For a new teammate the whole setup is one command; it reads the forge name from the manifest:

```sh
export GITLAB_TOKEN=...        # a personal access token with read_api, api (to write), read_repository
ade init --yes --forge gitlab --host gitlab.example.com --token-env GITLAB_TOKEN \
    --project 'git+https://gitlab.example.com/acme/tracker.git#.ade.toml'
```

## The `forge` key is a local name

`forge = "work"` does not say which host the team uses; it names an entry under `[forges.work]` in each
person's user config. That keeps the manifest portable (one person's `work` can be a self-hosted GitLab over
HTTPS, another's the same host over SSH) but means **everyone must use the same name**. `ade init` takes the
name from the manifest, so a teammate never types it. If you must add a second forge by hand, name it the same
as the manifest says.

## Minimal manifest

```toml
name  = "acme"                 # path-safe: letters, digits, . _ -
forge = "work"                 # key into the user config's [forges]

[tracker]
project = "acme/tracker"       # where the planning issues live

[repos.api]
path = "acme/api"
app  = ["api"]                 # issues labelled app::api route here

[repos.web]
path     = "acme/web"
app      = ["web"]
requires = ["api"]             # checkout dependency: web work also gets an api tree
```

## Conventions teams usually set

### Labels

Three independent facets, each a pattern containing `{value}`. ADE derives the rest from the forge.

```toml
[labels.type]
pattern = "type::{value}"      # story, bug, ... shown on every row and used for filters

[labels.app]
pattern = "app::{value}"       # routes a tracker issue to the repos whose `app` lists the value

[labels.workflow]
pattern = "workflow::{value}"  # the board column
stage   = { started = "in progress", draft = "in progress", review = "in review", merged = "done" }
sync    = "auto"               # auto | ask | off (unset means ask)
done    = "done"               # optional, see below
```

- `type` and `app` missing on an issue puts it in the **needs triage** filter.
- `workflow.stage` maps the stage ADE *derives* from forge facts (child issue, branch, MR, CI, approvals) to your
  board column names. `sync` decides who moves the label. Unset means `ask`, so a manifest that never mentions
  it does not change a shared board on its own. Every move is recorded in the audit log.
  - `auto`: ADE moves the label when the derived stage changes (and removes the old one), on every sync and in
    `ade start`. Pass `--no-move-label` to `ade start` to skip the move once.
  - `ask`: ADE moves the label only when you confirm. `ade start` moves it with `--move-label`, and the start
    dialog has a checkbox (key `l`). Without that, `ade start` and `ade start --dry-run` print the label they
    would set. A background sync never moves the label: it lists each stage change as a warning in `ade sync` and
    in the inbox, naming the label and the item. Set the label on the board, or set `sync = "auto"`.
  - `off`: ADE never moves the label; the board column only shows as drift.
  `ade start --dry-run` lists the label move as a forge write when it would run. When it would not, a note
  says why: `ask` without `--move-label`, `off`, `--no-move-label`, or a label already further along. Browse mode
  and a manifest that maps no `started` stage never touch the label and print no note.
- `workflow.done`: some teams leave sprint work open until the sprint review and mark it finished with a label.
  With `done = "done"`, an open tracker issue carrying that value (and no open MR) counts as done and is never
  nagged to close. Leave it out if your team closes issues when work finishes.

On GitHub the same patterns apply to labels (`type:{value}` is a common spelling since `::` scoped labels are a
GitLab idiom); nothing about the pattern is GitLab specific.

### Iterations (sprints)

```toml
[tracker]
iterations = { source = "gitlab-iterations", cadence = "Delivery" }
```

| `source` | Use when | Extra keys |
|---|---|---|
| `auto` | GitLab iterations, falling back to milestones when the group has none (Free tier) | |
| `gitlab-iterations` | GitLab Premium iterations | `cadence` (title filter when the group has several) |
| `milestones` | GitLab Free, or teams that use milestones as sprints | |
| `labels` | sprints are labels such as `Sprint::42` | `pattern`, `start` (day 1 of sprint 1), `length` (`2w`, `14d`) |
| `github-project` | GitHub Projects (v2) iteration field | |
| `cadence` | GitLab iterations of one named cadence | `cadence` |

`overlap` (default `1`) is how many working days around a boundary also show the adjacent sprint. Working days
come from each person's `user.workdays` and `user.holidays`.

A tracker in a personal (user) namespace has no group, so no iterations, cadences or epics: sprints are the tracker
project's milestones. `auto` and `milestones` read them; `gitlab-iterations` and `cadence` fail with an error saying
iterations need a group. Weight and approval rules are read from the tracker project.

```toml
[tracker]
iterations = { source = "labels", pattern = "Sprint::{n}", start = "2026-01-05", length = "2w", overlap = 1 }
```

`ade issue move <ref>` (or `>` in the dashboard) carries an issue into the next sprint, explicitly and audited as
`forge.issue.sprint`: it sets the iteration for `gitlab-iterations`/`cadence`, the milestone for `milestones`, and for
`labels` removes the old sprint label and adds the new one. ADE creates no sprint: without a next one the command says
what to create (the next milestone, the cadence's next iteration, or the next `pattern` label). `github-project`
iterations cannot be written. The project lead closes issues; ADE never bulk-closes.

### Weight (story points)

```toml
[tracker]
weight = { source = "auto" }
```

| `source` | Meaning |
|---|---|
| `auto` | the forge's weight field when the issue has one, else the `weight::{value}` label |
| `gitlab` | the issue `weight` field (GitLab Premium) |
| `label` | `pattern = "weight::{value}"`; the largest matching label wins |
| `github-field` | `field = "Estimate"`, a number field on the GitHub project |
| `none` | no weights |

An issue without a weight is **unweighted**, never zero, and appears in the needs-triage filter.

ADE never re-weights silently. `ade issue weight <ref> <n|none>` (or `=` in the dashboard) is the explicit, audited way: it writes the weight field where `source` is `gitlab`, or `auto` and the forge licenses it, and otherwise swaps the `pattern` label, removing every old weight label explicitly. `github-field` and `none` cannot be written. The child issue start work made follows unless `--no-child`. `ade ai classify` suggests a weight from the closed issues of the same type and app that share a title word, with no model call.

### Starting work

```toml
[conventions]
branch      = "{child_iid}-{slug}"       # e.g. 412-rate-limit-export
child_title = "{parent_title}"
child_body  = "templates/child.md"       # relative to the manifest
link        = ["relates_to", "description"]
child_issue = "auto"                      # auto | always | never
mr          = { create = "ask", template = "Default", draft = true, closes = "child", mention = "parent" }
```

- `child_issue`: `auto` creates a child issue in the code repo when the tracker is a different project, and none
  when the issue already lives in the code repo. `always` / `never` force it. The child is linked back to the
  tracker issue (`relates_to` link on GitLab, native sub-issue on GitHub) and carries a `Parent:` reference in
  its description. Reading accepts any of these, so older data still resolves.
  - On GitLab, `ade start` also places the child under the tracker issue in GitLab's work item hierarchy
    (GraphQL `workItemUpdate`), where the instance has one. GitLab nests only some pairs of types (an issue
    under an epic, a task under an issue), so it may refuse an issue under an issue. A refusal is a warning,
    not a failure: the `relates_to` link and the `Parent:` line stay, and ADE remembers a refusal of the pair
    of types for a week so later starts do not ask again. Any other error (a permission problem, say) is a
    warning for that start only and is not remembered.
  - To decide which tracker issue a code-repo issue belongs to, ADE asks in this order: the hierarchy parent
    (GraphQL, skipped silently where the instance or forge has none), the `Parent:` line, then a `relates_to`
    link from the child to a tracker issue. The hierarchy is read for every code-repo issue ADE lists, so a
    closed child or a teammate's keeps its place under the tracker issue; links are read only for your open
    issues. If the hierarchy read fails for another reason, ADE keeps the parents of the last sync and warns.
    The tracker issue is fetched even if it is not assigned to you and is outside the sprint window, and the
    child shows under it as one My work entry; the entry counts as yours while an open child is. A tracker
    issue that is closed is not listed, so its open child of yours is its own entry. A code-repo issue with no
    parent from any of these is its own entry.
- `mr.create`: `ask` (default), `always`, `never`. `mr.draft` opens the MR as a draft; leave it out (or set `true`) for a draft MR, `false` to open it ready for review. `mr.closes = "child"`
  puts `Closes #<child>` in the MR body; `mr.mention = "parent"` references the tracker issue.
- `mr.template`: the template name in the repo's MR template directory (GitLab
  `.gitlab/merge_request_templates/<name>.md`, GitHub `.github/pull_request_template.md`). ADE shows the
  rendered body, and lists any quick actions in it (`/assign`, `/label`, ...) so they can be kept or stripped
  before anything is sent.
- `commit_prefix`: a prefix for the first commit of the MR (and a squash merge message), e.g.
  `"GL-{parent}"`. See [Commit prefix](#commit-prefix).

### Commit prefix

```toml
[conventions]
commit_prefix = "GL-{parent}"   # GL-412 Rate-limit the export endpoint
```

Placeholders: `{parent}` is the tracker issue's iid, `{child}` the child issue's iid in the code repo (the
parent's when the item has none) and `{project}` the ADE project name. Unknown placeholders, a multi-line value
or leading/trailing whitespace are errors (`conventions.commit_prefix`); ADE adds the separating space itself.
Without the key nothing changes.

Only the **first commit of the MR** carries the prefix, as the ticket reference needs to appear once per MR.

- **Agent commits** (`ade agent push`, the TUI push) get the prefix only when the branch has no commits ahead of
  the MR target yet (`ade.target`; with no target on record the prefix is applied). A message that already starts
  with it is never prefixed twice.
- **Merges** (`ade mr merge`, the MR page `m`): a merge commit is not prefixed, ADE sends GitLab's/GitHub's own
  message. A **squash** message keeps the prefix (`squash_commit_message` = the prefixed MR title): the squash
  commit replaces the MR's commits, so it is the first commit. `--dry-run` and the merge dialog show it. The
  owning work item is found in the sync cache, so an MR ADE has not synced gets the forge's defaults.
- **GitHub** start work's empty commit is the branch's first commit: `GL-412 Start work: <title>`.

Your own commits are yours: start work and opening a workspace for an item's worktree record two per-worktree
git settings (`extensions.worktreeConfig` is switched on for the repository; in a bare clone `core.bare` moves
into the bare repository's own `config.worktree`, which git requires):

```
ade.parent        = 412          # the tracker issue's iid
ade.commitPrefix  = GL-412       # absent when commit_prefix is not set (a stale value is removed)
ade.target        = main         # the MR's target branch; absent when not known yet
```

A `commit-msg` hook (a global `core.hooksPath` one works for every worktree) can use them to prefix the first
commit you type; `ade.target` is the MR's target branch (written when ADE knows it), so the hook prefixes only
while the branch has no commits ahead of it:

```sh
#!/bin/sh
# commit-msg: prepend the ADE commit prefix to the MR's first commit only.
prefix=$(git config --get ade.commitPrefix) || exit 0
[ -n "$prefix" ] || exit 0
target=$(git config --get ade.target) || exit 0
# any commit already ahead of the target means this is not the first one
ahead=$(git rev-list --count "$target..HEAD" 2>/dev/null || git rev-list --count "origin/$target..HEAD" 2>/dev/null) || exit 0
[ "$ahead" = 0 ] || exit 0
first=$(head -n1 "$1")
case "$first" in
  "$prefix"|"$prefix "*|"$prefix:"*) exit 0 ;;                 # already prefixed
  Merge\ *|fixup!\ *|squash!\ *|amend!\ *|Revert\ *) exit 0 ;;  # merges, autosquash, reverts
esac
printf '%s %s\n' "$prefix" "$(cat "$1")" > "$1.new" && mv "$1.new" "$1"
```

ADE sets `ADE_AGENT_RUN=1` in the environment of every agent it starts (`ade agent plan|run|address`, and the `omp`
pane of the `agent` layout), and not in its own: the push of `ade agent push` is unmarked. A global `pre-push` hook can
use it to refuse an agent's own `git push`:

```sh
#!/bin/sh
# pre-push: refuse pushes made from inside an ADE agent run.
if [ -n "$ADE_AGENT_RUN" ]; then
  echo "pre-push: refused: an agent run may not push (review, then ade agent push)" >&2
  exit 1
fi
```

This is a guardrail, not a sandbox. The agent runs as you and still has the forge token in its environment (only the
read token, when the forge sets one), so it can unset the variable or push some other way. It stops the mistake, not a
determined agent.

## Repos, hooks and run graph

Each repo is cloned to `<root>/<project>/<repo>/`, as a `.bare` clone plus a **baseline worktree named after the
repo's default branch** (`main/`, `develop/`; a `/` in the branch name becomes `-`, so `release/1` is `release-1/`).
Feature worktrees sit beside it, one per work-item branch. `default_branch` on a repo sets that branch; left out, ADE
uses the branch the remote's `HEAD` points at.

```toml
[repos.api]
path           = "acme/api"
default_branch = "develop"               # baseline tree is api/develop/; omit to use the remote's HEAD

[repos.web.hooks]
post_clone    = ["npm ci"]               # once, in the baseline tree after `ade project add`
post_worktree = ["npm ci"]               # every new worktree
install       = ["npm ci"]               # every worktree a workspace uses (services tab, ade ws install)

[repos.api.tasks.dev]
cmd   = "make run"
port  = "auto"
ready = { http = "http://localhost:{port}/healthz", timeout = "60s" }

[repos.web.tasks.dev]
cmd      = "npm run dev -- --port {port}"
port     = "auto"
requires = ["api.dev"]                   # runtime dependency: api is ready first
env      = { API_URL = "http://localhost:{api.dev.port}" }

[groups]
dev = ["web.dev"]

[workspaces]
default = { layout = "code", run = "dev" }
```

`requires` on a repo is a *checkout* dependency; `requires` on a task is a *runtime* dependency. Both graphs are
validated together. Hooks run inside the tree and failures raise notifications. The full reference, including
`services`, `kind = "oneshot"`, ports and templates, is [PLAN.md §2](PLAN.md#2-configuration-core-function-4) and
[§2.1](PLAN.md#21-repo-and-task-dependencies).

`workspaces.<name>.layout` is the main pane of the tab ADE opens for a worktree: `code` (your editor), `agent` (omp
started with an ADE-generated implement skill that carries the ticket, MR, conventions and your notes, and stops for
review without pushing) or `manual` (a shell). Every layout also shows the item's live overview, lazygit, a shell and
the workspace's own tasks of the `run` group (none starts on its own: a pane waits for Enter; Ctrl-C in a running pane stops it, Enter starts it again). The services it requires start in the background services tab described below.
Each person can override the default with `host.layout` in their own config; `ade start --layout` and `W` on Home
choose per tab. A service runs from the work item's own worktree when it has one in that repo, else from the shared
baseline (default-branch) tree, so `{api.dev.port}` in web's `API_URL` follows the item's api when it has one. See
[PLAN.md §6.6](PLAN.md#66-agents-and-workspaces-core-function-7).

### Install hooks and background services

`hooks.install` is a list of shell commands that make a worktree runnable (`npm ci`, `make deps`). ADE runs them in
each worktree of that repo that a workspace uses (the one it opened, and the trees its required repos and services run
from), inside the repo's dev shell, one after the other; the trees of required repos and services come first, the
workspace's own tree last. With `services = "background"` they run when the workspace first opens, in the first pane
of the workspace's services tab, before any service starts. With `services = "manual"` nothing runs them on open: use
`ade ws up` or `ade ws install <tree> [--force]`. A review tab runs no install.

ADE remembers a success per worktree, keyed by a hash of the commands, of the content of the lockfiles at the
worktree's root (`package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`, `bun.lockb`, `go.sum`, `Cargo.lock`,
`poetry.lock`, `uv.lock`, `flake.lock`, when present) and of the worktree's creation. The commands run again only
when one of them changes (a pull that updates a lockfile installs again) or you pass `--force`. The dev shell is not
part of the key. A failing command stops that worktree's commands and is reported; ADE keeps no success marker but
records the failure, so every pane waiting for that install stops at once with the reason instead of waiting. The
next `ade ws install` clears the record. Each command is audited as `git.hook` with `hook=install`. `post_clone` and
`post_worktree` are unchanged.

Install commands run with ADE's environment (including any forge token you exported) and inside the worktree's dev
shell (`direnv exec`, `nix develop`), so the branch's own `.envrc` or flake runs when the workspace opens. They come
from the project manifest and have the same trust as `post_worktree` and task `cmd`: ADE asks nothing when the
manifest changes, and the audit log keeps the command text. Only open workspaces of projects you trust.

The first time ADE opens a workspace tab it also creates an unfocused tab named `<tab> · services` (you stay on the
workspace tab). It holds the install pane and one pane per service of the `run` group (requirements running from other
trees and `services.*`), started in dependency order with no "Waiting to run" prompt. Each pane waits for the install of
its worktrees and for every service it requires to be ready. A service some node requires, with a `port` and no
`ready`, counts as ready once its TCP port accepts connections, in its own pane as well as for its dependents; an
explicit `ready` always wins. A `services.*` instance another
workspace already runs is reused: the pane reports `already running (pid N)`, holds it and shows its log. The
workspace's own tasks stay in the workspace tab's strip and start on Enter.

A waiting pane never hangs silently: it prints `still waiting for <node> (<probe>) <time>` every 30 seconds, stops
with the reason when the pane that starts what it waits for failed (crashed, or its `ready` timed out), and gives up
after `--wait-timeout` (default 10 minutes, `ade task run`) with a non-zero exit. A stopped pane shows the reason;
press Enter to run it again.

```toml
[workspaces]
default = { layout = "code", run = "dev", services = "background" }   # or "manual"
```

`services = "manual"` skips the services tab: services and the `services` (start all) pane stay in the strip, each
waiting for Enter, and `ade ws up --no-zellij` still runs the install hooks first. Strip tasks wait for the install
too (`--after-install`): run `ade ws install` or `ade ws up` first, or the pane says so and waits.

### Dev shell per repo

```toml
[repos.api]
path     = "acme/api"
devshell = "auto"     # auto | direnv | nix | none
```

`repos.<r>.devshell` decides how ADE runs the editor, omp and shell panes and the tasks and services of that repo:
`direnv` (`direnv exec`, needs an allowed `.envrc`), `nix` (`nix develop` of the repo's flake) or `none`. `auto` (the
default) picks direnv for a repo with an `.envrc`, else `nix develop` for a flake with a `devShell`, else none.
Services that belong to no repo are never wrapped. Details: [setup.md](setup.md#dev-shells-direnv--nix).

### Team dotfiles

```toml
[dotfiles]
repo    = "git@gitlab.example.com:acme/dotfiles.git"   # the repo the team uses with `coder dotfiles`
branch  = "main"              # optional; default: the repository's default branch
install = "script/setup"      # optional; overrides the install script search
symlink = true                # optional; default true: link the dotfiles when there is no install script
```

`ade setup` and `ade dotfiles` apply the repo the way `coder dotfiles` does, so a team's Coder dotfiles also work
outside Coder. The repo is cloned to `<data dir>/dotfiles/<project>` (a URL on a configured forge host is cloned with
that forge's token; SSH host keys stay checked) and updated on later runs. The clone is ADE-managed: do not edit it.
Each run fetches and moves it to `origin/<branch>` (`branch`, else the remote's default branch), switching branch when
that changed. If it cannot fast-forward (the remote was force-pushed, or something changed the clone), it is reset to
`origin/<branch>` and local commits and edits to tracked files are discarded. When `repo` changes, the old clone is
moved to `<project>.backup-<timestamp>` and the new one cloned. Two URLs are the same repository when host and path
match, whatever the scheme (`ssh` or `https`), user, port, case of the host, trailing `.git` or slash.

Then ADE runs the first of `install.sh`, `install`, `bootstrap.sh`, `bootstrap`, `setup.sh`, `setup`,
`script/install.sh`, `script/install`, `script/bootstrap.sh`, `script/bootstrap`, `script/setup.sh`, `script/setup` that
exists, or the file named by `install`, inside the clone. A script without the executable bit runs through `sh`. It sees `ADE_PROJECT`,
`ADE_ROOT` and `ADE_DOTFILES_DIR`. With no script and `symlink = true`, every top-level entry whose name starts with
`.` is linked into `$HOME`, except `.git*` files other than `.gitconfig`; an existing file is first moved to
`<name>.bak` (`<name>.bak.1`, `<name>.bak.2`, ... when that name is taken; a backup is never overwritten), an existing
link is replaced. Running again changes nothing that is already in place. Symlinking needs an absolute `$HOME`; without
one the project fails instead of writing into the working directory.

The script runs in its own process group with a 10 minute limit; on timeout or cancel the whole group is killed. After
the script exits, ADE stops reading its output after 5 seconds, so a daemon it started cannot hang the run.
`install` must be a path inside the repo; this is checked again when the script runs.

**Security.** The install script is code your team pushes. It runs with your environment, minus the variables named by
`token_env` in your `[forges.*]` config. It still receives everything else: other secrets in your environment,
`SSH_AUTH_SOCK`, and files you can read. Tokens from `token_cmd` are not in its environment.
Treat changes to `[dotfiles]` and to that repo like changes to a CI pipeline. `ade project sync` never runs it; only
`ade setup` and `ade dotfiles` do.

In a Coder workspace (`CODER=true`) whose own dotfiles step already cloned the same URL to
`~/.config/coderv2/dotfiles`, ADE skips the project (`applied by coder`). Each person can change this in the user
config: `[dotfiles] enabled = false` turns it off, `repo` and `branch` replace the manifest's (only for projects that
have a `[dotfiles]` table). `ade doctor` warns when a project has dotfiles that were never applied.

### Self-hosted forge keys (user config, not the manifest)

A manifest names its forge only by local name; where that forge lives is each person's `[forges.<name>]`. For a
self-hosted GitLab (`ade init --host gitlab.example.com/gitlab --ca-file … --ssh-port 2222`) these keys matter:
`url` (a path prefix such as `/gitlab` is kept for https clone URLs and API calls), `ca_file` (PEM bundle trusted for
the API, manifest fetches over https and git over https), `ssh_port` (clone URLs become `ssh://git@host:2222/…`) and
`max_concurrency` (in-flight API requests per forge, default 4). See [setup.md](setup.md#self-hosted-gitlab).

## GitHub example

```toml
name  = "acme-gh"
forge = "personal"

[tracker]
project    = "acme/tracker"
iterations = { source = "github-project" }
weight     = { source = "github-field", field = "Estimate" }

[labels.type]
pattern = "type:{value}"

[labels.app]
pattern = "app:{value}"

[labels.workflow]
pattern = "workflow:{value}"
stage   = { started = "in progress", review = "in review", merged = "done" }

[repos.api]
path = "acme/api"
app  = ["api"]
```

```sh
ade init --yes --forge github --token-cmd 'gh auth token' \
    --project 'git+https://github.com/acme/tracker.git#.ade.toml'
```

## Bootstrapping a manifest

A team lead does not have to write the manifest by hand: `ade project bootstrap` reads an existing forge group
and generates it. It needs a forge entry in your own user config (run `ade init` first); that entry's name
becomes the manifest's `forge` key.

```sh
ade project bootstrap --forge work --group acme --yes --out acme.ade.toml
ade project bootstrap --forge personal --group my-org --match 'side-*' --tracker side-planning
ade project bootstrap --forge work --group acme --publish --dry-run     # preview the merge request
```

| Flag | Meaning |
|---|---|
| `--forge` | entry of your user config; may be left out when there is exactly one |
| `--group` | GitLab group (subgroups included) or GitHub organisation or user |
| `--tracker` | tracker repo, full path or unique name; detected when left out |
| `--name` | project name; defaults to the group's name |
| `--match` | glob on repo names (or paths): include only these repos |
| `--out` | file to write (default `.ade.toml`); `-` prints to stdout |
| `--yes` | never prompt; ambiguities with no sensible default fail naming the flag that settles them |
| `--force` | replace an existing `--out` file, or an existing manifest when publishing |
| `--publish` | open a merge request adding `.ade.toml` to the tracker repo (a forge write, audited) |
| `--dry-run` | write nothing, local or forge; print the manifest and what `--publish` would do |
| `--json` | machine-readable result: manifest text, repos, skipped repos, notes, warnings, publish plan |

In a terminal it asks about what it cannot settle (which repo is the tracker, which iteration cadence); with
`--yes` or without a terminal it never prompts. Everything is read through the forge API: nothing is cloned.

**What it detects** (and only states what it found; the rest is left as a comment):

- **Repos:** the group's repos, archived ones skipped. A name prefix shared by every repo (`acme-api`,
  `acme-web`, `acme-tracker`) is dropped from the `[repos.<name>]` keys.
- **Tracker:** `--tracker`, else the repo whose recent issues carry the most app-like labels, else the one named
  `tracker`, `issues` or `planning`. When these disagree or tie it asks, or fails under `--yes`.
- **Label patterns:** scoped labels with `::`, `:` or `/` separators. The facet is read from the scope name:
  `type` (also `kind`), `app` (`area`, `component`, `service`), `workflow` (`status`, `state`, `stage`) and
  `weight` (`points`, `estimate`; values must be numbers). The spelling used on the most issues wins.
- **Workflow stages:** by value name. `in progress` / `doing` / `wip` is `started` and `draft`; anything with
  `review` is `review`; `done` / `closed` / `shipped` is `merged` and also `labels.workflow.done`. `blocked` is
  left unmapped (ADE already reads it as blocked), as are values like `backlog`.
- **App values:** a repo takes the app-label values equal to its name, else containing or contained in it,
  else its own name (with a comment saying it is a guess).
- **Iterations:** GitLab iterations when the group licenses them and has some (`cadence` is set when there are
  several; the one with the most iterations is the default), otherwise milestones, otherwise `auto`. On GitHub:
  the tracker repo's milestones.
- **Weight:** the GitLab weight field when the group licenses it and issues use it, else a `weight` label
  pattern, else `none`.
- **Tasks:** from each repo's root files at the default branch. `go.mod` gives `go test ./...` (and `go run .`
  when there is a root `main.go`), `package.json` gives its `dev`, `test`, `build` and `lint` scripts through the
  package manager the lockfile names, `Makefile` gives `dev`/`run`/`serve`/`start`, `test`, `build` and `lint`
  targets, and a `Procfile` gives one service per process (`$PORT` in the command sets `port = "auto"` and
  `PORT = "{port}"`). A `flake.nix` with a `devShell` wraps every command in `nix develop -c`. When two files
  offer the same task name, the order is Procfile, `package.json`, `Makefile`, `go.mod`. Docker compose services
  and `package.json` install hooks are only mentioned in comments, and no `ready` probe is guessed.
- **Conventions:** `child_issue = "auto"`, plus a comment naming the repos' MR templates.

What it cannot know stays a comment: `requires` between repos, `ready` probes, hooks, services and any
`conventions` beyond `child_issue`. Read the generated comments, adjust, and commit the file to the tracker repo.

It validates the result exactly as `ade config check` would and refuses to write an invalid manifest. An existing
file is never replaced without `--force`; a file that already holds exactly the manifest this run generates is left
alone, so repeating a command is safe. (The output is deterministic: it changes only when the forge does.)

**Publishing.** `--publish` creates the branch `ade/bootstrap-manifest` on the tracker repo, commits `.ade.toml`
to it and opens a merge request into the default branch. It is idempotent: an identical file is not committed
again, and an open merge request for the branch is reused (so editing the draft and publishing again updates the
same request). If the default branch already has a different `.ade.toml`, publishing refuses unless `--force`;
if it has the identical one, there is nothing to do. In a terminal the plan is shown and confirmed before anything
is written. The three writes (`forge.branch.create`, `forge.file.commit`, `forge.mr.create`) land in the audit log;
`--dry-run` shows the plan and the request body.

Next steps, which the command also prints: try the file locally with `ade init --project <file>` (or
`ade project add <file>`), and once it is merged, teammates use
`ade init --yes --forge <kind> --token-env <VAR> --project 'git+<host>/<tracker>.git#.ade.toml'`.

## Local overrides and the JSON Schema

A `*.local.toml` beside a manifest (`acme.ade.toml` → `acme.ade.local.toml`) holds per-machine changes that are never shared, e.g. a clone URL or a fixed port. It is merged over the manifest before validation: tables merge key by key (so `[repos.api]` can change one key and keep the rest), every other value, arrays included, replaces the base. A manifest fetched from `git+…` or `https://…` has no file beside it, so its override lives in ADE's config directory under the manifest's file name (`~/.config/ade/acme.ade.local.toml`). `ade config check` names the override it applied, and `ade project add` / `ade update` store the merged manifest in the project snapshot.

`ade config schema --manifest` prints a JSON Schema of the manifest (`docs/schema/manifest.schema.json`), for completion and typo detection in taplo or VS Code Even Better TOML: put `#:schema …/manifest.schema.json` on the first line.

## Checking a manifest

```sh
ade config check                 # validates the user config and every registered manifest
ade doctor --probe               # read-only: confirms the forge exposes what the manifest relies on
```

`ade config check` reports unknown keys, cross-references that do not resolve, dependency cycles and bad enum
values with the path of the offending key. A manifest that fails validation is rejected before ADE touches the
disk, and an already registered project keeps running on its last good copy until the fix is pulled.

## Changing a shared manifest

Treat it like code: change it by merge request in the tracker repo. Teammates pick the change up on their next
`ade update` (at startup, or on the `update.interval`). A change that renames a repo is not applied to existing
clones: the old directory stays on disk, flagged as "not in manifest anymore".
