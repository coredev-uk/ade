package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// ParseSprintLength parses a sprint length such as "2w" or "14d".
func ParseSprintLength(s string) (time.Duration, error) {
	bad := fmt.Errorf("invalid sprint length %q: want e.g. 2w or 14d", s)
	if len(s) < 2 {
		return 0, bad
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 1 {
		return 0, bad
	}
	switch strings.ToLower(s[len(s)-1:]) {
	case "w":
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return 0, bad
}

// Manifest is a shareable project definition (*.ade.toml). It never holds secrets.
type Manifest struct {
	Name        string               `toml:"name"`
	Forge       string               `toml:"forge"` // key into Config.Forges
	Tracker     Tracker              `toml:"tracker"`
	Labels      Labels               `toml:"labels"`
	Repos       map[string]Repo      `toml:"repos"`
	Services    map[string]Task      `toml:"services"`
	Conventions Conventions          `toml:"conventions"`
	Groups      map[string][]string  `toml:"groups"`
	Workspaces  map[string]Workspace `toml:"workspaces"`
	Dotfiles    Dotfiles             `toml:"dotfiles"`

	// Source is where the manifest came from; Undecoded lists unknown keys.
	Source    string   `toml:"-"`
	Undecoded []string `toml:"-"`
	// Overlay is the local override merged into this manifest, when ReadManifestFile
	// or the project service applied one.
	Overlay string `toml:"-"`
}

type Tracker struct {
	Project    string           `toml:"project"`
	Board      string           `toml:"board"`
	Iterations IterationsSource `toml:"iterations"`
	Weight     WeightSource     `toml:"weight"`
}

// WeightSource says where issue weights (story points) come from.
type WeightSource struct {
	Source  string `toml:"source"`  // auto | gitlab | label | github-field | none
	Pattern string `toml:"pattern"` // label source, e.g. "weight::{value}"
	Field   string `toml:"field"`   // github-field source, e.g. "Estimate"
}

type IterationsSource struct {
	Source  string `toml:"source"`  // auto | gitlab-iterations | milestones | labels | github-project | cadence
	Cadence string `toml:"cadence"` // GitLab cadence title filter
	Pattern string `toml:"pattern"` // labels source, e.g. "Sprint::{n}"
	Start   string `toml:"start"`   // labels source: first day of sprint 1, "YYYY-MM-DD"
	Length  string `toml:"length"`  // labels source: sprint length, "2w" or "14d"
	Overlap int    `toml:"overlap"` // working days around a boundary that show the adjacent sprint (default 1)
}

type Labels struct {
	Type     LabelFacet    `toml:"type"`
	App      LabelFacet    `toml:"app"`
	Workflow WorkflowFacet `toml:"workflow"`
}

type LabelFacet struct {
	Pattern string `toml:"pattern"` // e.g. "type::{value}"
}

type WorkflowFacet struct {
	Pattern string            `toml:"pattern"`
	Stage   map[string]string `toml:"stage"` // derived stage -> workflow value
	Sync    string            `toml:"sync"`  // auto | ask | off; unset means ask
	// Done is the workflow value that marks a tracker issue finished while it
	// stays open (teams that close sprint work only at sprint end).
	Done string `toml:"done"`
}

// SyncMode is Sync with the default applied: unset means "ask", so a manifest
// that never mentions it does not rewrite a shared board on its own.
func (w WorkflowFacet) SyncMode() string {
	if w.Sync == "" {
		return "ask"
	}
	return w.Sync
}

// Dotfiles is the team's dotfiles repository: the one members of the project
// use with `coder dotfiles`. `ade setup` and `ade dotfiles` clone it and run
// its install script, or symlink its dotfiles into $HOME. The install script
// is team code that runs with your environment.
type Dotfiles struct {
	Repo   string `toml:"repo"`   // git URL; empty means the project has no dotfiles
	Branch string `toml:"branch"` // default: the repository's default branch
	// Install is a script inside the repo that overrides the automatic search
	// (install.sh, install, bootstrap.sh, ...).
	Install string `toml:"install"`
	// Symlink says whether the repo's dotfiles are linked into $HOME when no
	// install script is found (default true).
	Symlink *bool `toml:"symlink"`
}

// Configured reports whether the manifest has a dotfiles repo.
func (d Dotfiles) Configured() bool { return d.Repo != "" }

// SymlinkFallback is Symlink with the default applied.
func (d Dotfiles) SymlinkFallback() bool { return d.Symlink == nil || *d.Symlink }

// Repo is one repository in a project.
type Repo struct {
	Path          string          `toml:"path"`           // forge path, e.g. group/api
	URL           string          `toml:"url"`            // explicit clone URL; overrides forge+path
	DefaultBranch string          `toml:"default_branch"` // detected from origin/HEAD when empty
	App           []string        `toml:"app"`            // app-label values routed here
	Requires      []string        `toml:"requires"`       // checkout dependencies (repo names)
	Hooks         Hooks           `toml:"hooks"`
	Tasks         map[string]Task `toml:"tasks"`
	// Devshell says how a worktree's dev shell is entered for shell, omp and
	// editor panes and for run-graph tasks: auto (default: direnv when the
	// worktree has an .envrc, else nix develop when its flake.nix has a
	// devShell), direnv, nix or none.
	Devshell string `toml:"devshell"`
}

type Hooks struct {
	PostClone    []string `toml:"post_clone"`
	PostWorktree []string `toml:"post_worktree"`
	// Install are shell commands that set a worktree up for running (`npm ci`).
	// ADE runs them in every worktree of the repo a workspace uses, inside the
	// repo's dev shell, when the workspace first opens and on `ade ws install`.
	Install []string `toml:"install"`
}

// Task is a node in the run graph: a repo task or a service.
type Task struct {
	Cmd      string            `toml:"cmd"`
	Kind     string            `toml:"kind"` // service (default) | oneshot
	Port     PortSpec          `toml:"port"`
	Env      map[string]string `toml:"env"`
	Requires []string          `toml:"requires"` // node refs: "<repo>.<task>" or "services.<name>"
	Ready    Ready             `toml:"ready"`
	Keep     bool              `toml:"keep"`
}

// PortSpec is either "auto" or a fixed port number.
type PortSpec struct {
	Auto  bool
	Fixed int
}

func (p *PortSpec) UnmarshalTOML(v any) error {
	switch x := v.(type) {
	case string:
		if x != "auto" {
			return fmt.Errorf("port: want \"auto\" or a number, got %q", x)
		}
		p.Auto = true
	case int64:
		if x <= 0 || x > 65535 {
			return fmt.Errorf("port %d out of range", x)
		}
		p.Fixed = int(x)
	default:
		return fmt.Errorf("port: unsupported value %v", v)
	}
	return nil
}

type Ready struct {
	HTTP    string   `toml:"http"`
	TCP     string   `toml:"tcp"`
	Log     string   `toml:"log"`
	Timeout Duration `toml:"timeout"`
}

type Conventions struct {
	Branch       string   `toml:"branch"`
	ChildTitle   string   `toml:"child_title"`
	ChildBody    string   `toml:"child_body"`
	Link         []string `toml:"link"`
	ChildIssue   string   `toml:"child_issue"`   // auto | always | never
	CommitPrefix string   `toml:"commit_prefix"` // e.g. "GL-{parent}"; see commitmsg
	MR           MRConv   `toml:"mr"`
}

type MRConv struct {
	Create   string `toml:"create"`   // ask | always | never
	Template string `toml:"template"` // template name in the repo's MR template dir
	Draft    *bool  `toml:"draft"`    // unset: true. A pointer tells "false" from "not said".
	Closes   string `toml:"closes"`
	Mention  string `toml:"mention"`
}

// DraftMR reports whether start work opens the MR as a draft: true unless the
// manifest says draft = false.
func (c MRConv) DraftMR() bool { return c.Draft == nil || *c.Draft }

type Workspace struct {
	Layout string `toml:"layout"`
	Run    string `toml:"run"` // group name
	// Services says where the run group's services start: background (default)
	// in a tab of their own opened with the workspace, or manual in the strip
	// of the workspace tab, each waiting for Enter.
	Services string `toml:"services"`
}

// Workspace service modes.
const (
	ServicesBackground = "background"
	ServicesManual     = "manual"
)

// ServicesMode is Services with the default applied.
func (w Workspace) ServicesMode() string {
	if w.Services == "" {
		return ServicesBackground
	}
	return w.Services
}

// ParseManifest decodes manifest TOML. It does not validate cross-references;
// see Validate.
func ParseManifest(data []byte, source string) (*Manifest, error) {
	var m Manifest
	md, err := toml.Decode(string(data), &m)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	m.Source = source
	for _, k := range md.Undecoded() {
		m.Undecoded = append(m.Undecoded, k.String())
	}
	sort.Strings(m.Undecoded)
	return &m, nil
}

// RepoNames returns repo names in stable order.
func (m *Manifest) RepoNames() []string {
	names := make([]string, 0, len(m.Repos))
	for n := range m.Repos {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
