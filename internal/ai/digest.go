package ai

import (
	"context"
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// digestVersion keys the result cache: change the prompt, bump it.
const digestVersion = "digest-v1"

//go:embed prompts/digest.md
var digestSystem string

// Bounds keep a digest prompt (and so its price) small.
const (
	maxDigestNotes     = 40
	maxDigestNoteChars = 800
	plainNoteChars     = 160
)

// DigestNote is one comment from somebody else that arrived after the
// marker. Where says what it is attached to.
type DigestNote struct {
	Author string    `json:"author"`
	At     time.Time `json:"at"`
	Where  string    `json:"where"`
	Body   string    `json:"body"`
}

// DigestInput is what a digest needs from the forge: the item's title and its
// discussions. Me is the reader's username: their own comments are not news
// to them.
type DigestInput struct {
	Ref         forge.Ref
	Title       string
	Me          string
	Discussions []forge.Discussion
}

// LoadDigestInput reads the title and the discussions of an issue or merge
// request. It only reads. A failing Me is not an error: the digest then also
// lists the reader's own comments.
func LoadDigestInput(ctx context.Context, be forge.Forge, ref forge.Ref) (DigestInput, error) {
	in := DigestInput{Ref: ref}
	if ref.Kind == forge.KindMR {
		mr, err := be.MR(ctx, ref)
		if err != nil {
			return in, fmt.Errorf("read %s: %w", ref, err)
		}
		in.Title = mr.Title
	} else {
		is, err := be.Issue(ctx, ref)
		if err != nil {
			return in, fmt.Errorf("read %s: %w", ref, err)
		}
		in.Title = is.Title
	}
	ds, err := be.Discussions(ctx, ref)
	if err != nil {
		return in, fmt.Errorf("read the comments of %s: %w", ref, err)
	}
	in.Discussions = ds
	if me, err := be.Me(ctx); err == nil {
		in.Me = me.Username
	}
	return in, nil
}

// NewComments is what arrived after a marker.
type NewComments struct {
	// Notes are the new comments by others, oldest first: at most
	// maxDigestNotes of them, the newest ones.
	Notes []DigestNote
	// Omitted counts new comments dropped from Notes by that bound.
	Omitted int
	// Newest is the creation time of the newest human comment of the item
	// (the reader's own and omitted ones included): where the marker moves
	// to once the digest is shown. Zero when the item has none.
	Newest time.Time
}

// Count is how many new comments by others there are, omitted ones included.
func (c NewComments) Count() int { return len(c.Notes) + c.Omitted }

// CollectNew picks the human comments created after since (all of them when
// since is zero). System notes (label changes, assignments) are not comments.
// A reply carries where its thread started, never the text of earlier
// comments: only the new ones are summarised.
func CollectNew(in DigestInput, since time.Time) NewComments {
	var out NewComments
	var all []DigestNote
	for _, d := range in.Discussions {
		var first string
		for i, n := range d.Notes {
			if n.System {
				continue
			}
			if first == "" {
				first = n.Author.Username
			}
			if n.CreatedAt.After(out.Newest) {
				out.Newest = n.CreatedAt
			}
			if !n.CreatedAt.After(since) || (in.Me != "" && n.Author.Username == in.Me) {
				continue
			}
			where := "overview"
			if p := d.Position; p != nil && p.Path != "" {
				where = p.Path
				switch {
				case p.NewLine > 0:
					where += fmt.Sprintf(":%d", p.NewLine)
				case p.OldLine > 0:
					where += fmt.Sprintf(":%d (old)", p.OldLine)
				}
			}
			if i > 0 && first != "" {
				where = fmt.Sprintf("reply in %s's thread on %s", first, where)
			}
			all = append(all, DigestNote{Author: n.Author.Username, At: n.CreatedAt, Where: where, Body: n.Body})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.Before(all[j].At) })
	if len(all) > maxDigestNotes {
		out.Omitted = len(all) - maxDigestNotes
		all = all[out.Omitted:]
	}
	out.Notes = all
	return out
}

// DigestJob asks the explain role to summarise the new comments. Only their
// text, authors, places and times are the prompt (never the marker or counts
// that move on their own), so the same new comments hash the same and are
// never bought twice.
func DigestJob(forgeName string, in DigestInput, c NewComments) Job {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", in.Ref, in.Title)
	if c.Omitted > 0 {
		fmt.Fprintf(&b, "\n(%d older new comments are left out)\n", c.Omitted)
	}
	b.WriteString("\nNew comments, oldest first:\n")
	for _, n := range c.Notes {
		fmt.Fprintf(&b, "- [%s] %s, %s: %s\n", n.At.UTC().Format("2006-01-02 15:04Z"), n.Author, n.Where, clip(strings.Join(strings.Fields(n.Body), " "), maxDigestNoteChars))
	}
	kind := "issue"
	if in.Ref.Kind == forge.KindMR {
		kind = "mr"
	}
	return Job{
		Feature: "digest", Role: RoleExplain, PromptVersion: digestVersion,
		System: digestSystem, Prompt: b.String(),
		Forge: forgeName, Ref: in.Ref, Name: fmt.Sprintf("digest-%s%d", kind, in.Ref.IID),
		MaxTokens: 400,
		Params:    map[string]any{"comments": c.Count()},
	}
}

// PlainDigest lists the new comments without a model: author, place, time and
// the first words of each. It is what a digest is with AI off.
func PlainDigest(c NewComments) string {
	var b strings.Builder
	if c.Omitted > 0 {
		fmt.Fprintf(&b, "(%d older new comments are not listed)\n", c.Omitted)
	}
	for _, n := range c.Notes {
		fmt.Fprintf(&b, "- %s, %s, %s: %s\n", n.Author, n.Where, n.At.Local().Format("2 Jan 15:04"), clip(strings.Join(strings.Fields(n.Body), " "), plainNoteChars))
	}
	return strings.TrimRight(b.String(), "\n")
}

// Digest sources.
const (
	DigestSourceAI   = "ai"
	DigestSourceList = "list"
)

// DigestOpts steers one digest.
type DigestOpts struct {
	Profile  string
	Refresh  bool
	Override bool
	Assume   bool
	Confirm  func(Preflight) (bool, error)
	// NoAI lists the new comments instead of summarising them: no model, no
	// cost. It is also what a digest is when ai.mode is off or no profile is set.
	NoAI bool
	// Since replaces the stored marker for this digest (a time range to
	// look back over); the zero time uses the marker.
	Since time.Time
	// Peek leaves the marker where it is: the same comments stay new.
	Peek bool
	// DryRun sends nothing and moves nothing: it only reports the pre-flight.
	DryRun bool
}

// DigestResult is one item's digest.
type DigestResult struct {
	Ref forge.Ref `json:"ref"`
	// Since is the marker the digest started from: zero on a first look.
	Since time.Time `json:"since,omitempty"`
	// Until is where the marker moves to: the newest comment of the item.
	Until time.Time `json:"until,omitempty"`
	// Count is how many new comments by others there were.
	Count int `json:"new_comments"`
	// Omitted counts those that did not fit the prompt (the newest are kept).
	Omitted int `json:"omitted,omitempty"`
	// Text is the digest; empty when nothing is new.
	Text string `json:"text,omitempty"`
	// Source is "ai" or "list" (no model); empty when nothing is new.
	Source string `json:"source,omitempty"`
	// Marked reports that the marker moved to Until.
	Marked bool `json:"marked"`
	// Run is the AI call (a cache hit included); nil when no call was made.
	Run *Result `json:"-"`
	// Preflight and Cached are set by a dry run.
	Preflight *Preflight `json:"preflight,omitempty"`
	Cached    bool       `json:"cached,omitempty"`
}

// aiOn reports whether a model call is possible for profile.
func (s *Service) aiOn(profile string, noAI bool) bool {
	if noAI || s.Cfg.AI.Mode == "off" {
		return false
	}
	if profile == "" {
		profile = s.Cfg.AI.Profile
	}
	_, ok := s.Cfg.AI.Profiles[profile]
	return ok
}

// Digest summarises the comments of an item that arrived since the marker
// kept in its record, then moves the marker to the newest comment. The marker
// only moves once the digest was delivered: a failed or refused call, a
// declined pre-flight, an answer cut short, a dry run and Peek all leave the
// same comments new. Nothing new costs nothing: no call is made, and the
// marker still catches up with the reader's own comments.
func (s *Service) Digest(ctx context.Context, forgeName string, in DigestInput, o DigestOpts) (DigestResult, error) {
	since := o.Since
	if since.IsZero() {
		var err error
		if since, err = s.Records.CommentsSeen(forgeName, in.Ref); err != nil {
			return DigestResult{Ref: in.Ref}, err
		}
	}
	c := CollectNew(in, since)
	res := DigestResult{Ref: in.Ref, Since: since, Until: c.Newest, Count: c.Count(), Omitted: c.Omitted}
	mark := func() error {
		if o.Peek || o.DryRun {
			return nil
		}
		moved, err := s.Records.MarkCommentsSeen(forgeName, in.Ref, c.Newest)
		res.Marked = moved
		if err != nil {
			return fmt.Errorf("the digest is ready, but the last-seen marker could not be saved: %w", err)
		}
		return nil
	}
	if res.Count == 0 {
		return res, mark()
	}
	if !s.aiOn(o.Profile, o.NoAI) {
		res.Text, res.Source = PlainDigest(c), DigestSourceList
		return res, mark()
	}
	job := DigestJob(forgeName, in, c)
	job.Profile, job.Refresh, job.Override, job.Assume, job.Confirm = o.Profile, o.Refresh, o.Override, o.Assume, o.Confirm
	if o.DryRun {
		pf, cached, err := s.Preview(ctx, job)
		if err != nil {
			return res, err
		}
		res.Preflight, res.Cached = &pf, cached
		return res, nil
	}
	r, err := s.Run(ctx, job)
	if err != nil {
		return res, err
	}
	res.Run, res.Text, res.Source = &r, r.Text, DigestSourceAI
	if r.Truncated {
		return res, nil // a fragment is not a digest: the same comments stay new
	}
	return res, mark()
}
