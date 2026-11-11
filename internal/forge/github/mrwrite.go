package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coredev-uk/ade/internal/forge"
)

type wireDiffFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"` // added removed modified renamed copied changed unchanged
	Patch            string `json:"patch"`
}

// MRDiff implements forge.MRWriter. GitHub quotes comment positions by the
// head commit and a path, so BaseSHA and StartSHA (both the base branch's tip
// here) are informational. Files without a patch (binary, or too large) have
// an empty Diff.
func (c *Client) MRDiff(ctx context.Context, ref forge.Ref) (forge.Diff, error) {
	if err := wantKind(ref, forge.KindMR); err != nil {
		return forge.Diff{}, err
	}
	var (
		pr    wirePull
		files []wireDiffFile
	)
	err := concurrently(
		func() error { return c.getJSON(ctx, pullPath(ref), nil, false, &pr) },
		func() (err error) {
			files, err = getAll[wireDiffFile](ctx, c, pullPath(ref)+"/files", nil, false)
			return
		},
	)
	if err != nil {
		return forge.Diff{}, err
	}
	d := forge.Diff{BaseSHA: pr.Base.SHA, StartSHA: pr.Base.SHA, HeadSHA: pr.Head.SHA, Files: make([]forge.DiffFile, len(files))}
	for i, f := range files {
		old := f.PreviousFilename
		if old == "" {
			old = f.Filename
		}
		d.Files[i] = forge.DiffFile{
			OldPath: old, NewPath: f.Filename, Diff: f.Patch,
			NewFile: f.Status == "added", Deleted: f.Status == "removed", Renamed: f.Status == "renamed",
		}
	}
	return d, nil
}

// readTail returns the last n bytes of r (everything when n <= 0), starting at
// a character boundary.
func readTail(r io.Reader, n int) (string, error) {
	if n <= 0 {
		b, err := io.ReadAll(r)
		return string(b), err
	}
	buf := make([]byte, 0, 2*n)
	chunk := make([]byte, 32*1024)
	for {
		k, err := r.Read(chunk)
		buf = append(buf, chunk[:k]...)
		if len(buf) > 2*n {
			buf = append(buf[:0], buf[len(buf)-n:]...)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if len(buf) > n {
		buf = buf[len(buf)-n:]
	}
	for len(buf) > 0 && !utf8.RuneStart(buf[0]) {
		buf = buf[1:]
	}
	return string(buf), nil
}

// getPull reads the pull request itself (node id, head, draft flag).
func (c *Client) getPull(ctx context.Context, mr forge.Ref) (wirePull, error) {
	var w wirePull
	err := c.getJSON(ctx, pullPath(mr), nil, false, &w)
	return w, err
}

func (c *Client) wantWritable(mr forge.Ref, body string) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("github: a comment needs a body")
	}
	return nil
}

// Reply implements forge.MRWriter. Review threads take a thread reply;
// conversation comments and review summaries are not threaded on GitHub, so a
// reply to one is a new conversation comment.
func (c *Client) Reply(ctx context.Context, mr forge.Ref, discussionID, body string) (forge.Note, error) {
	if err := c.wantWritable(mr, body); err != nil {
		return forge.Note{}, err
	}
	if discussionID == "" {
		return forge.Note{}, errors.New("github: reply needs a discussion id")
	}
	if !isThreadID(discussionID) {
		return c.issueComment(ctx, mr, body)
	}
	var out struct {
		Reply *struct {
			Comment gqlThreadComment `json:"comment"`
		} `json:"addPullRequestReviewThreadReply"`
	}
	vars := map[string]any{"input": map[string]any{"pullRequestReviewThreadId": discussionID, "body": body}}
	if err := c.graphql(ctx, addReviewThreadReplyMutation, vars, &out); err != nil {
		return forge.Note{}, err
	}
	if out.Reply == nil {
		return forge.Note{}, fmt.Errorf("github: reply to %s: no comment returned", discussionID)
	}
	return out.Reply.Comment.note(), nil
}

func (c *Client) issueComment(ctx context.Context, ref forge.Ref, body string) (forge.Note, error) {
	var w wireComment
	if err := c.send(ctx, http.MethodPost, issuePath(ref)+"/comments", map[string]string{"body": body}, &w); err != nil {
		return forge.Note{}, err
	}
	return w.note(), nil
}

// Resolve implements forge.MRWriter. Only review threads can be resolved.
func (c *Client) Resolve(ctx context.Context, mr forge.Ref, discussionID string, resolved bool) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	switch {
	case discussionID == "":
		return errors.New("github: resolve needs a discussion id")
	case !isThreadID(discussionID):
		return fmt.Errorf("github: %s is a conversation comment, not a review thread: %w", discussionID, forge.ErrUnavailable)
	}
	doc := resolveThreadMutation
	if !resolved {
		doc = unresolveThreadMutation
	}
	return c.graphql(ctx, doc, map[string]any{"id": discussionID}, nil)
}

// anchor turns a position into GitHub's (path, line, side). Only a new line
// anchors on the right side; an old-only line is the left side. A renamed
// file is always addressed by its new path.
func anchor(p *forge.DiffPosition) (path string, line int, side string, err error) {
	path = p.NewPath
	if path == "" {
		path = p.OldPath
	}
	switch {
	case path == "":
		return "", 0, "", errors.New("github: diff position needs a path")
	case p.NewLine > 0:
		return path, p.NewLine, "RIGHT", nil
	case p.OldLine > 0:
		return path, p.OldLine, "LEFT", nil
	}
	return "", 0, "", errors.New("github: diff position needs a line")
}

type wireReviewComment struct {
	ID           int64     `json:"id"`
	Path         string    `json:"path"`
	Line         *int      `json:"line"`
	OriginalLine *int      `json:"original_line"`
	Side         string    `json:"side"`
	Body         string    `json:"body"`
	InReplyToID  int64     `json:"in_reply_to_id"`
	User         wireUser  `json:"user"`
	CreatedAt    time.Time `json:"created_at"`
}

func (w wireReviewComment) position() *forge.Position {
	if w.Path == "" {
		return nil
	}
	line := 0
	switch {
	case w.Line != nil:
		line = *w.Line
	case w.OriginalLine != nil:
		line = *w.OriginalLine
	}
	p := &forge.Position{Path: w.Path}
	if w.Side == "LEFT" {
		p.OldLine = line
	} else {
		p.NewLine = line
	}
	return p
}

// Comment implements forge.MRWriter: an immediate thread on a diff line, or a
// conversation comment.
func (c *Client) Comment(ctx context.Context, mr forge.Ref, body string, pos *forge.DiffPosition) (forge.Discussion, error) {
	if err := c.wantWritable(mr, body); err != nil {
		return forge.Discussion{}, err
	}
	if pos == nil {
		n, err := c.issueComment(ctx, mr, body)
		if err != nil {
			return forge.Discussion{}, err
		}
		return forge.Discussion{ID: issueCommentPrefix + strconv.FormatInt(n.ID, 10), Notes: []forge.Note{n}}, nil
	}
	path, line, side, err := anchor(pos)
	if err != nil {
		return forge.Discussion{}, err
	}
	head := pos.HeadSHA
	if head == "" {
		pr, err := c.getPull(ctx, mr)
		if err != nil {
			return forge.Discussion{}, err
		}
		head = pr.Head.SHA
	}
	in := map[string]any{"body": body, "commit_id": head, "path": path, "line": line, "side": side}
	var w wireReviewComment
	if err := c.send(ctx, http.MethodPost, pullPath(mr)+"/comments", in, &w); err != nil {
		return forge.Discussion{}, err
	}
	_, threads, err := c.reviewThreads(ctx, mr)
	if err != nil {
		return forge.Discussion{}, err
	}
	for _, t := range threads {
		for _, tc := range t.Comments.Nodes {
			if tc.DatabaseID == w.ID {
				if d, ok := t.discussion(); ok {
					return d, nil
				}
			}
		}
	}
	return forge.Discussion{
		Notes:    []forge.Note{{ID: w.ID, Author: w.User.user(), Body: w.Body, CreatedAt: w.CreatedAt}},
		Position: w.position(),
	}, nil
}

// Approve implements forge.MRWriter: an APPROVE review. A sha pins it to that
// head; a newer push makes GitHub reject it. GitHub refuses approvals of one's
// own pull request.
func (c *Client) Approve(ctx context.Context, mr forge.Ref, sha string) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	in := map[string]string{"event": "APPROVE"}
	if sha != "" {
		in["commit_id"] = sha
	}
	return c.send(ctx, http.MethodPost, pullPath(mr)+"/reviews", in, nil)
}

// Unapprove implements forge.MRWriter: GitHub has no unapprove, so the
// caller's standing approval is dismissed.
func (c *Client) Unapprove(ctx context.Context, mr forge.Ref) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	me, err := c.Me(ctx)
	if err != nil {
		return err
	}
	reviews, err := getAll[wireReview](ctx, c, pullPath(mr)+"/reviews", nil, false)
	if err != nil {
		return err
	}
	var mine *wireReview
	for i, r := range reviews {
		switch {
		case r.User.ID != me.ID:
		case r.State == "APPROVED":
			mine = &reviews[i]
		case r.State == "CHANGES_REQUESTED" || r.State == "DISMISSED":
			mine = nil
		}
	}
	if mine == nil {
		return fmt.Errorf("github: %s has no approval by %s: %w", mr, me.Username, forge.ErrNotFound)
	}
	path := pullPath(mr) + "/reviews/" + strconv.FormatInt(mine.ID, 10) + "/dismissals"
	return c.send(ctx, http.MethodPut, path, map[string]string{"message": "Approval withdrawn."}, nil)
}

// SetDraft implements forge.MRWriter through GraphQL; REST cannot change a
// pull request's draft state.
func (c *Client) SetDraft(ctx context.Context, mr forge.Ref, draft bool) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	pr, err := c.getPull(ctx, mr)
	if err != nil {
		return err
	}
	if pr.Draft == draft {
		return nil
	}
	doc := markReadyMutation
	if draft {
		doc = convertToDraftMutation
	}
	return c.graphql(ctx, doc, map[string]any{"id": pr.NodeID}, nil)
}

// Merge implements forge.MRWriter. The SHA is mandatory: merging a head the
// caller never saw is exactly what the pin prevents. AutoMerge asks GitHub to
// merge when requirements pass (the repository must allow it); GitHub deletes
// the branch of an auto-merged PR only per the repository's own setting, so
// RemoveSourceBranch applies to immediate merges.
func (c *Client) Merge(ctx context.Context, mr forge.Ref, o forge.MergeOpts) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	if o.SHA == "" {
		return errors.New("github: merge needs the head SHA that was reviewed")
	}
	pr, err := c.getPull(ctx, mr)
	if err != nil {
		return err
	}
	if o.AutoMerge {
		method := "MERGE"
		if o.Squash {
			method = "SQUASH"
		}
		vars := map[string]any{"input": map[string]any{"pullRequestId": pr.NodeID, "mergeMethod": method, "expectedHeadOid": o.SHA}}
		return c.graphql(ctx, enableAutoMergeMutation, vars, nil)
	}
	in := map[string]any{"sha": o.SHA, "merge_method": "merge"}
	if o.Squash {
		in["merge_method"] = "squash"
	}
	msg := o.Message
	if o.Squash && o.SquashMessage != "" {
		msg = o.SquashMessage
	}
	if msg != "" {
		title, rest, _ := strings.Cut(msg, "\n")
		in["commit_title"] = title
		if rest = strings.TrimSpace(rest); rest != "" {
			in["commit_message"] = rest
		}
	}
	if err := c.send(ctx, http.MethodPut, pullPath(mr)+"/merge", in, nil); err != nil {
		return err
	}
	if o.RemoveSourceBranch && pr.Head.Repo != nil && pr.Head.Repo.FullName == mr.Project {
		ref := repoPath(mr.Project) + "/git/refs/heads/" + escapeRefPath(pr.Head.Ref)
		err := c.send(ctx, http.MethodDelete, ref, nil, nil)
		if err != nil && !errors.Is(err, forge.ErrNotFound) && !isStatus(err, http.StatusUnprocessableEntity) {
			return fmt.Errorf("merged %s but deleting branch %s failed: %w", mr, pr.Head.Ref, err)
		}
	}
	return nil
}

// escapeRefPath escapes each segment of a ref name but keeps the slashes.
func escapeRefPath(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// --- draft notes: one pending review --------------------------------------
//
// GitHub batches review comments in a pending review, visible only to its
// author; submitting it publishes every comment at once with one
// notification. A caller has at most one. Comments positioned on the diff are
// its comments. A comment without a position (the overview a review adds) is
// the review's body; GitHub cannot edit the body of a pending review, so the
// overview is kept by the client until the review is submitted. It therefore
// lives only as long as the Client: a process that dies between adding an
// overview and publishing loses it, and a draft store that resumes in a new
// process finds it gone.

// overviewID is the DraftNote id of an MR's overview text. Real comment ids
// are positive.
const overviewID int64 = -1

func (c *Client) overview(mr forge.Ref) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.overviews[mr]
}

func (c *Client) setOverview(mr forge.Ref, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if text == "" {
		delete(c.overviews, mr)
		return
	}
	c.overviews[mr] = text
}

// addOverview appends body to the MR's overview, separated by a blank line.
func (c *Client) addOverview(mr forge.Ref, body string) forge.DraftNote {
	full := body
	if prev := c.overview(mr); prev != "" {
		full = prev + "\n\n" + body
	}
	c.setOverview(mr, full)
	return forge.DraftNote{ID: overviewID, Body: full}
}

// myPendingReview finds the caller's pending review, nil when there is none.
func (c *Client) myPendingReview(ctx context.Context, mr forge.Ref) (*wireReview, error) {
	me, err := c.Me(ctx)
	if err != nil {
		return nil, err
	}
	reviews, err := getAll[wireReview](ctx, c, pullPath(mr)+"/reviews", nil, false)
	if err != nil {
		return nil, err
	}
	for i, r := range reviews {
		if r.State == "PENDING" && r.User.ID == me.ID {
			return &reviews[i], nil
		}
	}
	return nil, nil
}

// pendingReview returns the caller's pending review, starting one at headSHA
// (the current head when empty) if there is none.
func (c *Client) pendingReview(ctx context.Context, mr forge.Ref, headSHA string) (wireReview, error) {
	r, err := c.myPendingReview(ctx, mr)
	if err != nil || r != nil {
		if r == nil {
			return wireReview{}, err
		}
		return *r, err
	}
	if headSHA == "" {
		pr, err := c.getPull(ctx, mr)
		if err != nil {
			return wireReview{}, err
		}
		headSHA = pr.Head.SHA
	}
	var created wireReview
	if err := c.send(ctx, http.MethodPost, pullPath(mr)+"/reviews", map[string]string{"commit_id": headSHA}, &created); err != nil {
		return wireReview{}, err
	}
	return created, nil
}

func reviewPath(mr forge.Ref, id int64) string {
	return pullPath(mr) + "/reviews/" + strconv.FormatInt(id, 10)
}

// AddDraftNote implements forge.MRWriter: a comment in the caller's pending
// review (started on first use). With a position it opens a thread on that
// diff line, with inReplyTo (a review thread id) it answers the thread, and
// with neither it extends the review's overview body.
func (c *Client) AddDraftNote(ctx context.Context, mr forge.Ref, body string, pos *forge.DiffPosition, inReplyTo string) (forge.DraftNote, error) {
	if err := c.wantWritable(mr, body); err != nil {
		return forge.DraftNote{}, err
	}
	switch {
	case pos != nil && inReplyTo != "":
		return forge.DraftNote{}, errors.New("github: a draft reply takes no diff position")
	case inReplyTo != "" && !isThreadID(inReplyTo):
		return forge.DraftNote{}, fmt.Errorf("github: %s is not a review thread; only threads take replies inside a review", inReplyTo)
	}
	var path, side string
	var line int
	if pos != nil {
		var err error
		if path, line, side, err = anchor(pos); err != nil {
			return forge.DraftNote{}, err
		}
	}
	head := ""
	if pos != nil {
		head = pos.HeadSHA
	}
	if pos == nil && inReplyTo == "" {
		return c.addOverview(mr, body), nil
	}
	review, err := c.pendingReview(ctx, mr, head)
	if err != nil {
		return forge.DraftNote{}, err
	}

	switch {
	case pos != nil:
		var out struct {
			Add *struct {
				Thread struct {
					Comments struct {
						Nodes []struct {
							DatabaseID int64 `json:"databaseId"`
						} `json:"nodes"`
					} `json:"comments"`
				} `json:"thread"`
			} `json:"addPullRequestReviewThread"`
		}
		input := map[string]any{"pullRequestReviewId": review.NodeID, "path": path, "line": line, "side": side, "body": body}
		if err := c.graphql(ctx, addReviewThreadMutation, map[string]any{"input": input}, &out); err != nil {
			return forge.DraftNote{}, err
		}
		if out.Add == nil || len(out.Add.Thread.Comments.Nodes) == 0 {
			return forge.DraftNote{}, fmt.Errorf("github: no comment returned for the new thread on %s:%d", path, line)
		}
		p := &forge.Position{Path: path}
		if side == "LEFT" {
			p.OldLine = line
		} else {
			p.NewLine = line
		}
		return forge.DraftNote{ID: out.Add.Thread.Comments.Nodes[0].DatabaseID, Body: body, Position: p}, nil

	default: // a reply to a thread
		var out struct {
			Reply *struct {
				Comment struct {
					DatabaseID int64 `json:"databaseId"`
				} `json:"comment"`
			} `json:"addPullRequestReviewThreadReply"`
		}
		input := map[string]any{"pullRequestReviewId": review.NodeID, "pullRequestReviewThreadId": inReplyTo, "body": body}
		if err := c.graphql(ctx, addReviewThreadReplyMutation, map[string]any{"input": input}, &out); err != nil {
			return forge.DraftNote{}, err
		}
		if out.Reply == nil {
			return forge.DraftNote{}, fmt.Errorf("github: no comment returned for the reply to %s", inReplyTo)
		}
		return forge.DraftNote{ID: out.Reply.Comment.DatabaseID, Body: body, InReplyTo: inReplyTo}, nil
	}
}

// DraftNotes implements forge.MRWriter: the overview, then the caller's
// pending review comments. The pending comments come from the review threads
// query: only their author sees them there, and unlike the REST list it
// carries line and side (REST reports only a diff-relative position).
func (c *Client) DraftNotes(ctx context.Context, mr forge.Ref) ([]forge.DraftNote, error) {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return nil, err
	}
	var out []forge.DraftNote
	if o := c.overview(mr); o != "" {
		out = append(out, forge.DraftNote{ID: overviewID, Body: o})
	}
	_, threads, err := c.reviewThreads(ctx, mr)
	if err != nil {
		return nil, err
	}
	for _, t := range threads {
		for i, cm := range t.Comments.Nodes {
			if !cm.pending() {
				continue
			}
			d := forge.DraftNote{ID: cm.DatabaseID, Body: cm.Body}
			if i == 0 {
				d.Position = t.position()
			} else {
				d.InReplyTo = t.ID
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// DeleteDraftNote implements forge.MRWriter. The overview id takes back the
// overview text.
func (c *Client) DeleteDraftNote(ctx context.Context, mr forge.Ref, id int64) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	if id == overviewID {
		c.setOverview(mr, "")
		return nil
	}
	return c.send(ctx, http.MethodDelete, repoPath(mr.Project)+"/pulls/comments/"+strconv.FormatInt(id, 10), nil, nil)
}

// PublishDraftNotes implements forge.MRWriter: submitting the pending review
// as a plain comment review publishes every comment and the overview in one
// event, so the pull request's participants get a single notification. With
// only an overview and no pending review, the review is created and submitted
// in one request.
func (c *Client) PublishDraftNotes(ctx context.Context, mr forge.Ref) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	review, err := c.myPendingReview(ctx, mr)
	if err != nil {
		return err
	}
	overview := c.overview(mr)
	in := map[string]string{"event": "COMMENT"}
	if overview != "" {
		in["body"] = overview
	}
	switch {
	case review != nil:
		err = c.send(ctx, http.MethodPost, reviewPath(mr, review.ID)+"/events", in, nil)
	case overview != "":
		err = c.send(ctx, http.MethodPost, pullPath(mr)+"/reviews", in, nil)
	default:
		return fmt.Errorf("github: %s has no pending review to publish: %w", mr, forge.ErrNotFound)
	}
	if err == nil {
		c.setOverview(mr, "")
	}
	return err
}
