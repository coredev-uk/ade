package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/project"
)

// Project-level runs: the services of a project started, stopped and looked
// at from outside any worktree (`ade services …`). Their instances are the same
// rows and holds the workspaces use, so a service a worktree's tab already runs
// is shared, and `ade ws status` shows them all.

// ProjectHolderPrefix starts the holder of a project-level run: "project:<name>".
const ProjectHolderPrefix = "project:"

// Holder names who keeps the instances of a run at this location up: the
// worktree for a workspace, "project:<name>" for a run of the whole project.
func (l Location) Holder() string {
	if l.Tree != "" {
		return l.Tree
	}
	return ProjectHolderPrefix + l.Project
}

// ProjectNames lists the projects cloned under root.
func ProjectNames(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), ".project.toml")); err == nil {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out, nil
}

// LocateProject loads the manifest of the project name under root. The result
// has no repo and no tree: nodes resolve to each repo's baseline (or a pin).
func LocateProject(root, name string) (Location, error) {
	absRoot, err := resolve(root)
	if err != nil {
		return Location{}, fmt.Errorf("projects root: %w", err)
	}
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return Location{}, fmt.Errorf("%q is not a project name", name)
	}
	dir := filepath.Join(absRoot, name)
	m, err := project.LoadManifest(dir)
	if err != nil {
		have, _ := ProjectNames(absRoot)
		return Location{}, fmt.Errorf("project %s: %w (projects: %s)", name, err, strings.Join(have, ", "))
	}
	return Location{Root: absRoot, Project: name, ProjectDir: dir, Manifest: m}, nil
}

// ProjectOf says which project cwd is inside, when it is under root.
func ProjectOf(root, cwd string) (string, bool) {
	absRoot, err := resolve(root)
	if err != nil {
		return "", false
	}
	abs, err := resolve(cwd)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(absRoot, abs)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", false
	}
	name, _, _ := strings.Cut(rel, string(filepath.Separator))
	if _, err := os.Stat(filepath.Join(absRoot, name, ".project.toml")); err != nil {
		return "", false
	}
	return name, true
}

// ResolveNodes turns what the user typed into node ids. A name is a node id
// (`api.dev`, `services.db`), the bare name of a service (`db`) or a group of
// the manifest, which stands for its members. No names selects every
// `services.*` node.
func ResolveNodes(m *config.Manifest, names []string) ([]string, error) {
	g := NewGraph(m)
	var out []string
	add := func(ids ...string) {
		for _, id := range ids {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	if len(names) == 0 {
		for _, id := range g.IDs() {
			if strings.HasPrefix(id, ServicesPrefix+".") {
				add(id)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("project %s declares no [services.*]: name the nodes (<repo>.<task>, services.<name>) or a group", m.Name)
		}
		return out, nil
	}
	for _, name := range names {
		switch members, isGroup := m.Groups[name]; {
		case hasNode(g, name):
			add(name)
		case hasNode(g, ServicesPrefix+"."+name):
			add(ServicesPrefix + "." + name)
		case isGroup:
			add(members...)
		default:
			groups := make([]string, 0, len(m.Groups))
			for gn := range m.Groups {
				groups = append(groups, gn)
			}
			slices.Sort(groups)
			return nil, fmt.Errorf("no node or group %q in project %s (nodes: %s; groups: %s)", name, m.Name, strings.Join(g.IDs(), ", "), strings.Join(groups, ", "))
		}
	}
	return out, nil
}

func hasNode(g *Graph, id string) bool {
	_, ok := g.Node(id)
	return ok
}

// ServiceStatus is one node of a project: whether it runs, and where.
type ServiceStatus struct {
	Node string `json:"node"`
	// Kind is service or oneshot; empty for a node the manifest no longer declares.
	Kind     string `json:"kind,omitempty"`
	Declared bool   `json:"declared"`
	// State is the best state of its instances (ready, starting, blocked,
	// done, dead), or stopped when it has none.
	State string `json:"state"`
	// Instances are its instances in every worktree of the project.
	Instances []Row `json:"instances"`
}

// stateRank orders instance states, best first.
var stateRank = []string{StateReady, StateDone, StateStarting, StateBlocked, "dead"}

// RowState is the state of a row as people read it: dead when its process has gone.
func RowState(r Row) string {
	if !r.Alive && r.Kind != "oneshot" {
		return "dead"
	}
	return r.State
}

// ProjectStatus lists the nodes of a project with their instances in every
// worktree. nodes selects some; without any it lists every node that has an
// instance and every declared `services.*` node (running or not).
func (m *Manager) ProjectStatus(ctx context.Context, loc Location, nodes []string) ([]ServiceStatus, error) {
	rows, err := m.Status(ctx)
	if err != nil {
		return nil, err
	}
	g := NewGraph(loc.Manifest)
	byNode := map[string]*ServiceStatus{}
	entry := func(id string) *ServiceStatus {
		if s, ok := byNode[id]; ok {
			return s
		}
		s := &ServiceStatus{Node: id, State: "stopped", Instances: []Row{}}
		if n, ok := g.Node(id); ok {
			s.Declared, s.Kind = true, kindOf(n)
		}
		byNode[id] = s
		return s
	}
	if len(nodes) == 0 {
		for _, id := range g.IDs() {
			if strings.HasPrefix(id, ServicesPrefix+".") {
				entry(id)
			}
		}
	}
	for _, id := range nodes {
		entry(id)
	}
	for _, r := range rows {
		if r.Project != loc.Project || len(nodes) > 0 && !slices.Contains(nodes, r.Node) {
			continue
		}
		s := entry(r.Node)
		s.Instances = append(s.Instances, r)
	}
	out := make([]ServiceStatus, 0, len(byNode))
	for _, id := range slices.Sorted(maps.Keys(byNode)) {
		s := byNode[id]
		best := len(stateRank)
		for _, r := range s.Instances {
			if i := slices.Index(stateRank, RowState(r)); i >= 0 && i < best {
				best, s.State = i, stateRank[i]
			}
		}
		out = append(out, *s)
	}
	return out, nil
}

// DownOptions selects what Down stops.
type DownOptions struct {
	// Holder is the project-level run whose hold is released.
	Holder string
	// Nodes limits Down to instances of these node ids; empty means every
	// instance of the project.
	Nodes []string
	// Force also drops the holds of workspaces and stops `keep` instances: the
	// workspaces' panes then show their service exited.
	Force bool
}

// StillHeld is an instance Down left running, and who keeps it.
type StillHeld struct {
	ID      string   `json:"id"`
	Node    string   `json:"node"`
	Tree    string   `json:"tree"`
	Holders []string `json:"holders,omitempty"`
}

// DownResult is what Down did.
type DownResult struct {
	Stopped   []Stopped   `json:"stopped"`
	StillHeld []StillHeld `json:"still_held"`
}

// Down releases the project-level hold on the project's instances and stops
// those nobody else holds, dependents before requirements. Instances a
// worktree's workspace still holds keep running and are reported in
// StillHeld, unless o.Force.
func (m *Manager) Down(ctx context.Context, project string, o DownOptions) (DownResult, error) {
	res := DownResult{Stopped: []Stopped{}, StillHeld: []StillHeld{}}
	rows, err := m.Status(ctx)
	if err != nil {
		return res, err
	}
	var ids []string
	for _, r := range rows {
		if r.Project != project || len(o.Nodes) > 0 && !slices.Contains(o.Nodes, r.Node) {
			continue
		}
		ids = append(ids, r.ID)
		if o.Force {
			_, err = m.db.ExecContext(ctx, `DELETE FROM ws_holds WHERE instance = ?`, r.ID)
		} else {
			_, err = m.db.ExecContext(ctx, `DELETE FROM ws_holds WHERE instance = ? AND (holder = ? OR substr(holder, 1, ?) = ?)`,
				r.ID, o.Holder, len(o.Holder)+1, o.Holder+"#")
		}
		if err != nil {
			return res, err
		}
	}
	stopped, err := m.reap(ctx, ids, o.Force)
	if err != nil {
		return res, err
	}
	if stopped != nil {
		res.Stopped = stopped
	}
	for _, id := range ids {
		r, err := m.get(id)
		if err != nil {
			return res, err
		}
		if r == nil {
			continue
		}
		holders, err := m.holders(id)
		if err != nil {
			return res, err
		}
		res.StillHeld = append(res.StillHeld, StillHeld{ID: r.ID, Node: r.Node, Tree: r.Tree, Holders: holders})
	}
	return res, nil
}

// ProjectTabOptions tunes OpenProjectServices.
type ProjectTabOptions struct {
	Trees TreeOptions
	// Session targets another zellij session than the current one (tests).
	Session string
}

// OpenProjectServices opens the project's standalone services tab, `<project>
// · services`, in the zellij session: the install step, then one pane per
// node, each waiting for the install and for what it requires, exactly as in
// a workspace's services tab, but over the project's baseline trees. The tab
// is focused; one that exists is focused instead of created again. nodes are
// ids (ResolveNodes). Outside zellij nothing opens and Hint says what to run.
func OpenProjectServices(ctx context.Context, a *app.App, projectName string, nodes []string, o ProjectTabOptions) (OpenResult, error) {
	loc, err := LocateProject(a.Root, projectName)
	if err != nil {
		return OpenResult{}, err
	}
	mgr, err := NewManager(a)
	if err != nil {
		return OpenResult{}, err
	}
	plan, err := mgr.Plan(ctx, loc, PlanOptions{Nodes: nodes, Trees: o.Trees})
	if err != nil {
		return OpenResult{}, err
	}
	ade, err := os.Executable()
	if err != nil {
		return OpenResult{}, fmt.Errorf("locate ade executable: %w", err)
	}
	spec := ProjectServicesSpec(loc, plan, ade, nodes, o.Trees, len(mgr.InstallTargets(loc, plan, true)) > 0)
	res := OpenResult{Tab: spec.ServicesTab(), Layout: "services"}
	hostKind, err := chooseHost(a.Config.Host.Mode, os.Getenv)
	if err != nil {
		return res, err
	}
	res.Host = hostKind
	if hostKind != HostZellij {
		res.Hint = "ade services up --project " + loc.Project
		return res, nil
	}
	session := o.Session
	if session == "" {
		session = currentSession(os.Getenv)
	}
	return openProjectTab(ctx, runZellij, a, loc, spec, session, res)
}

// openProjectTab writes the services layout and opens its tab in session.
func openProjectTab(ctx context.Context, run zellijRunner, a *app.App, loc Location, spec LayoutSpec, session string, res OpenResult) (OpenResult, error) {
	res.Session = session
	kdl, err := BuildServicesLayout(spec)
	if err != nil {
		return res, err
	}
	dir := filepath.Join(a.Paths.CacheDir, "ws", "layouts")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return res, err
	}
	file := filepath.Join(dir, slugOf(loc.ProjectDir+"#"+res.Tab)+".kdl")
	if err = os.WriteFile(file, []byte(kdl), 0o600); err != nil {
		return res, err
	}
	res.LayoutFile = file
	res.Created, _, err = openTab(ctx, run, session, res.Tab, file, tabLayout{})
	entry := audit.Entry{
		Action: "workspace.open", Target: loc.ProjectDir, Project: loc.Project, Outcome: "ok",
		Params: map[string]any{"host": HostZellij, "layout": res.Layout, "tab": res.Tab},
	}
	if err != nil {
		entry.Outcome, entry.Error = "error", err.Error()
	}
	if aerr := a.Audit.Append(entry); aerr != nil && err == nil {
		err = aerr
	}
	return res, err
}

// ProjectServicesSpec is the layout input of a project's services tab: panes
// run `ade task run <node> --project <name>`, so nothing in it needs a worktree.
func ProjectServicesSpec(loc Location, plan *Plan, ade string, nodes []string, trees TreeOptions, install bool) LayoutSpec {
	runArgs := appendTreeArgs(nil, trees)
	spec := LayoutSpec{Tab: loc.Project, Dir: loc.ProjectDir, Tools: Tools{Ade: ade}, BackgroundServices: true}
	inRoots := map[string]bool{}
	for _, r := range plan.Roots {
		inRoots[r] = true
	}
	for _, in := range plan.Instances {
		if in.Node.Oneshot() && !inRoots[in.Node.ID] {
			continue // runs as part of its dependents' `task run`
		}
		args := []string{"task", "run", in.Node.ID, "--project", loc.Project}
		args = append(args, runArgs...)
		args = append(args, "--after-install", "--wait-requires")
		spec.Services = append(spec.Services, Pane{Name: in.Node.ID, Args: args})
	}
	if install {
		args := append([]string{"services", "install"}, nodes...)
		args = append(args, "--project", loc.Project)
		spec.Install = Pane{Name: "install", Args: append(args, runArgs...)}
	}
	return spec
}
