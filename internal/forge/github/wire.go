package github

import (
	"regexp"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// The wire types decode only what ADE reads. GitHub adds fields freely, so
// every optional field is a pointer or has a usable zero value.

type wireUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

func (u wireUser) user() forge.User {
	return forge.User{ID: u.ID, Username: u.Login, Name: u.Name}
}

func users(in []wireUser) []forge.User {
	if len(in) == 0 {
		return nil
	}
	out := make([]forge.User, len(in))
	for i, u := range in {
		out[i] = u.user()
	}
	return out
}

type wireLabel struct {
	Name string `json:"name"`
}

func labelNames(in []wireLabel) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, l := range in {
		out[i] = l.Name
	}
	return out
}

// startLine finds the sprint start in a milestone description: GitHub
// milestones have a due date only, so the first "Start: YYYY-MM-DD" line
// carries the other end.
var startLine = regexp.MustCompile(`(?im)^[ \t]*start(?:s|\s+date)?[ \t]*[:=][ \t]*(\d{4}-\d{2}-\d{2})\b`)

type wireMilestone struct {
	ID          int64      `json:"id"`
	Number      int        `json:"number"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	State       string     `json:"state"`
	DueOn       *time.Time `json:"due_on"`
}

func (m wireMilestone) milestone() forge.Milestone {
	out := forge.Milestone{ID: m.ID, Title: m.Title, State: "active"}
	if m.State == "closed" {
		out.State = "closed"
	}
	if sub := startLine.FindStringSubmatch(m.Description); sub != nil {
		if d, err := forge.ParseDate(sub[1]); err == nil {
			out.Start = d
		}
	}
	if m.DueOn != nil {
		u := m.DueOn.UTC()
		out.Due = forge.NewDate(u.Year(), u.Month(), u.Day())
	}
	return out
}

// repoOf extracts "owner/repo" from a repository_url ("…/repos/owner/repo").
func repoOf(repositoryURL, fallback string) string {
	_, rest, ok := strings.Cut(repositoryURL, "/repos/")
	if !ok || strings.Count(rest, "/") != 1 {
		return fallback
	}
	return rest
}

type wireIssue struct {
	ID            int64          `json:"id"`
	Number        int            `json:"number"`
	Title         string         `json:"title"`
	Body          string         `json:"body"`
	State         string         `json:"state"`
	HTMLURL       string         `json:"html_url"`
	RepositoryURL string         `json:"repository_url"`
	Labels        []wireLabel    `json:"labels"`
	User          wireUser       `json:"user"`
	Assignees     []wireUser     `json:"assignees"`
	Milestone     *wireMilestone `json:"milestone"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	ClosedAt      *time.Time     `json:"closed_at"`
	// PullRequest is present when the "issue" is a pull request: GitHub lists
	// both through the issues endpoints.
	PullRequest *struct{} `json:"pull_request"`
}

func openedOrClosed(state string) string {
	if state == "open" {
		return "opened"
	}
	return state
}

// issue converts; project is the fallback owner of the number when the
// payload names no repository.
func (w wireIssue) issue(project string) forge.Issue {
	is := forge.Issue{
		Ref:         forge.Ref{Project: repoOf(w.RepositoryURL, project), IID: w.Number, Kind: forge.KindIssue},
		ID:          w.ID,
		Title:       w.Title,
		Description: w.Body,
		State:       openedOrClosed(w.State),
		WebURL:      w.HTMLURL,
		Labels:      labelNames(w.Labels),
		Author:      w.User.user(),
		Assignees:   users(w.Assignees),
		CreatedAt:   w.CreatedAt,
		UpdatedAt:   w.UpdatedAt,
		ClosedAt:    w.ClosedAt,
	}
	is.Tasks.Done, is.Tasks.Total = taskCounts(w.Body)
	if w.Milestone != nil {
		m := w.Milestone.milestone()
		is.Milestone = &m
	}
	return is
}

var taskItem = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+]|\d+[.)])[ \t]+\[([ xX])\]`)

// taskCounts reads Markdown checklists the way GitHub renders them.
func taskCounts(body string) (done, total int) {
	for _, m := range taskItem.FindAllStringSubmatch(body, -1) {
		total++
		if m[1] != " " {
			done++
		}
	}
	return done, total
}

type wireBranch struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo *struct {
		FullName string `json:"full_name"`
	} `json:"repo"` // null once the fork is deleted
}

type wirePull struct {
	ID                 int64       `json:"id"`
	NodeID             string      `json:"node_id"`
	Number             int         `json:"number"`
	Title              string      `json:"title"`
	Body               string      `json:"body"`
	State              string      `json:"state"`
	Draft              bool        `json:"draft"`
	HTMLURL            string      `json:"html_url"`
	User               wireUser    `json:"user"`
	Assignees          []wireUser  `json:"assignees"`
	RequestedReviewers []wireUser  `json:"requested_reviewers"`
	Labels             []wireLabel `json:"labels"`
	Head               wireBranch  `json:"head"`
	Base               wireBranch  `json:"base"`
	UpdatedAt          time.Time   `json:"updated_at"`
	MergedAt           *time.Time  `json:"merged_at"`
	MergedBy           *wireUser   `json:"merged_by"` // single-pull payload only
	// Mergeable is null while GitHub computes it, and absent from lists.
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
}

func (w wirePull) mr(project string) forge.MR {
	state := openedOrClosed(w.State)
	if w.MergedAt != nil {
		state = "merged"
	}
	return forge.MR{
		Ref:            forge.Ref{Project: project, IID: w.Number, Kind: forge.KindMR},
		ID:             w.ID,
		Title:          w.Title,
		Description:    w.Body,
		State:          state,
		Draft:          w.Draft,
		SourceBranch:   w.Head.Ref,
		TargetBranch:   w.Base.Ref,
		HeadSHA:        w.Head.SHA,
		WebURL:         w.HTMLURL,
		Author:         w.User.user(),
		Assignees:      users(w.Assignees),
		Reviewers:      users(w.RequestedReviewers),
		Approvals:      forge.Approvals{Required: -1},
		HasConflicts:   w.MergeableState == "dirty",
		DetailedStatus: w.MergeableState,
		UpdatedAt:      w.UpdatedAt,
		MergedAt:       w.MergedAt,
		MergedBy:       w.mergedBy(),
	}
}

func (w wirePull) mergedBy() string {
	if w.MergedBy == nil {
		return ""
	}
	return w.MergedBy.Login
}

// changesRequested lists reviewers whose latest verdict is CHANGES_REQUESTED.
func changesRequested(reviews []wireReview) []forge.User {
	latest := map[int64]wireReview{}
	var order []int64
	for _, r := range reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
		default:
			continue
		}
		if _, seen := latest[r.User.ID]; !seen {
			order = append(order, r.User.ID)
		}
		latest[r.User.ID] = r
	}
	var out []forge.User
	for _, id := range order {
		if r := latest[id]; r.State == "CHANGES_REQUESTED" {
			out = append(out, r.User.user())
		}
	}
	return out
}

type wireReview struct {
	ID          int64      `json:"id"`
	NodeID      string     `json:"node_id"`
	User        wireUser   `json:"user"`
	Body        string     `json:"body"`
	State       string     `json:"state"` // APPROVED CHANGES_REQUESTED COMMENTED DISMISSED PENDING
	CommitID    string     `json:"commit_id"`
	SubmittedAt *time.Time `json:"submitted_at"`
}

// approvals reduces reviews to the standing approvals: a reviewer's latest
// APPROVED, CHANGES_REQUESTED or DISMISSED review decides; later plain
// comments change nothing, as on GitHub. Required is -1: whether approvals are
// required is branch-protection state the free tier does not report.
func approvals(reviews []wireReview) forge.Approvals {
	a := forge.Approvals{Required: -1}
	latest := map[int64]wireReview{}
	var order []int64
	for _, r := range reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
		default:
			continue
		}
		if _, seen := latest[r.User.ID]; !seen {
			order = append(order, r.User.ID)
		}
		latest[r.User.ID] = r
	}
	for _, id := range order {
		if r := latest[id]; r.State == "APPROVED" {
			a.ApprovedBy = append(a.ApprovedBy, r.User.user())
		}
	}
	return a
}

type wireComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	User      wireUser  `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

func (c wireComment) note() forge.Note {
	return forge.Note{ID: c.ID, Author: c.User.user(), Body: c.Body, CreatedAt: c.CreatedAt}
}

// Discussion ids. Review threads use GitHub's own node id ("PRRT_…"); things
// that GitHub does not thread get a prefixed REST id so that a Reply to them
// can post a conversation comment instead.
const (
	issueCommentPrefix = "issuecomment:"
	reviewPrefix       = "review:"
)

func isThreadID(id string) bool {
	return !strings.HasPrefix(id, issueCommentPrefix) && !strings.HasPrefix(id, reviewPrefix)
}

type gqlThread struct {
	ID           string `json:"id"`
	IsResolved   bool   `json:"isResolved"`
	IsOutdated   bool   `json:"isOutdated"`
	Path         string `json:"path"`
	Line         *int   `json:"line"`
	OriginalLine *int   `json:"originalLine"`
	DiffSide     string `json:"diffSide"`
	Comments     struct {
		Nodes []gqlThreadComment `json:"nodes"`
	} `json:"comments"`
}

type gqlThreadComment struct {
	ID         string    `json:"id"`
	DatabaseID int64     `json:"databaseId"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
	Author     *struct {
		Login string `json:"login"`
	} `json:"author"` // null once the account is deleted
	Review *struct {
		State string `json:"state"`
	} `json:"pullRequestReview"`
}

func (c gqlThreadComment) pending() bool { return c.Review != nil && c.Review.State == "PENDING" }

func (c gqlThreadComment) note() forge.Note {
	n := forge.Note{ID: c.DatabaseID, Body: c.Body, CreatedAt: c.CreatedAt}
	if c.Author != nil {
		n.Author = forge.User{Username: c.Author.Login}
	} else {
		n.Author = forge.User{Username: "ghost"}
	}
	return n
}

// discussion converts a thread without the viewer's pending comments, which
// are drafts and not yet part of the conversation. ok is false when nothing
// is left.
func (t gqlThread) discussion() (d forge.Discussion, ok bool) {
	d = forge.Discussion{ID: t.ID, Resolvable: true, Resolved: t.IsResolved}
	for _, c := range t.Comments.Nodes {
		if !c.pending() {
			d.Notes = append(d.Notes, c.note())
		}
	}
	if len(d.Notes) == 0 {
		return forge.Discussion{}, false
	}
	d.Position = t.position()
	return d, true
}

// position anchors the thread: the current line, or for an outdated thread
// the line it was written on. A LEFT side line is a removed one.
func (t gqlThread) position() *forge.Position {
	if t.Path == "" {
		return nil
	}
	p := &forge.Position{Path: t.Path}
	line := 0
	switch {
	case t.Line != nil:
		line = *t.Line
	case t.OriginalLine != nil:
		line = *t.OriginalLine
	}
	if t.DiffSide == "LEFT" {
		p.OldLine = line
	} else {
		p.NewLine = line
	}
	return p
}

// threadCounts tallies resolvable threads, the numbers an MR header shows.
func threadCounts(ds []forge.Discussion) (unresolved, resolved int) {
	for _, d := range ds {
		switch {
		case !d.Resolvable:
		case d.Resolved:
			resolved++
		default:
			unresolved++
		}
	}
	return unresolved, resolved
}
