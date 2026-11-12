package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// JSON Schema (draft 2020-12) of the user config and of project manifests,
// generated from the Go structs by reflection so it cannot drift from what
// the decoder accepts. Editors use it for completion and typo detection
// (taplo, VS Code Even Better TOML: `#:schema <file>` on the first line).
// The generated files are committed under docs/schema/ and a test fails when
// they are stale; regenerate them with `ade config schema --write docs/schema`.

// SchemaKind selects which document a schema describes.
type SchemaKind string

const (
	SchemaConfig   SchemaKind = "config"
	SchemaManifest SchemaKind = "manifest"
)

// SchemaFile is the committed file name of a schema.
func SchemaFile(k SchemaKind) string { return string(k) + ".schema.json" }

// Schema returns the JSON Schema of kind, indented, with a trailing newline.
func Schema(kind SchemaKind) ([]byte, error) {
	var (
		root  reflect.Type
		title string
	)
	switch kind {
	case SchemaConfig:
		root, title = reflect.TypeFor[Config](), "ADE user config (config.toml, config.local.toml)"
	case SchemaManifest:
		root, title = reflect.TypeFor[Manifest](), "ADE project manifest (*.ade.toml, *.ade.local.toml)"
	default:
		return nil, fmt.Errorf("unknown schema %q (want config or manifest)", kind)
	}
	g := schemaGen{kind: kind}
	doc := g.node(root, "")
	doc["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	doc["title"] = title
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type schemaGen struct{ kind SchemaKind }

var (
	durationType = reflect.TypeFor[Duration]()
	portType     = reflect.TypeFor[PortSpec]()
	timeType     = reflect.TypeFor[time.Time]()
)

// node describes type t at TOML key path (map values are ".*", array items
// share their array's path).
func (g schemaGen) node(t reflect.Type, path string) map[string]any {
	n := map[string]any{}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t {
	case durationType:
		n["type"] = "string"
		n["pattern"] = `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
		n["examples"] = []string{"10m", "90s"}
	case portType:
		n["oneOf"] = []any{
			map[string]any{"const": "auto"},
			map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
		}
	case timeType:
		n["type"] = "string"
		n["format"] = "date-time"
	default:
		switch t.Kind() {
		case reflect.String:
			n["type"] = "string"
		case reflect.Bool:
			n["type"] = "boolean"
		case reflect.Int, reflect.Int64:
			n["type"] = "integer"
		case reflect.Float64:
			n["type"] = "number"
		case reflect.Slice:
			n["type"] = "array"
			n["items"] = g.node(t.Elem(), path)
		case reflect.Map:
			n["type"] = "object"
			n["additionalProperties"] = g.node(t.Elem(), path+".*")
		case reflect.Struct:
			n["type"] = "object"
			props := map[string]any{}
			for f := range t.Fields() {
				name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
				if name == "" || name == "-" || !f.IsExported() {
					continue
				}
				props[name] = g.node(f.Type, join(path, name))
			}
			n["properties"] = props
			n["additionalProperties"] = false
		default:
			panic(fmt.Sprintf("config schema: unsupported type %s at %q", t, path))
		}
	}
	if enum, ok := schemaEnums[g.key(path)]; ok {
		n["enum"] = enum
	}
	if d, ok := schemaDefaults[g.key(path)]; ok {
		n["default"] = d
	}
	if d, ok := schemaDocs[g.key(path)]; ok {
		n["description"] = d
	}
	if p, ok := schemaPatterns[g.key(path)]; ok {
		n["pattern"] = p
	}
	return n
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// key is the lookup key of path in the tables below.
func (g schemaGen) key(path string) string { return string(g.kind) + ":" + path }

// schemaEnums mirrors the allowed values validate.go enforces.
var schemaEnums = map[string][]string{
	"config:update.feature_branches":      {"ff-if-clean", "notify-only"},
	"config:forges.*.kind":                {"gitlab", "github"},
	"config:forges.*.git_protocol":        {"ssh", "https"},
	"config:host.mode":                    {"auto", "zellij", "vscode", "plain"},
	"config:host.editor":                  {"auto", "nvim", "ade-nvim", "code", "$EDITOR"},
	"config:host.glyphs":                  {"unicode", "nerd", "ascii"},
	"config:host.layout":                  {"code", "agent", "manual"},
	"config:ai.mode":                      {"off", "ask", "auto"},
	"config:ai.profiles.*.driver":         AIDrivers,
	"config:ai.profiles.*.agent":          AgentDrivers,
	"config:ai.profiles.*.billing.source": AIBillingSource,
	"manifest:tracker.iterations.source":  {"auto", "gitlab-iterations", "milestones", "labels", "github-project", "cadence"},
	"manifest:tracker.weight.source":      {"auto", "gitlab", "label", "github-field", "none"},
	"manifest:labels.workflow.sync":       {"auto", "ask", "off"},
	"manifest:conventions.child_issue":    {"auto", "always", "never"},
	"manifest:conventions.mr.create":      {"ask", "always", "never"},
	"manifest:repos.*.tasks.*.kind":       {"service", "oneshot"},
	"manifest:repos.*.devshell":           {"auto", "direnv", "nix", "none"},
	"manifest:services.*.kind":            {"service", "oneshot"},
	"manifest:workspaces.*.layout":        {"code", "agent", "review", "manual"},
	"manifest:workspaces.*.services":      {ServicesBackground, ServicesManual},
}

// schemaDefaults are the values an unset key takes, where that is not the
// zero value.
var schemaDefaults = map[string]any{
	"manifest:labels.workflow.sync":  "ask",
	"manifest:workspaces.*.services": ServicesBackground,
}

// schemaPatterns constrain free-form strings validate.go checks by regexp.
var schemaPatterns = map[string]string{
	"config:records.audit_retain": retainFmt.String(),
}

// schemaDocs holds the key descriptions editors show on hover. TestSchemaDocsNameRealKeys
// fails when an entry names a key the structs do not have.
var schemaDocs = map[string]string{
	"config:user.handle":                  "Your username on the forge; \"mine\" means assigned to or authored by this handle.",
	"config:user.timezone":                "IANA time zone for standup and sprint boundaries.",
	"config:user.workdays":                "Working days, lower-case three-letter names. Standup looks back to the previous working day.",
	"config:user.holidays":                "Non-working dates (YYYY-MM-DD); standup skips them.",
	"config:forges":                       "One entry per GitLab or GitHub host, keyed by a name manifests refer to.",
	"config:forges.*.url":                 "Web/API base URL; defaults to gitlab.com or github.com by kind.",
	"config:forges.*.token_cmd":           "Command whose stdout is the access token (the write token; it also reads when no read token is set). Secrets never live in the config file.",
	"config:forges.*.token_env":           "Environment variable holding the access token (the write token; it also reads when no read token is set). Agents ADE launches do not inherit it when a read token is set.",
	"config:forges.*.read_token_cmd":      "Command whose stdout is a read-only token (GitLab read_api, read_user, read_repository). With a read token, reads use it and only writes use the token.",
	"config:forges.*.read_token_env":      "Environment variable holding a read-only token. With a read token, reads use it and only writes use the token.",
	"config:forges.*.git_protocol":        "Protocol used for clones (default ssh).",
	"config:forges.*.ca_file":             "PEM bundle of the certificate authority a self-hosted forge uses; trusted for API and https git. SSL_CERT_FILE is honoured too.",
	"config:forges.*.ssh_port":            "SSH port of a self-hosted forge when not 22; clone URLs become ssh://git@host:port/path.",
	"config:forges.*.max_concurrency":     "Most API requests in flight to this forge at once (default 4).",
	"config:paths.root":                   "Projects root: <root>/<project>/<repo>/<worktree> (default ~/ade).",
	"config:projects":                     "Registered project manifests.",
	"config:projects.source":              "Local path, https URL, or git+<url>#<path> of a *.ade.toml manifest.",
	"config:update.on_start":              "Run a full update (git, then forge sync) at every start (default true).",
	"config:update.interval":              "How often the dashboard updates in the background (default 10m).",
	"config:update.feature_branches":      "ff-if-clean fast-forwards clean feature worktrees; notify-only only reports them.",
	"config:update.concurrency":           "Repositories updated in parallel.",
	"config:notify.toast":                 "Event-kind globs shown as pop-ups; every event is recorded in the inbox regardless.",
	"config:records.dir":                  "Where audit logs and work-item records live (default $XDG_DATA_HOME/ade). Keep it outside any git work tree.",
	"config:records.audit_retain":         "\"forever\" (default) or a day count such as \"365d\": whole monthly audit files older than that are deleted; the current month never is.",
	"config:records.snapshot_comments":    "Latest comments copied into item.md.",
	"config:host.mode":                    "Where workspaces open: zellij tabs, VS Code, or plain terminal supervision (auto detects).",
	"config:host.editor":                  "auto: code inside a VS Code terminal, else nvim, else $EDITOR. ade-nvim opens the Nix-wrapped Neovim; $EDITOR uses the environment.",
	"config:host.glyphs":                  "Glyph set of the dashboard.",
	"config:host.layout":                  "Default tab layout: code (editor main pane), agent (omp with the ADE implement skill) or manual (shell). Overrides the manifest workspace's layout; empty keeps it.",
	"config:ai.mode":                      "off disables every AI call; ask confirms over the budget threshold; auto runs within caps.",
	"config:ai.profile":                   "Key of the active profile in ai.profiles.",
	"config:ai.profiles":                  "Places AI calls can go: a driver plus the models its roles use.",
	"config:ai.profiles.*.driver":         "How calls are made.",
	"config:ai.profiles.*.agent":          "CLI that runs agent work for this profile (default omp).",
	"config:ai.profiles.*.models":         "Role (explain, classify, draft, plan, implement, review) to model id. A missing classify model falls back to explain; a missing review model to draft, then explain.",
	"config:ai.profiles.*.extra_headers":  "Headers sent on every request of an HTTP driver, e.g. a gateway's team or tag headers.",
	"config:ai.profiles.*.timeout":        "Per-request timeout of an HTTP driver, e.g. \"90s\" (default 5m).",
	"config:ai.profiles.*.prices":         "Model id to list prices in USD per million tokens, for pre-flight estimates.",
	"config:ai.profiles.*.billing.source": "Where authoritative spend comes from.",
	"config:dotfiles.enabled":             "Apply project dotfiles on this machine (default true). false turns them off for every project.",
	"config:dotfiles.repo":                "Use this git URL instead of the dotfiles repo a project's manifest names. Has no effect on projects without [dotfiles].",
	"config:dotfiles.branch":              "Branch of the overriding repo (default: the repository's default branch).",
	"config:budget.daily_usd":             "Daily local spend cap; 0 is no cap.",
	"config:budget.weekly_usd":            "Weekly local spend cap; 0 is no cap.",
	"config:budget.sprint_usd":            "Sprint cap, enforced while a sprint window is known; 0 is no cap.",
	"config:budget.call_usd":              "Hard cap per call; also passed to omp as --max-cost.",
	"config:budget.confirm_over_usd":      "Pre-flight confirmation threshold per action.",
	"config:budget.agent_run_usd":         "Passed as --max-cost to agent runs.",
	"manifest:name":                       "Project name; becomes the directory under the projects root.",
	"manifest:forge":                      "Key into the user config's [forges].",
	"manifest:tracker.project":            "Forge path of the tracker project.",
	"manifest:tracker.iterations.source":  "Where sprints come from.",
	"manifest:tracker.iterations.overlap": "Working days around a boundary that show the adjacent sprint (default 1).",
	"manifest:tracker.weight.source":      "Where issue weights come from.",
	"manifest:labels.workflow.stage":      "Derived stage (started, draft, review, merged) to workflow label value.",
	"manifest:labels.workflow.sync":       "Whether ADE moves the workflow label when the derived stage changes: auto moves it, ask moves it only when you confirm (ade start --move-label, or the start dialog) and lists background moves as pending, off never moves it (default ask).",
	"manifest:labels.workflow.done":       "Workflow value that marks an open tracker issue finished.",
	"manifest:dotfiles":                   "The team's dotfiles repository (the one used with `coder dotfiles`). `ade setup` and `ade dotfiles` clone it and run its install script, or symlink its dotfiles into $HOME. The install script is team code that runs with your environment.",
	"manifest:dotfiles.repo":              "Git URL of the dotfiles repository. A URL on a configured forge host is cloned with that forge's token.",
	"manifest:dotfiles.branch":            "Branch to check out (default: the repository's default branch).",
	"manifest:dotfiles.install":           "Script inside the repo to run, overriding the search for install.sh, install, bootstrap.sh, bootstrap, setup.sh, setup, script/install.sh, script/install, script/bootstrap.sh, script/bootstrap, script/setup.sh and script/setup.",
	"manifest:dotfiles.symlink":           "Link the repo's dotfiles into $HOME when no install script is found (default true).",
	"manifest:project":                    "Project-wide setup ADE generates under <root>/<project>/ on every add and update: agent guidance, a nix shell wrapper.",
	"manifest:project.agents":             "File relative to the manifest, written as <root>/<project>/AGENTS.md (the project's agent guidance). Needs a local or git+ manifest source.",
	"manifest:project.packages":           "nixpkgs attributes put on PATH for every pane, task and service of the project through <root>/<project>/.ade/shell (needs nix on PATH).",
	"manifest:project.nixpkgs":            "Flake reference the packages come from (default: the registry's \"nixpkgs\").",
	"manifest:repos":                      "Repositories of the project.",
	"manifest:repos.*.path":               "Forge path, e.g. group/api.",
	"manifest:repos.*.url":                "Explicit clone URL; overrides forge + path.",
	"manifest:repos.*.app":                "App-label values routed to this repo.",
	"manifest:repos.*.requires":           "Checkout dependencies (repo names).",
	"manifest:repos.*.devshell":           "How a worktree's dev shell is entered for panes and tasks: auto (direnv with an .envrc, else nix develop with a flake devShell), direnv, nix or none.",
	"manifest:repos.*.hooks.install":      "Shell commands that set a worktree up (npm ci). Run in each worktree of this repo a workspace uses, inside the repo's dev shell, in the services tab when the workspace first opens (background mode), and on ade ws install and ade ws up in either mode; re-run only when the commands or the worktree's lockfiles change. Runs with ADE's environment: same trust as post_worktree.",
	"manifest:repos.*.tasks":              "Run-graph nodes of this repo.",
	"manifest:repos.*.tasks.*.requires":   "Node references: \"<repo>.<task>\" or \"services.<name>\".",
	"manifest:repos.*.tasks.*.env":        "Environment of the task; values may use {port}, {dir}, {state} and <node>.port templates.",
	"manifest:repo_groups":                "Display-only repo groups: `ade project status` lists repos under them and each group with a workspace gets a .code-workspace file. They never change routing, checkouts or the run graph.",
	"manifest:repo_groups.*.repos":        "Repo names in the group; a repo may be in several groups.",
	"manifest:repo_groups.*.workspace":    "File stem of <root>/<project>/<workspace>.code-workspace, a VS Code multi-root workspace of the group's repos (baseline worktrees).",
	"manifest:services":                   "Shared infrastructure nodes that are not repos.",
	"manifest:conventions.branch":         "Branch name template, e.g. {child_iid}-{slug}.",
	"manifest:conventions.child_issue":    "auto creates a child issue when the tracker project differs from the code repo.",
	"manifest:conventions.commit_prefix":  "Prefix for the first commit of an MR (and a squash merge message) that ADE makes, e.g. \"GL-{parent}\". Placeholders: {parent} (tracker issue iid), {child} (child issue iid, else the branch's leading number, else the parent's), {project}.",
	"manifest:conventions.mr.create":      "Whether start work creates the MR: ask, always or never.",
	"manifest:conventions.mr.draft":       "Open the MR as a draft. Unset means true.",
	"manifest:groups":                     "Named lists of nodes that workspaces run.",
	"manifest:workspaces":                 "Named workspaces: a layout and the run group to bring up.",
	"manifest:workspaces.*.layout":        "Zellij tab layout.",
	"manifest:workspaces.*.services":      "background (default) starts the run group's services in a tab of their own, after the install hooks, when the workspace first opens; manual keeps them in the workspace tab's strip, each waiting for Enter.",

	// Its own alignment block: the longer key would realign the whole table.
	"manifest:conventions.mr.reviewer_group": "Group whose members start work asks to review the MR: a GitLab group path (acme/reviewers) or, on GitHub, org/team-slug of the repository's owner. Empty requests nobody.",
}

// schemaHasKey reports whether the schema of the kind in key ("config:a.b.*")
// has a node at that path.
func schemaHasKey(key string) bool {
	kind, path, _ := strings.Cut(key, ":")
	doc, err := Schema(SchemaKind(kind))
	if err != nil {
		return false
	}
	var n map[string]any
	if json.Unmarshal(doc, &n) != nil {
		return false
	}
	if path == "" {
		return true
	}
	for part := range strings.SplitSeq(path, ".") {
		var next any
		for {
			if items, ok := n["items"].(map[string]any); ok {
				n = items
				continue
			}
			break
		}
		if part == "*" {
			next = n["additionalProperties"]
		} else if props, ok := n["properties"].(map[string]any); ok {
			next = props[part]
		}
		m, ok := next.(map[string]any)
		if !ok {
			return false
		}
		n = m
	}
	return true
}
