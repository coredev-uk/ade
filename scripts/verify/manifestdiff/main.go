// Command manifestdiff compares a generated manifest with a hand-written one
// on what `ade project bootstrap` is expected to get right, and reports what
// it is not expected to know. Used by scripts/verify/bootstrap.sh.
//
//	manifestdiff <expected.ade.toml> <generated.ade.toml>
//
// Exit 1 when a compared field differs. Fields compared: tracker project,
// repos by forge path with their app values, label patterns, the workflow
// stage map, sync and done value, the iteration source and the weight source.
// Name, forge key, board, tasks, groups, workspaces, conventions and repo
// requires are listed as differences without failing: they are choices a
// forge cannot reveal.
package main

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
)

func load(path string) *config.Manifest {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "manifestdiff:", err)
		os.Exit(2)
	}
	m, err := config.ParseManifest(b, path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "manifestdiff:", err)
		os.Exit(2)
	}
	return m
}

var failed bool

// same reports a compared field: equal, or a failure.
func same(field string, want, got any) {
	if fmt.Sprint(want) == fmt.Sprint(got) {
		fmt.Printf("  same  %-40s %v\n", field, got)
		return
	}
	failed = true
	fmt.Printf("  DIFF  %-40s want %v, got %v\n", field, want, got)
}

// info reports a field the generator is not expected to match.
func info(field string, want, got any) {
	if fmt.Sprint(want) == fmt.Sprint(got) {
		fmt.Printf("  same  %-40s %v\n", field, got)
		return
	}
	fmt.Printf("  info  %-40s hand-written %v, generated %v\n", field, want, got)
}

func byPath(m *config.Manifest) map[string]config.Repo {
	out := map[string]config.Repo{}
	for _, r := range m.Repos {
		out[r.Path] = r
	}
	return out
}

func keyOf(m *config.Manifest, path string) string {
	for k, r := range m.Repos {
		if r.Path == path {
			return k
		}
	}
	return ""
}

func apps(r config.Repo) []string {
	a := slices.Clone(r.App)
	sort.Strings(a)
	return a
}

func taskNames(r config.Repo) []string { return slices.Sorted(maps.Keys(r.Tasks)) }

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: manifestdiff <expected.ade.toml> <generated.ade.toml>")
		os.Exit(2)
	}
	want, got := load(os.Args[1]), load(os.Args[2])

	fmt.Println("compared (must match):")
	same("tracker.project", want.Tracker.Project, got.Tracker.Project)
	wr, gr := byPath(want), byPath(got)
	same("repo paths", slices.Sorted(maps.Keys(wr)), slices.Sorted(maps.Keys(gr)))
	for _, p := range slices.Sorted(maps.Keys(wr)) {
		if g, ok := gr[p]; ok {
			same("repos["+p+"].app", apps(wr[p]), apps(g))
		}
	}
	same("labels.type.pattern", want.Labels.Type.Pattern, got.Labels.Type.Pattern)
	same("labels.app.pattern", want.Labels.App.Pattern, got.Labels.App.Pattern)
	same("labels.workflow.pattern", want.Labels.Workflow.Pattern, got.Labels.Workflow.Pattern)
	same("labels.workflow.stage", sortedMap(want.Labels.Workflow.Stage), sortedMap(got.Labels.Workflow.Stage))
	same("labels.workflow.done", want.Labels.Workflow.Done, got.Labels.Workflow.Done)
	same("labels.workflow.sync", orAuto(want.Labels.Workflow.Sync), orAuto(got.Labels.Workflow.Sync))
	same("tracker.iterations.source", want.Tracker.Iterations.Source, got.Tracker.Iterations.Source)
	same("tracker.iterations.cadence", want.Tracker.Iterations.Cadence, got.Tracker.Iterations.Cadence)
	same("tracker.weight", want.Tracker.Weight, got.Tracker.Weight)

	fmt.Println("not compared (choices a forge cannot reveal):")
	info("name", want.Name, got.Name)
	info("forge", want.Forge, got.Forge)
	info("tracker.board", want.Tracker.Board, got.Tracker.Board)
	for _, p := range slices.Sorted(maps.Keys(wr)) {
		g, ok := gr[p]
		if !ok {
			continue
		}
		info("repos["+p+"] key", keyOf(want, p), keyOf(got, p))
		info("repos["+p+"].requires", wr[p].Requires, g.Requires)
		info("repos["+p+"].tasks", taskNames(wr[p]), taskNames(g))
	}
	info("groups", sortedGroups(want.Groups), sortedGroups(got.Groups))
	info("workspaces", len(want.Workspaces), len(got.Workspaces))
	info("conventions.child_issue", want.Conventions.ChildIssue, got.Conventions.ChildIssue)
	info("conventions.branch", want.Conventions.Branch, got.Conventions.Branch)
	info("conventions.mr", mrString(want.Conventions.MR), mrString(got.Conventions.MR))

	if failed {
		fmt.Println("RESULT: the generated manifest differs on a compared field")
		os.Exit(1)
	}
	fmt.Println("RESULT: equivalent on every compared field")
}

func orAuto(s string) string {
	if s == "" {
		return "auto"
	}
	return s
}

func sortedMap(m map[string]string) string {
	keys := slices.Sorted(maps.Keys(m))
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func sortedGroups(g map[string][]string) string {
	keys := slices.Sorted(maps.Keys(g))
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + strings.Join(g[k], "+")
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// mrString renders the MR convention without pointer addresses; "unset" is
// what the key says when the manifest does not.
func mrString(c config.MRConv) string {
	draft := "unset"
	if c.Draft != nil {
		draft = fmt.Sprint(*c.Draft)
	}
	return fmt.Sprintf("{create=%q template=%q draft=%s closes=%q mention=%q}", c.Create, c.Template, draft, c.Closes, c.Mention)
}
