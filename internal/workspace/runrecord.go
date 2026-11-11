package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/records"
)

// Run states recorded in run.md.
const (
	RunRunning  = "running"  // a supervising command is alive
	RunStopped  = "stopped"  // released
	RunDetached = "detached" // left running on purpose; `ade ws down` stops it
	RunFailed   = "failed"   // the graph did not come up
)

// RunRecord is the metadata of one workspace run: which instances served a
// worktree and on which ports. It never holds output, env or commands.
type RunRecord struct {
	ID        string
	State     string
	Project   string
	Repo      string
	Worktree  string
	Branch    string
	Workspace string
	Tracker   forge.Ref
	Started   time.Time
	Ended     time.Time
	Instances []Result
}

// Markdown renders run.md: frontmatter for tools, a table for people.
func (r RunRecord) Markdown() string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\nkind: workspace\nstate: %s\nitem: %s\nproject: %s\nrepo: %s\nworktree: %s\nbranch: %s\nworkspace: %s\nstarted: %s\n",
		r.ID, r.State, r.Tracker, r.Project, r.Repo, r.Worktree, r.Branch, r.Workspace, r.Started.UTC().Format(time.RFC3339))
	if !r.Ended.IsZero() {
		fmt.Fprintf(&b, "ended: %s\n", r.Ended.UTC().Format(time.RFC3339))
	}
	b.WriteString("---\n")
	fmt.Fprintf(&b, "# Workspace run %s\n\n%s on %s (%s)\n", r.ID, r.State, r.Repo+"/"+filepath.Base(r.Worktree), r.Workspace)
	if len(r.Instances) > 0 {
		b.WriteString("\n| node | outcome | port | pid | tree |\n|---|---|---|---|---|\n")
		for _, in := range r.Instances {
			outcome := string(in.Outcome)
			if in.Shared {
				outcome += " (shared)"
			}
			port, pid := "", ""
			if in.Port > 0 {
				port = strconv.Itoa(in.Port)
			}
			if in.PID > 0 {
				pid = strconv.Itoa(in.PID)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", in.Node, outcome, port, pid, in.Tree)
		}
	}
	return b.String()
}

// Recorder writes one workspace run into its work item's record directory
// (PLAN 4.2: runs/<id>/run.md). A nil Recorder does nothing: worktrees that no
// work item owns (a baseline `main/`) have no record to write to.
type Recorder struct {
	dir string
	rec RunRecord
}

// NewRecorder prepares the record of a run on the worktree owned by tracker
// (see ItemOf). Nothing is written until Update.
func NewRecorder(recordsDir, forgeName string, tracker forge.Ref, loc Location, workspace, branch string) *Recorder {
	if workspace == "" {
		workspace = DefaultWorkspace
	}
	id := ulid.Make().String()
	return &Recorder{
		dir: filepath.Join(records.New(recordsDir).ItemDir(forgeName, tracker), "runs", id),
		rec: RunRecord{
			ID: id, Project: loc.Project, Repo: loc.Repo, Worktree: loc.Tree, Branch: branch,
			Workspace: workspace, Tracker: tracker, Started: time.Now(),
		},
	}
}

// Path is the written run.md, or "" for a nil Recorder.
func (r *Recorder) Path() string {
	if r == nil {
		return ""
	}
	return filepath.Join(r.dir, "run.md")
}

// Update rewrites run.md with the state and instances.
func (r *Recorder) Update(state string, instances []Result) error {
	if r == nil {
		return nil
	}
	r.rec.State, r.rec.Instances = state, instances
	if state == RunStopped || state == RunFailed {
		r.rec.Ended = time.Now()
	}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return err
	}
	tmp := r.Path() + ".tmp"
	if err := os.WriteFile(tmp, []byte(r.rec.Markdown()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.Path())
}
