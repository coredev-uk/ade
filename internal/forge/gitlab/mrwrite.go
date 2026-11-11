package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/coredev-uk/ade/internal/forge"
)

var _ forge.MRWriter = (*Client)(nil)

// send issues a write with an optional JSON body and decodes the answer into
// out when there is one (204 answers carry no body).
func (c *Client) send(ctx context.Context, method, path string, in, out any) error {
	r := request{method: method, path: path}
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("gitlab %s %s: encoding request: %w", method, path, err)
		}
		r.body = b
	}
	resp, err := c.do(ctx, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return decode(resp, path, out)
}

type wireVersion struct {
	HeadSHA  string `json:"head_commit_sha"`
	BaseSHA  string `json:"base_commit_sha"`
	StartSHA string `json:"start_commit_sha"`
}

type wireDiffFile struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Diff    string `json:"diff"`
	New     bool   `json:"new_file"`
	Renamed bool   `json:"renamed_file"`
	Deleted bool   `json:"deleted_file"`
}

// MRDiff implements forge.MRWriter. The SHAs come from the newest version
// (GET /versions lists newest first); the files from the paginated /diffs.
func (c *Client) MRDiff(ctx context.Context, ref forge.Ref) (forge.Diff, error) {
	if err := wantKind(ref, forge.KindMR); err != nil {
		return forge.Diff{}, err
	}
	var (
		versions []wireVersion
		files    []wireDiffFile
	)
	base := mrPath(ref)
	err := concurrently(
		func() error { return c.getJSON(ctx, base+"/versions", url.Values{"per_page": {"1"}}, false, &versions) },
		func() (err error) { files, err = getAll[wireDiffFile](ctx, c, base+"/diffs", nil, false); return },
	)
	if err != nil {
		return forge.Diff{}, err
	}
	if len(versions) == 0 {
		return forge.Diff{}, fmt.Errorf("gitlab: %s has no diff version yet (GitLab computes it asynchronously after a push)", ref)
	}
	d := forge.Diff{
		BaseSHA: versions[0].BaseSHA, StartSHA: versions[0].StartSHA, HeadSHA: versions[0].HeadSHA,
		Files: make([]forge.DiffFile, len(files)),
	}
	for i, f := range files {
		d.Files[i] = forge.DiffFile{
			OldPath: f.OldPath, NewPath: f.NewPath, NewFile: f.New, Deleted: f.Deleted, Renamed: f.Renamed, Diff: f.Diff,
		}
	}
	return d, nil
}

type wireJob struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Stage    string   `json:"stage"`
	Status   string   `json:"status"`
	WebURL   string   `json:"web_url"`
	Duration *float64 `json:"duration"`
}

// Jobs implements forge.MRWriter.
func (c *Client) Jobs(ctx context.Context, project string, pipelineID int64) ([]forge.Job, error) {
	raw, err := getAll[wireJob](ctx, c, pipelinePath(project, pipelineID)+"/jobs", nil, false)
	if err != nil {
		return nil, err
	}
	out := make([]forge.Job, len(raw))
	for i, j := range raw {
		out[i] = forge.Job{ID: j.ID, Name: j.Name, Stage: j.Stage, Status: j.Status, WebURL: j.WebURL}
		if j.Duration != nil {
			out[i].Duration = *j.Duration
		}
	}
	return out, nil
}

// JobLog implements forge.MRWriter. GitLab serves the whole trace as plain
// text; only the last tailBytes are kept in memory. A job without a log is
// forge.ErrNotFound, as GitLab answers 404.
func (c *Client) JobLog(ctx context.Context, project string, jobID int64, tailBytes int) (string, error) {
	path := projectPath(project) + "/jobs/" + strconv.FormatInt(jobID, 10) + "/trace"
	resp, err := c.do(ctx, request{method: http.MethodGet, path: path, accept: "text/plain"})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return readTail(resp.Body, tailBytes)
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

// RetryPipeline implements forge.MRWriter: GitLab retries the failed and
// canceled jobs of the same pipeline.
func (c *Client) RetryPipeline(ctx context.Context, project string, pipelineID int64) error {
	return c.send(ctx, http.MethodPost, pipelinePath(project, pipelineID)+"/retry", nil, nil)
}

func pipelinePath(project string, id int64) string {
	return projectPath(project) + "/pipelines/" + strconv.FormatInt(id, 10)
}

func discussionPath(mr forge.Ref, id string) string {
	return mrPath(mr) + "/discussions/" + url.PathEscape(id)
}

// Reply implements forge.MRWriter.
func (c *Client) Reply(ctx context.Context, mr forge.Ref, discussionID, body string) (forge.Note, error) {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return forge.Note{}, err
	}
	if discussionID == "" || strings.TrimSpace(body) == "" {
		return forge.Note{}, errors.New("gitlab: reply needs a discussion id and a body")
	}
	var w wireNote
	if err := c.send(ctx, http.MethodPost, discussionPath(mr, discussionID)+"/notes", map[string]string{"body": body}, &w); err != nil {
		return forge.Note{}, err
	}
	return w.note(), nil
}

// Resolve implements forge.MRWriter.
func (c *Client) Resolve(ctx context.Context, mr forge.Ref, discussionID string, resolved bool) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	if discussionID == "" {
		return errors.New("gitlab: resolve needs a discussion id")
	}
	return c.send(ctx, http.MethodPut, discussionPath(mr, discussionID), map[string]bool{"resolved": resolved}, nil)
}

// wirePositionIn is the position object GitLab takes on create. A zero line is
// left out: GitLab reads "only new_line" as an added line, "only old_line" as
// a removed one and both as an unchanged one.
type wirePositionIn struct {
	Type     string `json:"position_type"`
	BaseSHA  string `json:"base_sha"`
	StartSHA string `json:"start_sha"`
	HeadSHA  string `json:"head_sha"`
	OldPath  string `json:"old_path"`
	NewPath  string `json:"new_path"`
	OldLine  int    `json:"old_line,omitempty"`
	NewLine  int    `json:"new_line,omitempty"`
}

func positionIn(p *forge.DiffPosition) (*wirePositionIn, error) {
	if p == nil {
		return nil, nil
	}
	switch {
	case p.BaseSHA == "" || p.StartSHA == "" || p.HeadSHA == "":
		return nil, errors.New("gitlab: diff position needs base, start and head SHAs (from MRDiff)")
	case p.NewPath == "" && p.OldPath == "":
		return nil, errors.New("gitlab: diff position needs a path")
	case p.NewLine <= 0 && p.OldLine <= 0:
		return nil, errors.New("gitlab: diff position needs a line")
	}
	oldPath, newPath := p.OldPath, p.NewPath
	if oldPath == "" {
		oldPath = newPath
	}
	if newPath == "" {
		newPath = oldPath
	}
	return &wirePositionIn{
		Type: "text", BaseSHA: p.BaseSHA, StartSHA: p.StartSHA, HeadSHA: p.HeadSHA,
		OldPath: oldPath, NewPath: newPath, OldLine: p.OldLine, NewLine: p.NewLine,
	}, nil
}

// Comment implements forge.MRWriter: an immediate thread.
func (c *Client) Comment(ctx context.Context, mr forge.Ref, body string, pos *forge.DiffPosition) (forge.Discussion, error) {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return forge.Discussion{}, err
	}
	if strings.TrimSpace(body) == "" {
		return forge.Discussion{}, errors.New("gitlab: comment needs a body")
	}
	wp, err := positionIn(pos)
	if err != nil {
		return forge.Discussion{}, err
	}
	in := struct {
		Body     string          `json:"body"`
		Position *wirePositionIn `json:"position,omitempty"`
	}{body, wp}
	var w wireDiscussion
	if err := c.send(ctx, http.MethodPost, mrPath(mr)+"/discussions", in, &w); err != nil {
		return forge.Discussion{}, err
	}
	return w.discussion(), nil
}

// Approve implements forge.MRWriter. A sha pins the approval to that head: a
// newer push answers 409.
func (c *Client) Approve(ctx context.Context, mr forge.Ref, sha string) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	var in any
	if sha != "" {
		in = map[string]string{"sha": sha}
	}
	return c.send(ctx, http.MethodPost, mrPath(mr)+"/approve", in, nil)
}

// Unapprove implements forge.MRWriter.
func (c *Client) Unapprove(ctx context.Context, mr forge.Ref) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	return c.send(ctx, http.MethodPost, mrPath(mr)+"/unapprove", nil, nil)
}

// draftPrefix is GitLab's own draft pattern (DRAFT_REGEX): "Draft:", "[Draft]",
// "(Draft)", "Draft - " or a leading "Draft" word, case-insensitive.
var draftPrefix = regexp.MustCompile(`(?i)^\s*(?:\[draft\]|\(draft\)|draft:|draft\s-\s|draft\b)\s*`)

// readyTitle strips every draft marker from the front of title.
func readyTitle(title string) string {
	for {
		t := draftPrefix.ReplaceAllString(title, "")
		if t == title {
			return t
		}
		title = t
	}
}

// SetDraft implements forge.MRWriter. REST has no draft parameter; GitLab
// derives draft from the title, so this adds or removes the "Draft: " prefix.
func (c *Client) SetDraft(ctx context.Context, mr forge.Ref, draft bool) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	var w wireMR
	if err := c.getJSON(ctx, mrPath(mr), nil, false, &w); err != nil {
		return err
	}
	if (w.Draft || w.WIP) == draft {
		return nil
	}
	title := readyTitle(w.Title)
	if draft {
		title = "Draft: " + title
	}
	return c.send(ctx, http.MethodPut, mrPath(mr), map[string]string{"title": title}, nil)
}

// Merge implements forge.MRWriter. The SHA is mandatory: merging a head the
// caller never saw is exactly what the pin prevents.
func (c *Client) Merge(ctx context.Context, mr forge.Ref, o forge.MergeOpts) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	if o.SHA == "" {
		return errors.New("gitlab: merge needs the head SHA that was reviewed")
	}
	in := map[string]any{"sha": o.SHA}
	if o.Squash {
		in["squash"] = true
	}
	if o.RemoveSourceBranch {
		in["should_remove_source_branch"] = true
	}
	if o.AutoMerge {
		in["auto_merge"] = true
	}
	if o.Message != "" {
		in["merge_commit_message"] = o.Message
		if o.Squash {
			in["squash_commit_message"] = o.Message
		}
	}
	return c.send(ctx, http.MethodPut, mrPath(mr)+"/merge", in, nil)
}

type wireDraftNote struct {
	ID           int64  `json:"id"`
	Note         string `json:"note"`
	DiscussionID string `json:"discussion_id"`
	Position     *struct {
		NewPath string `json:"new_path"`
		OldPath string `json:"old_path"`
		NewLine int    `json:"new_line"`
		OldLine int    `json:"old_line"`
	} `json:"position"`
}

func (w wireDraftNote) draft() forge.DraftNote {
	d := forge.DraftNote{ID: w.ID, Body: w.Note, InReplyTo: w.DiscussionID}
	if p := w.Position; p != nil && (p.NewPath != "" || p.OldPath != "") {
		d.Position = &forge.Position{
			Path: positionPath(p.NewPath, p.OldPath), OldPath: p.OldPath, NewLine: p.NewLine, OldLine: p.OldLine,
		}
	}
	return d
}

func draftNotesPath(mr forge.Ref) string { return mrPath(mr) + "/draft_notes" }

// AddDraftNote implements forge.MRWriter.
func (c *Client) AddDraftNote(ctx context.Context, mr forge.Ref, body string, pos *forge.DiffPosition, inReplyTo string) (forge.DraftNote, error) {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return forge.DraftNote{}, err
	}
	if strings.TrimSpace(body) == "" {
		return forge.DraftNote{}, errors.New("gitlab: draft note needs a body")
	}
	if pos != nil && inReplyTo != "" {
		return forge.DraftNote{}, errors.New("gitlab: a draft reply takes no diff position")
	}
	wp, err := positionIn(pos)
	if err != nil {
		return forge.DraftNote{}, err
	}
	in := struct {
		Note      string          `json:"note"`
		InReplyTo string          `json:"in_reply_to_discussion_id,omitempty"`
		Position  *wirePositionIn `json:"position,omitempty"`
	}{body, inReplyTo, wp}
	var w wireDraftNote
	if err := c.send(ctx, http.MethodPost, draftNotesPath(mr), in, &w); err != nil {
		return forge.DraftNote{}, err
	}
	return w.draft(), nil
}

// DraftNotes implements forge.MRWriter.
func (c *Client) DraftNotes(ctx context.Context, mr forge.Ref) ([]forge.DraftNote, error) {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return nil, err
	}
	raw, err := getAll[wireDraftNote](ctx, c, draftNotesPath(mr), nil, false)
	if err != nil {
		return nil, err
	}
	out := make([]forge.DraftNote, len(raw))
	for i, w := range raw {
		out[i] = w.draft()
	}
	return out, nil
}

// DeleteDraftNote implements forge.MRWriter.
func (c *Client) DeleteDraftNote(ctx context.Context, mr forge.Ref, id int64) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	return c.send(ctx, http.MethodDelete, draftNotesPath(mr)+"/"+strconv.FormatInt(id, 10), nil, nil)
}

// PublishDraftNotes implements forge.MRWriter: one bulk_publish call, so the
// MR's participants get a single notification.
func (c *Client) PublishDraftNotes(ctx context.Context, mr forge.Ref) error {
	if err := wantKind(mr, forge.KindMR); err != nil {
		return err
	}
	return c.send(ctx, http.MethodPost, draftNotesPath(mr)+"/bulk_publish", nil, nil)
}
