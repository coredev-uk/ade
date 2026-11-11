package fake

import (
	"fmt"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// The in-memory model. Everything is a pointer into one state value guarded by
// Server.mu; handlers and mutators lock it, renderers assume it is held.

type user struct {
	ID       int64
	Username string
	Name     string
}

type project struct {
	ID   int64
	Path string
}

type milestone struct {
	ID    int64
	IID   int
	Title string
	State string
	Start string // YYYY-MM-DD
	Due   string
}

// iteration is group-level and Premium only. Cadence is the owning cadence's
// title; REST never reports it, GraphQL and the cadence search do.
type iteration struct {
	ID      int64
	IID     int
	Seq     int
	Cadence string
	Title   string // empty for cadence-generated iterations (rendered null)
	Start   time.Time
	Due     time.Time
}

type issue struct {
	ID        int64
	IID       int
	Project   *project
	Title     string
	Desc      string
	State     string // opened | closed
	Labels    []string
	Author    *user
	Assignees []*user
	Milestone *milestone
	Iteration *iteration // Premium only
	Weight    *int       // Premium only
	Epic      string     // Premium only
	Created   time.Time
	Updated   time.Time
	Closed    *time.Time
}

// link relates two issues. Type is from A's side; B sees the inverse.
type link struct {
	ID      int64
	A, B    *issue
	Type    string
	Created time.Time
}

type pipeline struct {
	ID      int64
	Status  string
	Updated time.Time
	Jobs    []*job
}

// job is one CI job. Retried marks a job a retry superseded: GitLab hides
// those unless include_retried is set.
type job struct {
	ID       int64
	Name     string
	Stage    string
	Status   string
	Duration float64
	Trace    string
	Retried  bool
}

type mr struct {
	ID        int64
	IID       int
	Project   *project
	Title     string
	Desc      string
	State     string // opened | merged | closed
	Draft     bool
	Source    string
	Target    string
	SHA       string
	Author    *user
	Assignees []*user
	Reviewers []*user
	Approved  []*user // approved_by
	Pipeline  *pipeline
	Closes    []*issue // issues the description closes
	Mentions  []*issue // issues the description merely references
	Labels    []string // set by create and quick actions
	Created   time.Time
	Updated   time.Time
	Merged    *time.Time

	Diff        *mrDiff        // latest version; nil until GitLab has computed one
	Drafts      []*draftNote   // the sandbox user's unpublished review comments
	Publishes   int            // bulk_publish calls that published something
	AutoMerge   bool           // merge_when_pipeline_succeeds
	Retries     int            // pipeline retries requested
	MergeParams map[string]any // parameters of the last merge request
}

// mrDiff is the latest diff version of an MR.
type mrDiff struct {
	Base, Start, Head string
	Files             []diffFile
}

type diffFile struct {
	Old, New string
	NewFile  bool
	Deleted  bool
	Renamed  bool
	Diff     string
}

// draftNote is a draft review comment. InReplyTo is the discussion it answers.
type draftNote struct {
	ID        int64
	Body      string
	Position  *position
	InReplyTo string
}

type note struct {
	ID         int64
	Author     *user
	Body       string
	System     bool
	Created    time.Time
	Resolvable bool
	Resolved   bool
	Position   *position
}

// position anchors a note to a diff line. Path is the new path; OldPath is set
// when the file was renamed (empty means the same as Path). The SHAs are the
// diff version the position was made against.
type position struct {
	Path              string
	OldPath           string
	NewLine           int
	OldLine           int
	Base, Start, Head string
}

type discussion struct {
	ID         string
	Individual bool
	Notes      []*note
}

type state struct {
	clock      time.Time
	nextID     int64
	me         *user
	users      []*user
	group      string
	groupID    int64
	projects   []*project
	milestones []*milestone
	iterations []*iteration
	issues     []*issue
	links      []*link
	mrs        []*mr
	threads    map[forge.Ref][]*discussion
	// approvalsRequired is the Premium rule threshold.
	approvalsRequired int
}

func (s *state) id() int64 {
	s.nextID++
	return s.nextID
}

func (s *state) findProject(idOrPath string) *project {
	for _, p := range s.projects {
		if p.Path == idOrPath || fmt.Sprint(p.ID) == idOrPath {
			return p
		}
	}
	return nil
}

func (s *state) hasGroup(idOrPath string) bool {
	return idOrPath == s.group || idOrPath == fmt.Sprint(s.groupID)
}

func (s *state) findIssue(p *project, iid int) *issue {
	for _, is := range s.issues {
		if is.Project == p && is.IID == iid {
			return is
		}
	}
	return nil
}

func (s *state) findMR(p *project, iid int) *mr {
	for _, m := range s.mrs {
		if m.Project == p && m.IID == iid {
			return m
		}
	}
	return nil
}

func (s *state) findUser(username string) *user {
	for _, u := range s.users {
		if u.Username == username {
			return u
		}
	}
	return nil
}

func (s *state) ref(p *project, iid int, k forge.Kind) forge.Ref {
	return forge.Ref{Project: p.Path, IID: iid, Kind: k}
}

// taskCounts reads Markdown checklists the way GitLab does for
// task_completion_status.
func taskCounts(desc string) (done, total int) {
	for line := range strings.SplitSeq(desc, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "- [x]"), strings.HasPrefix(l, "- [X]"):
			done++
			total++
		case strings.HasPrefix(l, "- [ ]"):
			total++
		}
	}
	return done, total
}
