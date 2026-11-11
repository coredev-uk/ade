package workspace

import (
	"context"
	"fmt"
	"strings"
)

// RestartNode is the node a restart of this row should name: for a blocked
// instance that is the requirement whose death blocked it (restarting the
// blocked service alone would start it against a dead requirement), otherwise
// the instance's own node.
func (r Row) RestartNode() string {
	if r.BlockedBy != "" {
		return r.BlockedBy
	}
	return r.Node
}

// Restart stops the instance of node in plan p together with everything in
// the plan that transitively requires it (dependents first, newest first),
// then starts them again in dependency order, so the dependents come back
// ready against the fresh requirement. Instances elsewhere in the plan are
// left running; one that is not running makes the restarted nodes that need
// it report blocked.
//
// Holds are kept: an instance other workspaces (or a foreground `ade ws up`)
// were holding stays held by them (dropping the row drops its holds, so they
// are re-recorded under the instance lock as it starts). Only an instance
// nobody held gets o.Holder, which is required. The results are those of the restarted instances, in
// plan order. Dependents that live in another workspace's plan are not part of
// p and are not touched; they report blocked while the node is down and are
// restarted from their own workspace.
func (m *Manager) Restart(ctx context.Context, p *Plan, node string, o UpOptions) ([]Result, error) {
	if o.Holder == "" {
		return nil, fmt.Errorf("workspace: Restart needs a holder")
	}
	target, ok := p.Instance(node)
	if !ok {
		return nil, fmt.Errorf("workspace %q has no node %q (nodes: %s)", p.Workspace, node, strings.Join(planNodes(p), ", "))
	}
	subtree := map[string]bool{target.ID: true}
	for _, in := range p.Instances { // start order: a dependent always follows its requirement
		for _, req := range in.Requires {
			if subtree[req] {
				subtree[in.ID] = true
			}
		}
	}

	o.restore, o.preset = map[string][]holdRec{}, map[string]Result{}
	for i := len(p.Instances) - 1; i >= 0; i-- { // dependents first
		in := p.Instances[i]
		if !subtree[in.ID] {
			o.preset[in.ID] = m.standing(in)
			continue
		}
		holds, err := m.stopInstance(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if len(holds) > 0 {
			o.restore[in.ID] = holds
		}
	}

	all, err := m.start(ctx, p, o)
	var out []Result
	for i, in := range p.Instances {
		if subtree[in.ID] {
			out = append(out, all[i])
		}
	}
	return out, err
}

func planNodes(p *Plan) []string {
	names := make([]string, len(p.Instances))
	for i, in := range p.Instances {
		names[i] = in.Node.ID
	}
	return names
}

// holdRec is one row of ws_holds.
type holdRec struct {
	holder string
	pid    int
	pstart int64
}

// stopInstance stops id under its lock and drops its row, returning the holds
// the drop took with it (the row owns them).
func (m *Manager) stopInstance(ctx context.Context, id string) ([]holdRec, error) {
	unlock, err := m.lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	r, err := m.get(id)
	if err != nil || r == nil {
		return nil, err
	}
	rows, err := m.db.QueryContext(ctx, `SELECT holder, pid, pstart FROM ws_holds WHERE instance = ?`, id)
	if err != nil {
		return nil, err
	}
	var holds []holdRec
	for rows.Next() {
		var h holdRec
		if err := rows.Scan(&h.holder, &h.pid, &h.pstart); err != nil {
			rows.Close()
			return nil, err
		}
		holds = append(holds, h)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	stopGroup(r.PID, r.pstart, m.Grace)
	return holds, m.drop(id)
}

// standing is the result of an instance a restart leaves alone: ready when its
// process still runs (a finished oneshot counts), blocked otherwise.
func (m *Manager) standing(in *Instance) Result {
	res := Result{ID: in.ID, Node: in.Node.ID, Kind: kindOf(in.Node), Tree: in.Dir, Port: in.Port, Log: in.Log, Outcome: OutcomeBlocked}
	row, err := m.get(in.ID)
	switch {
	case err != nil:
		res.Detail = err.Error()
	case row == nil:
		res.Detail = "not running"
	case row.Kind == "oneshot" && row.State == StateDone, row.Alive && row.State == StateReady:
		res.Outcome, res.PID, res.Shared = OutcomeReady, row.PID, true
	default:
		res.Detail = "not running"
	}
	return res
}

// RestartRow restarts the subtree of a Status row (what the dashboard's `r`
// does on a blocked service): it finds the workspace plan, from the row's
// holders, that contains the row's RestartNode and calls Restart with the
// worktree that holds it as the holder of anything that had no holder.
func (m *Manager) RestartRow(ctx context.Context, root string, r Row, o UpOptions) ([]Result, error) {
	node := r.RestartNode()
	var lastErr error
	for _, h := range append(append([]string(nil), r.Holders...), r.Tree) {
		tree, _, _ := strings.Cut(h, "#")
		loc, err := Locate(root, tree)
		if err != nil {
			lastErr = err
			continue
		}
		for _, ws := range workspaceNames(loc) {
			plan, err := m.Plan(ctx, loc, PlanOptions{Workspace: ws})
			if err != nil {
				lastErr = err
				continue
			}
			if _, ok := plan.Instance(node); !ok {
				continue
			}
			if o.Holder == "" {
				o.Holder = loc.Tree
			}
			return m.Restart(ctx, plan, node, o)
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no workspace of the holders of %s runs node %s", r.ID, node)
}

// workspaceNames lists the manifest's workspaces, the default one first.
func workspaceNames(loc Location) []string {
	names := []string{DefaultWorkspace}
	for n := range loc.Manifest.Workspaces {
		if n != DefaultWorkspace {
			names = append(names, n)
		}
	}
	return names
}
