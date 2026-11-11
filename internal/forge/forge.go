// Package forge is ADE's forge-neutral model of issues, merge requests and
// planning data, and the interface each forge driver (gitlab, github)
// implements. Drivers translate; they never derive stage or court (that is
// package domain's job).
package forge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Kind distinguishes issue and merge-request references.
type Kind int

const (
	KindIssue Kind = iota
	KindMR
)

// Ref identifies an issue ("group/proj#12") or MR ("group/proj!3").
type Ref struct {
	Project string `json:"project"`
	IID     int    `json:"iid"`
	Kind    Kind   `json:"kind"`
}

// MarshalText makes a Ref read as "group/proj#12" in JSON and map keys,
// which is what scripts consuming `--json` expect. A zero Ref is "".
func (r Ref) MarshalText() ([]byte, error) {
	if r.IsZero() {
		return nil, nil
	}
	return []byte(r.String()), nil
}

func (r *Ref) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*r = Ref{}
		return nil
	}
	p, err := ParseRef(string(b))
	*r = p
	return err
}

func (r Ref) String() string {
	sep := "#"
	if r.Kind == KindMR {
		sep = "!"
	}
	return r.Project + sep + strconv.Itoa(r.IID)
}

func (r Ref) IsZero() bool { return r.Project == "" && r.IID == 0 }

// ParseRef parses "group/proj#12" or "group/proj!3".
func ParseRef(s string) (Ref, error) {
	i := strings.LastIndexAny(s, "#!")
	if i <= 0 || i == len(s)-1 {
		return Ref{}, fmt.Errorf("invalid ref %q: want group/project#N or group/project!N", s)
	}
	n, err := strconv.Atoi(s[i+1:])
	if err != nil || n <= 0 {
		return Ref{}, fmt.Errorf("invalid ref %q: bad number", s)
	}
	k := KindIssue
	if s[i] == '!' {
		k = KindMR
	}
	return Ref{Project: s[:i], IID: n, Kind: k}, nil
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name,omitempty"`
}

// Date is a calendar date (no time zone), as GitLab reports start/due dates.
type Date struct{ time.Time }

func NewDate(y int, m time.Month, d int) Date { return Date{time.Date(y, m, d, 0, 0, 0, 0, time.UTC)} }

func ParseDate(s string) (Date, error) {
	t, err := time.Parse("2006-01-02", s)
	return Date{t}, err
}

func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return d.Format("2006-01-02")
}

// MarshalJSON and UnmarshalJSON override the promoted time.Time methods, which
// would write RFC 3339 and reject "2006-01-02".
func (d Date) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(d.String())), nil }

func (d *Date) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		if string(b) == "null" {
			*d = Date{}
			return nil
		}
		return fmt.Errorf("date: %w", err)
	}
	return d.UnmarshalText([]byte(s))
}
func (d Date) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func (d *Date) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*d = Date{}
		return nil
	}
	p, err := ParseDate(string(b))
	*d = p
	return err
}

type Milestone struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	State string `json:"state"` // active | closed
	Start Date   `json:"start,omitzero"`
	Due   Date   `json:"due,omitzero"`
}

// Iteration is one sprint. Title may be empty for cadence-generated iterations.
type Iteration struct {
	ID      int64  `json:"id"`
	IID     int    `json:"iid,omitempty"`
	Title   string `json:"title,omitempty"`
	Cadence string `json:"cadence,omitempty"`
	State   string `json:"state"` // upcoming | current | closed
	Start   Date   `json:"start"`
	Due     Date   `json:"due"` // last day of the iteration
}

type TaskCount struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

type Issue struct {
	Ref         Ref        `json:"ref"`
	ID          int64      `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	State       string     `json:"state"` // opened | closed
	WebURL      string     `json:"web_url,omitempty"`
	Labels      []string   `json:"labels,omitempty"`
	Author      User       `json:"author"`
	Assignees   []User     `json:"assignees,omitempty"`
	Milestone   *Milestone `json:"milestone,omitempty"`
	Iteration   *Iteration `json:"iteration,omitempty"`
	Weight      *int       `json:"weight,omitempty"` // nil: unweighted (never 0)
	Tasks       TaskCount  `json:"tasks"`
	Epic        string     `json:"epic,omitempty"` // parent epic title, when readable
	// Comments counts human comments (system notes excluded); nil when the
	// listing did not say. Zero lets callers skip fetching the discussion.
	Comments  *int       `json:"comments,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

// Link is an issue-to-issue relation. Type is the forge's link_type
// (relates_to, blocks, is_blocked_by) or "sub_issue"/"parent" for hierarchy.
type Link struct {
	Ref  Ref    `json:"ref"`
	Type string `json:"type"`
}

type Pipeline struct {
	ID        int64     `json:"id"`
	Status    string    `json:"status"` // success failed running pending canceled skipped manual created
	WebURL    string    `json:"web_url,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Approvals: Required is -1 when the forge cannot tell (Free tier).
type Approvals struct {
	Required   int    `json:"required"`
	ApprovedBy []User `json:"approved_by,omitempty"`
}

// ThreadState is what a sync remembers of one discussion: the author of each
// human note, oldest first. Comparing two syncs shows new threads and replies,
// and who wrote them, without keeping the text.
type ThreadState struct {
	ID      string   `json:"id"`
	Authors []string `json:"authors"`
}

// ThreadStates summarises discussions for MR.Threads. Discussions made only
// of system notes ("added 1 commit") are not conversation and are skipped. The
// result is never nil, so that "no threads" differs from "not read".
func ThreadStates(ds []Discussion) []ThreadState {
	out := []ThreadState{}
	for _, d := range ds {
		t := ThreadState{ID: d.ID}
		for _, n := range d.Notes {
			if !n.System {
				t.Authors = append(t.Authors, n.Author.Username)
			}
		}
		if len(t.Authors) > 0 {
			out = append(out, t)
		}
	}
	return out
}

type MR struct {
	Ref          Ref       `json:"ref"`
	ID           int64     `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description,omitempty"`
	State        string    `json:"state"` // opened | merged | closed
	Draft        bool      `json:"draft"`
	SourceBranch string    `json:"source_branch"`
	TargetBranch string    `json:"target_branch"`
	HeadSHA      string    `json:"head_sha,omitempty"`
	WebURL       string    `json:"web_url,omitempty"`
	Author       User      `json:"author"`
	Assignees    []User    `json:"assignees,omitempty"`
	Reviewers    []User    `json:"reviewers,omitempty"`
	Pipeline     *Pipeline `json:"pipeline,omitempty"`
	Approvals    Approvals `json:"approvals"`
	Unresolved   int       `json:"unresolved"` // unresolved resolvable threads
	Resolved     int       `json:"resolved"`
	// Threads lists every conversation with its note authors; nil when the
	// forge's list view did not read discussions (see ThreadStates).
	Threads []ThreadState `json:"threads"`
	// ChangesRequestedBy are the reviewers whose standing verdict is "changes
	// requested" (GitLab reviewer state requested_changes, GitHub review
	// CHANGES_REQUESTED).
	ChangesRequestedBy []User `json:"changes_requested_by,omitempty"`
	// MergedBy is the username that merged the MR, when the forge says.
	MergedBy       string     `json:"merged_by,omitempty"`
	HasConflicts   bool       `json:"has_conflicts,omitempty"`
	DetailedStatus string     `json:"detailed_status,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
	MergedAt       *time.Time `json:"merged_at,omitempty"`
}

type Note struct {
	ID        int64     `json:"id"`
	Author    User      `json:"author"`
	Body      string    `json:"body"`
	System    bool      `json:"system,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Position anchors a diff thread to a line.
type Position struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	NewLine int    `json:"new_line,omitempty"`
	OldLine int    `json:"old_line,omitempty"`
}

type Discussion struct {
	ID         string    `json:"id"`
	Resolvable bool      `json:"resolvable"`
	Resolved   bool      `json:"resolved"`
	Position   *Position `json:"position,omitempty"`
	Notes      []Note    `json:"notes"`
}

// Caps records which gated features this forge instance offers. Features are
// probed, never inferred from a tier name.
type Caps struct {
	Iterations    bool      `json:"iterations"`
	Cadences      bool      `json:"cadences"`
	Weight        bool      `json:"weight"`
	ApprovalRules bool      `json:"approval_rules"`
	BlocksLinks   bool      `json:"blocks_links"`
	Epics         bool      `json:"epics"`
	ProbedAt      time.Time `json:"probed_at"`
}

type IssueQuery struct {
	Project      string // required
	Assignee     string // username; empty = any
	State        string // opened | closed | all (default opened)
	Milestone    string // milestone title
	IterationID  int64
	Labels       []string // all must match
	UpdatedAfter time.Time
}

type MRQuery struct {
	Project      string // required
	Author       string
	Assignee     string
	Reviewer     string
	State        string // opened | merged | closed | all (default opened)
	SourceBranch string
	UpdatedAfter time.Time
}

// Forge is the read side every driver implements (Phase 1). Write methods
// arrive with the phases that need them as separate interfaces.
type Forge interface {
	Kind() string // gitlab | github
	Me(ctx context.Context) (User, error)
	Capabilities(ctx context.Context, group string) (Caps, error)
	ListIssues(ctx context.Context, q IssueQuery) ([]Issue, error)
	Issue(ctx context.Context, ref Ref) (Issue, error)
	Links(ctx context.Context, ref Ref) ([]Link, error)
	ListMRs(ctx context.Context, q MRQuery) ([]MR, error)
	// MR returns one merge request with pipeline, approvals and thread counts.
	MR(ctx context.Context, ref Ref) (MR, error)
	// MRsForIssue returns MRs that reference or close the issue.
	MRsForIssue(ctx context.Context, ref Ref) ([]MR, error)
	Discussions(ctx context.Context, ref Ref) ([]Discussion, error)
	// Milestones lists a group's (or project's) milestones incl. closed.
	Milestones(ctx context.Context, groupOrProject string) ([]Milestone, error)
	// Iterations lists a group's iterations, optionally for one cadence title.
	// Returns ErrUnavailable where the forge has no iterations.
	Iterations(ctx context.Context, group, cadence string) ([]Iteration, error)
}

// DiffFile is one file of a merge request's diff. Paths are those of the
// latest version; for a rename OldPath differs from NewPath, and for a
// deleted file NewPath still names the file. Diff is the unified hunk text
// (from the first "@@" header; no ---/+++ preamble), empty when the forge
// withheld it (collapsed or too large).
type DiffFile struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	NewFile bool   `json:"new_file,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
	Renamed bool   `json:"renamed,omitempty"`
	Diff    string `json:"diff,omitempty"`
}

// Diff is the latest version of a merge request's changes. The SHAs are what a
// comment position must quote to land on the version being reviewed.
type Diff struct {
	BaseSHA  string     `json:"base_sha"`
	StartSHA string     `json:"start_sha"`
	HeadSHA  string     `json:"head_sha"`
	Files    []DiffFile `json:"files"`
}

// DiffPosition anchors a new comment to a line of the latest version. Only
// NewLine set anchors an added line, only OldLine a removed line, both an
// unchanged line (their numbers may differ).
type DiffPosition struct {
	BaseSHA  string `json:"base_sha"`
	StartSHA string `json:"start_sha"`
	HeadSHA  string `json:"head_sha"`
	OldPath  string `json:"old_path"`
	NewPath  string `json:"new_path"`
	OldLine  int    `json:"old_line,omitempty"`
	NewLine  int    `json:"new_line,omitempty"`
}

// Job is one CI job of a pipeline.
type Job struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Stage    string  `json:"stage"`
	Status   string  `json:"status"` // success failed running pending canceled skipped manual created
	WebURL   string  `json:"web_url,omitempty"`
	Duration float64 `json:"duration,omitempty"` // seconds
}

// DraftNote is a not-yet-published review comment, visible only to its author.
// InReplyTo is the id of the discussion it answers, empty for a new thread.
type DraftNote struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	Position  *Position `json:"position,omitempty"`
	InReplyTo string    `json:"in_reply_to,omitempty"`
}

// MergeOpts controls a merge. SHA is the head the caller reviewed: the forge
// refuses the merge when the branch has moved.
type MergeOpts struct {
	SHA                string `json:"sha"`
	Squash             bool   `json:"squash,omitempty"`
	RemoveSourceBranch bool   `json:"remove_source_branch,omitempty"`
	AutoMerge          bool   `json:"auto_merge,omitempty"`
	Message            string `json:"message,omitempty"`
}

// MRWriter is the Phase 2 write side of a forge: the diff and CI reads the MR
// page needs, thread and approval writes, and review batching. Drivers
// implement it next to Forge.
type MRWriter interface {
	// MRDiff returns the latest version's SHAs and files, with renames resolved.
	MRDiff(ctx context.Context, ref Ref) (Diff, error)
	Jobs(ctx context.Context, project string, pipelineID int64) ([]Job, error)
	// JobLog returns the last tailBytes of the job's log (all of it when <= 0).
	JobLog(ctx context.Context, project string, jobID int64, tailBytes int) (string, error)
	// RetryPipeline retries the pipeline's failed jobs.
	RetryPipeline(ctx context.Context, project string, pipelineID int64) error
	Reply(ctx context.Context, mr Ref, discussionID, body string) (Note, error)
	Resolve(ctx context.Context, mr Ref, discussionID string, resolved bool) error
	// Comment starts a thread at once (not batched): on a diff line when pos
	// is set, otherwise on the MR overview.
	Comment(ctx context.Context, mr Ref, body string, pos *DiffPosition) (Discussion, error)
	// Approve approves the head sha; empty sha approves whatever is current.
	Approve(ctx context.Context, mr Ref, sha string) error
	Unapprove(ctx context.Context, mr Ref) error
	// SetDraft marks the MR draft or ready; a no-op when it already is.
	SetDraft(ctx context.Context, mr Ref, draft bool) error
	// Merge always pins o.SHA; AutoMerge merges when checks pass.
	Merge(ctx context.Context, mr Ref, o MergeOpts) error
	// AddDraftNote queues a comment for the next publish. pos anchors it to a
	// diff line; inReplyTo (a discussion id) makes it a reply instead.
	AddDraftNote(ctx context.Context, mr Ref, body string, pos *DiffPosition, inReplyTo string) (DraftNote, error)
	DraftNotes(ctx context.Context, mr Ref) ([]DraftNote, error)
	DeleteDraftNote(ctx context.Context, mr Ref, id int64) error
	// PublishDraftNotes publishes every draft note of the caller in one batch:
	// one notification for the MR's participants.
	PublishDraftNotes(ctx context.Context, mr Ref) error
}

// ErrUnavailable marks a feature this forge instance does not offer
// (e.g. a Premium endpoint on a Free instance).
var ErrUnavailable = errors.New("feature unavailable on this forge")

// ErrNotFound marks a missing resource.
var ErrNotFound = errors.New("not found")

// ErrAuth marks a credential problem (401/403 on a non-gated endpoint).
var ErrAuth = errors.New("authentication failed")

// Group returns the namespace of a project path ("a/b/c" -> "a/b").
func Group(project string) string {
	if i := strings.LastIndex(project, "/"); i > 0 {
		return project[:i]
	}
	return project
}
