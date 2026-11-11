package workspace

import (
	"context"
	"crypto/sha1" //nolint:gosec // names a directory, not a security boundary
	"encoding/hex"
	"fmt"
	"maps"
	"net"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/readiness"
)

// DefaultWorkspace is the manifest [workspaces] entry used when none is named.
const DefaultWorkspace = "default"

// PlanOptions selects what a plan covers.
type PlanOptions struct {
	Workspace string // [workspaces.<name>]; default "default"
	Run       string // overrides the workspace's run group; a group or node id
	Nodes     []string
	Trees     TreeOptions
}

// Instance is one node bound to the tree it runs in, with everything
// resolved: port, env, command and readiness probe.
type Instance struct {
	ID       string // "<node>@<tree>"
	Node     *Node
	Tree     Tree   // the tree serving the node; services use the project directory
	Dir      string // working directory
	Port     int    // 0 when the node declares none
	State    string // directory for the node's data ({state})
	Log      string
	Cmd      string
	Env      map[string]string
	Ready    *readiness.Spec // nil: ready once started (services) or after exit 0 (oneshots)
	Requires []string        // instance ids
}

// Plan is a resolved run graph for one workspace.
type Plan struct {
	Loc       Location
	Workspace string
	Roots     []string // node ids the workspace asks for
	Trees     map[string]Tree
	Instances []*Instance // start order: requirements first
}

// Instance returns the planned instance of a node id.
func (p *Plan) Instance(node string) (*Instance, bool) {
	for _, in := range p.Instances {
		if in.Node.ID == node {
			return in, true
		}
	}
	return nil, false
}

// Plan resolves trees, allocates ports and expands templates for the nodes the
// workspace runs. It starts nothing; calling it twice yields the same ports.
func (m *Manager) Plan(ctx context.Context, loc Location, o PlanOptions) (*Plan, error) {
	g := NewGraph(loc.Manifest)
	ws := o.Workspace
	if ws == "" {
		ws = DefaultWorkspace
	}
	roots := o.Nodes
	if len(roots) == 0 {
		var err error
		if roots, err = g.Roots(ws, o.Run); err != nil {
			return nil, err
		}
	}
	nodes, err := g.Order(roots)
	if err != nil {
		return nil, err
	}
	tr := newTreeResolver(loc, o.Trees)
	p := &Plan{Loc: loc, Workspace: ws, Roots: roots, Trees: map[string]Tree{}}
	byNode := map[string]*Instance{}
	for _, n := range nodes {
		in, err := m.bind(ctx, loc, tr, n)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n.ID, err)
		}
		byNode[n.ID] = in
		p.Instances = append(p.Instances, in)
	}
	for _, n := range nodes {
		in := byNode[n.ID]
		for _, req := range n.Requires {
			in.Requires = append(in.Requires, byNode[req].ID)
		}
		if err := in.expand(ctx, tr, byNode); err != nil {
			return nil, fmt.Errorf("%s: %w", n.ID, err)
		}
	}
	for repo := range loc.Manifest.Repos {
		if t, ok := tr.done[repo]; ok {
			p.Trees[repo] = t
		}
	}
	return p, nil
}

// bind picks the tree, allocates the port and names the state and log files.
func (m *Manager) bind(ctx context.Context, loc Location, tr *treeResolver, n *Node) (*Instance, error) {
	in := &Instance{Node: n}
	if n.Repo == "" {
		in.Tree = Tree{Path: loc.ProjectDir, Source: SourceBaseline}
	} else {
		t, err := tr.tree(ctx, n.Repo)
		if err != nil {
			return nil, err
		}
		in.Tree = t
	}
	in.Dir = in.Tree.Path
	in.ID = n.ID + "@" + in.Tree.Path
	slug := slugOf(in.ID)
	in.State = filepath.Join(m.StateDir, slug)
	in.Log = filepath.Join(m.LogDir, slug+".log")

	switch {
	case n.Task.Port.Fixed > 0:
		in.Port = n.Task.Port.Fixed
	case n.Task.Port.Auto:
		held := m.live(in.ID)
		port, err := m.ports.Assign(in.ID, held)
		if err != nil {
			return nil, err
		}
		in.Port = port
	}
	return in, nil
}

// slugOf makes a file-system name for an instance id (or any other string):
// a readable prefix plus a hash of the whole.
func slugOf(id string) string {
	sum := sha1.Sum([]byte(id)) //nolint:gosec
	name, _, _ := strings.Cut(id, "@")
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			return r
		}
		return '_'
	}, name)
	if len(name) > 48 {
		name = name[:48]
	}
	return name + "-" + hex.EncodeToString(sum[:4])
}

var templateRE = regexp.MustCompile(`(\$?)\{([^{}]*)\}`)

// expand fills the instance's command, env and probe from the plan.
func (in *Instance) expand(ctx context.Context, tr *treeResolver, byNode map[string]*Instance) error {
	sub := func(s string) (string, error) { return in.substitute(ctx, s, tr, byNode) }
	var err error
	if in.Cmd, err = sub(in.Node.Task.Cmd); err != nil {
		return fmt.Errorf("cmd: %w", err)
	}
	in.Env = map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(in.Node.Task.Env)) {
		if in.Env[k], err = sub(in.Node.Task.Env[k]); err != nil {
			return fmt.Errorf("env.%s: %w", k, err)
		}
	}
	r := in.Node.Task.Ready
	var spec readiness.Spec
	switch {
	case r.HTTP != "":
		spec.Kind, spec.Target = readiness.HTTP, r.HTTP
	case r.TCP != "":
		spec.Kind, spec.Target = readiness.TCP, r.TCP
	case r.Log != "":
		spec.Kind, spec.Target = readiness.Log, r.Log
	default:
		return nil
	}
	spec.Timeout = r.Timeout.Duration
	if spec.Kind != readiness.Log { // a log regexp is not a template: braces are regexp quantifiers
		if spec.Target, err = sub(spec.Target); err != nil {
			return fmt.Errorf("ready.%s: %w", spec.Kind, err)
		}
	}
	if spec.Kind == readiness.TCP && !strings.Contains(spec.Target, ":") {
		spec.Target = net.JoinHostPort("localhost", spec.Target)
	}
	in.Ready = &spec
	return nil
}

// substitute expands {port}, {dir}, {state}, {<repo>.dir}, {<repo>.<task>.port}
// and {services.<name>.port}. ${VAR} stays shell syntax.
func (in *Instance) substitute(ctx context.Context, s string, tr *treeResolver, byNode map[string]*Instance) (string, error) {
	var firstErr error
	out := templateRE.ReplaceAllStringFunc(s, func(match string) string {
		sub := templateRE.FindStringSubmatch(match)
		if sub[1] == "$" {
			return match
		}
		v, err := in.lookup(ctx, sub[2], tr, byNode)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return v
	})
	return out, firstErr
}

func (in *Instance) lookup(ctx context.Context, ref string, tr *treeResolver, byNode map[string]*Instance) (string, error) {
	switch ref {
	case "port":
		if in.Port == 0 {
			return "", fmt.Errorf("{port}: %s declares no port", in.Node.ID)
		}
		return strconv.Itoa(in.Port), nil
	case "dir":
		return in.Dir, nil
	case "state":
		return in.State, nil
	}
	if strings.Contains(ref, "/") {
		return "", fmt.Errorf("{%s}: references into other manifests are not supported by the local runner", ref)
	}
	parts := strings.Split(ref, ".")
	switch {
	case len(parts) == 2 && parts[1] == "dir" && parts[0] != ServicesPrefix:
		t, err := tr.tree(ctx, parts[0])
		if err != nil {
			return "", fmt.Errorf("{%s}: %w", ref, err)
		}
		return t.Path, nil
	case len(parts) == 3 && parts[2] == "port":
		target, ok := byNode[parts[0]+"."+parts[1]]
		if !ok {
			return "", fmt.Errorf("{%s}: %s.%s is not part of this workspace's run graph", ref, parts[0], parts[1])
		}
		if target.Port == 0 {
			return "", fmt.Errorf("{%s}: %s declares no port", ref, target.Node.ID)
		}
		return strconv.Itoa(target.Port), nil
	}
	return "", fmt.Errorf("unknown template variable {%s}", ref)
}
