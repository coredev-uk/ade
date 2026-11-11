package config

import (
	"fmt"
	"sort"

	"github.com/BurntSushi/toml"
)

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

	// Source is where the manifest came from; Undecoded lists unknown keys.
	Source    string   `toml:"-"`
	Undecoded []string `toml:"-"`
}

type Tracker struct {
	Project    string           `toml:"project"`
	Board      string           `toml:"board"`
	Iterations IterationsSource `toml:"iterations"`
}

type IterationsSource struct {
	Source  string `toml:"source"`  // auto | gitlab-iterations | milestones | labels | github-project | cadence
	Cadence string `toml:"cadence"` // GitLab cadence title filter
	Pattern string `toml:"pattern"` // labels source, e.g. "Sprint::{n}"
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
	Sync    string            `toml:"sync"`  // auto | ask | off
}

// Repo is one repository in a project.
type Repo struct {
	Path          string          `toml:"path"`           // forge path, e.g. group/api
	URL           string          `toml:"url"`            // explicit clone URL; overrides forge+path
	DefaultBranch string          `toml:"default_branch"` // detected from origin/HEAD when empty
	App           []string        `toml:"app"`            // app-label values routed here
	Requires      []string        `toml:"requires"`       // checkout dependencies (repo names)
	Hooks         Hooks           `toml:"hooks"`
	Tasks         map[string]Task `toml:"tasks"`
}

type Hooks struct {
	PostClone    []string `toml:"post_clone"`
	PostWorktree []string `toml:"post_worktree"`
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
	Branch     string   `toml:"branch"`
	ChildTitle string   `toml:"child_title"`
	ChildBody  string   `toml:"child_body"`
	Link       []string `toml:"link"`
	MR         MRConv   `toml:"mr"`
}

type MRConv struct {
	Draft   bool   `toml:"draft"`
	Closes  string `toml:"closes"`
	Mention string `toml:"mention"`
}

type Workspace struct {
	Layout string `toml:"layout"`
	Run    string `toml:"run"` // group name
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
