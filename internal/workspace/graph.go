// Package workspace turns a project manifest into something running: the
// checkout graph (which tree each required repo uses), the run graph (nodes
// started in dependency order with ports, env, readiness and shared
// instances), zellij layouts, and the VS Code task export (PLAN 2.1, 6.6, 6.7).
package workspace

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
)

// ServicesPrefix owns the ids of manifest services: "services.db".
const ServicesPrefix = "services"

// Node is one task or service of the run graph.
type Node struct {
	ID       string // "<repo>.<task>" or "services.<name>"
	Repo     string // owning repo; empty for services
	Name     string
	Task     config.Task
	Requires []string // node ids as written in the manifest
}

// Oneshot reports whether the node must exit 0 instead of staying up.
func (n *Node) Oneshot() bool { return n.Task.Kind == "oneshot" }

// Graph is the run graph of one manifest.
type Graph struct {
	m     *config.Manifest
	nodes map[string]*Node
	ids   []string // sorted
}

// NewGraph indexes the tasks and services of m.
func NewGraph(m *config.Manifest) *Graph {
	g := &Graph{m: m, nodes: map[string]*Node{}}
	for repo, r := range m.Repos {
		for name, t := range r.Tasks {
			g.add(&Node{ID: repo + "." + name, Repo: repo, Name: name, Task: t, Requires: t.Requires})
		}
	}
	for name, t := range m.Services {
		g.add(&Node{ID: ServicesPrefix + "." + name, Name: name, Task: t, Requires: t.Requires})
	}
	g.ids = slices.Sorted(maps.Keys(g.nodes))
	return g
}

func (g *Graph) add(n *Node) { g.nodes[n.ID] = n }

// Node returns the node with the given id.
func (g *Graph) Node(id string) (*Node, bool) {
	n, ok := g.nodes[id]
	return n, ok
}

// IDs lists every node id in sorted order.
func (g *Graph) IDs() []string { return g.ids }

// CycleError reports a dependency cycle by its path.
type CycleError struct{ Path []string }

func (e *CycleError) Error() string {
	return "dependency cycle: " + strings.Join(e.Path, " → ")
}

// Order returns the closure of roots (the roots and everything they
// transitively require) in start order: a node always follows its
// requirements, and ties break alphabetically so the order is deterministic.
// Cross-manifest references (shared/auth.dev) are not runnable from here.
func (g *Graph) Order(roots []string) ([]*Node, error) {
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var (
		order []*Node
		stack []string
		visit func(id string) error
	)
	visit = func(id string) error {
		n, ok := g.nodes[id]
		if !ok {
			if m, _, qualified := strings.Cut(id, "/"); qualified {
				return fmt.Errorf("%s: nodes of other manifests (%s) cannot be started from this workspace", id, m)
			}
			return fmt.Errorf("unknown node %q", id)
		}
		switch state[id] {
		case done:
			return nil
		case visiting:
			i := slices.Index(stack, id)
			return &CycleError{Path: append(slices.Clone(stack[i:]), id)}
		}
		state[id] = visiting
		stack = append(stack, id)
		for _, req := range slices.Sorted(slices.Values(n.Requires)) {
			if err := visit(req); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = done
		order = append(order, n)
		return nil
	}
	for _, id := range slices.Sorted(slices.Values(roots)) {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// Roots expands a workspace's run group (or a single node id) into node ids.
// run overrides the workspace's own group when non-empty.
func (g *Graph) Roots(workspace, run string) ([]string, error) {
	if run == "" {
		w, ok := g.m.Workspaces[workspace]
		if !ok {
			return nil, fmt.Errorf("manifest %q has no [workspaces.%s]", g.m.Name, workspace)
		}
		run = w.Run
		if run == "" {
			return nil, nil
		}
	}
	if members, ok := g.m.Groups[run]; ok {
		return slices.Clone(members), nil
	}
	if _, ok := g.nodes[run]; ok {
		return []string{run}, nil
	}
	return nil, fmt.Errorf("%q is neither a group nor a node of manifest %q", run, g.m.Name)
}
