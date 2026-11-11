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
sync    = "auto"               # auto | ask | off
done    = "done"               # optional, see below
```

- `type` and `app` missing on an issue puts it in the **needs triage** filter.
- `workflow.stage` maps the stage ADE *derives* from forge facts (child issue, branch, MR, CI, approvals) to your
  board column names. With `sync = "auto"` ADE moves the label when the derived stage changes (and removes the
  old one); `ask` confirms each move; `off` only shows drift. Every move is recorded in the audit log.
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

```toml
[tracker]
iterations = { source = "labels", pattern = "Sprint::{n}", start = "2026-01-05", length = "2w", overlap = 1 }
```

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
- `mr.create`: `ask` (default), `always`, `never`. `mr.draft` opens the MR as a draft; leave it out (or set `true`) for a draft MR, `false` to open it ready for review. `mr.closes = "child"`
  puts `Closes #<child>` in the MR body; `mr.mention = "parent"` references the tracker issue.
- `mr.template`: the template name in the repo's MR template directory (GitLab
  `.gitlab/merge_request_templates/<name>.md`, GitHub `.github/pull_request_template.md`). ADE shows the
  rendered body, and lists any quick actions in it (`/assign`, `/label`, ...) so they can be kept or stripped
  before anything is sent.

## Repos, hooks and run graph

```toml
[repos.web.hooks]
post_clone    = ["npm ci"]               # once, in main/ after `ade project add`
post_worktree = ["npm ci"]               # every new worktree

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
