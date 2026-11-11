package review

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
)

// SummaryID is the id of the draft Submit makes from its summary argument.
const SummaryID = "summary"

// Result reports one Submit run. In a dry run the counts are what a real run
// would do and nothing was written.
type Result struct {
	// Drafts are all of the MR's drafts after the run, with RemoteID and
	// Published updated: what to persist.
	Drafts []Draft `json:"drafts"`
	// Uploaded draft notes were created on the forge by this run.
	Uploaded int `json:"uploaded"`
	// Resumed drafts were already on the forge from an earlier, interrupted
	// run (matched by id or, when a reply was lost, by body and position) and
	// were not created again.
	Resumed int `json:"resumed"`
	// Published is how many draft notes the bulk publish sent.
	Published int `json:"published"`
	// Assumed drafts had been uploaded before but were no longer on the forge;
	// they are taken to have been published already and are never re-created.
	Assumed  int  `json:"assumed,omitempty"`
	Approved bool `json:"approved"`
	// Foreign counts draft notes of yours on the MR that ADE did not write
	// (started in the web UI): the batch publish sends those too.
	Foreign int  `json:"foreign,omitempty"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// Option customises Submit.
type Option func(*options)

type options struct {
	log      *audit.Log
	bus      *events.Bus
	diff     *forge.Diff
	progress func([]Draft) error
	dryRun   bool
}

// WithAudit records the run as a `review.submit` audit entry.
func WithAudit(l *audit.Log) Option { return func(o *options) { o.log = l } }

// WithBus publishes a transient `review.submitted` event on success.
func WithBus(b *events.Bus) Option { return func(o *options) { o.bus = b } }

// WithDiff supplies the diff positions are computed from instead of fetching
// the latest one.
func WithDiff(d forge.Diff) Option { return func(o *options) { o.diff = &d } }

// WithProgress is called with the full draft list after every step that
// changes it (each upload, the publish), so the caller can persist it. An
// error stops the run: the forge already holds the step's effect, and the next
// run resumes from it.
func WithProgress(fn func([]Draft) error) Option { return func(o *options) { o.progress = fn } }

// WithDryRun validates every position and reads the forge's draft notes but
// writes nothing.
func WithDryRun() Option { return func(o *options) { o.dryRun = true } }

// Submit sends a review as one batch: every selected draft (see Draft.Selected)
// becomes a GitLab draft note anchored to the latest diff version, one
// PublishDraftNotes call publishes them together (one notification), then an
// optional approval pinned to the reviewed head. A non-empty summary becomes
// an overview draft, SummaryID, sent with the rest.
//
// It is safe to call again after a failure. Drafts already uploaded are not
// created twice: each remembers its RemoteID, and one whose answer was lost is
// found again on the forge by body and position. Published drafts are never
// re-sent, so a failed approval is retried alone.
//
// Every position is validated before the first write, so a draft whose line is
// outside the diff fails the call without leaving a partial review behind.
// On error the result still carries the drafts as far as they got.
func Submit(ctx context.Context, w forge.MRWriter, mr forge.Ref, drafts []Draft, summary string, approve bool, opts ...Option) (res Result, err error) {
	var o options
	for _, f := range opts {
		f(&o)
	}
	res.Drafts = slices.Clone(drafts)
	res.DryRun = o.dryRun
	defer func() { o.finish(mr, &res, summary != "", approve, err) }()

	if summary = strings.TrimSpace(summary); summary != "" && !slices.ContainsFunc(res.Drafts, func(d Draft) bool { return d.ID == SummaryID }) {
		d, nerr := Normalize(Draft{ID: SummaryID, Body: summary}, now())
		if nerr != nil {
			return res, nerr
		}
		res.Drafts = append(res.Drafts, d)
	}

	// Plan: which drafts go, and where on the diff each lands.
	var send, retract []int
	for i, d := range res.Drafts {
		switch {
		case d.Published:
		case d.Selected():
			send = append(send, i)
		case d.RemoteID != 0:
			retract = append(retract, i) // uploaded, then denied or changed its mind
		}
	}
	diff, err := o.diffFor(ctx, w, mr, approve, res.Drafts, send)
	if err != nil {
		return res, err
	}
	positions := make(map[int]*forge.DiffPosition, len(send))
	for _, i := range send {
		if res.Drafts[i].RemoteID != 0 {
			continue
		}
		p, perr := PositionFor(diff, res.Drafts[i])
		if perr != nil {
			return res, fmt.Errorf("draft %s: %w", res.Drafts[i].ID, perr)
		}
		positions[i] = p
	}

	var remote []forge.DraftNote
	if len(send)+len(retract) > 0 {
		if remote, err = w.DraftNotes(ctx, mr); err != nil {
			return res, err
		}
	}
	onRemote := func(id int64) bool {
		return slices.ContainsFunc(remote, func(n forge.DraftNote) bool { return n.ID == id })
	}
	claimed := map[int64]bool{}
	for _, d := range res.Drafts {
		if d.RemoteID != 0 {
			claimed[d.RemoteID] = true
		}
	}

	save := func() error {
		if o.progress == nil || o.dryRun {
			return nil
		}
		return o.progress(slices.Clone(res.Drafts))
	}

	// Take back drafts that left the selection after being uploaded, so the
	// publish does not send them.
	for _, i := range retract {
		d := &res.Drafts[i]
		if onRemote(d.RemoteID) && !o.dryRun {
			if derr := w.DeleteDraftNote(ctx, mr, d.RemoteID); derr != nil && !errors.Is(derr, forge.ErrNotFound) {
				return res, derr
			}
		}
		delete(claimed, d.RemoteID)
		d.RemoteID = 0
	}

	// Upload.
	var publish []int
	for _, i := range send {
		d := &res.Drafts[i]
		switch {
		case d.RemoteID != 0 && onRemote(d.RemoteID):
			res.Resumed++
			publish = append(publish, i)
			continue
		case d.RemoteID != 0:
			d.Published = true // gone from the forge: published (or deleted); never create it again
			res.Assumed++
			continue
		}
		body := Render(*d)
		if id := adopt(remote, claimed, body, positions[i]); id != 0 {
			d.RemoteID, claimed[id] = id, true
			res.Resumed++
			publish = append(publish, i)
			continue
		}
		res.Uploaded++
		publish = append(publish, i)
		if o.dryRun {
			continue
		}
		n, aerr := w.AddDraftNote(ctx, mr, body, positions[i], "")
		if aerr != nil {
			res.Uploaded--
			return res, fmt.Errorf("upload draft %s: %w", d.ID, aerr)
		}
		d.RemoteID, claimed[n.ID] = n.ID, true
		if serr := save(); serr != nil {
			return res, serr
		}
	}
	for _, n := range remote {
		if !claimed[n.ID] {
			res.Foreign++
		}
	}

	// Publish once.
	if len(publish) > 0 {
		res.Published = len(publish)
		if !o.dryRun {
			if perr := w.PublishDraftNotes(ctx, mr); perr != nil {
				res.Published = 0
				return res, fmt.Errorf("publish review: %w", perr)
			}
			for _, i := range publish {
				res.Drafts[i].Published = true
			}
			if serr := save(); serr != nil {
				return res, serr
			}
		}
	} else if err := save(); err != nil {
		return res, err
	}

	if approve {
		if !o.dryRun {
			if aerr := w.Approve(ctx, mr, diff.HeadSHA); aerr != nil {
				return res, fmt.Errorf("approve: %w", aerr)
			}
		}
		res.Approved = true
	}
	return res, nil
}

// diffFor returns the diff to position against: the supplied one, or the
// latest, fetched only when something needs it (a new positioned comment or an
// approval, which pins the reviewed head).
func (o *options) diffFor(ctx context.Context, w forge.MRWriter, mr forge.Ref, approve bool, ds []Draft, send []int) (forge.Diff, error) {
	if o.diff != nil {
		return *o.diff, nil
	}
	need := approve
	for _, i := range send {
		if ds[i].RemoteID == 0 && !ds[i].Overview() {
			need = true
		}
	}
	if !need {
		return forge.Diff{}, nil
	}
	return w.MRDiff(ctx, mr)
}

// Render is the text a draft is posted as: the body, led by its severity
// ("**blocking:** ...") so the author sees how strongly it is meant.
func Render(d Draft) string {
	if d.Severity == SeverityNone {
		return d.Body
	}
	return "**" + string(d.Severity) + ":** " + d.Body
}

// adopt finds a draft note on the forge that is this comment already: same
// text and position, not claimed by another draft. It recovers drafts whose
// upload succeeded but whose answer never arrived.
func adopt(remote []forge.DraftNote, claimed map[int64]bool, body string, pos *forge.DiffPosition) int64 {
	for _, n := range remote {
		if claimed[n.ID] || n.InReplyTo != "" || n.Body != body {
			continue
		}
		switch {
		case pos == nil && n.Position == nil:
			return n.ID
		case pos != nil && n.Position != nil && n.Position.Path == positionPath(pos) &&
			n.Position.NewLine == pos.NewLine && n.Position.OldLine == pos.OldLine:
			return n.ID
		}
	}
	return 0
}

func positionPath(p *forge.DiffPosition) string {
	if p.NewPath != "" {
		return p.NewPath
	}
	return p.OldPath
}

// finish writes the audit entry and the event for a run.
func (o *options) finish(mr forge.Ref, res *Result, hasSummary, approve bool, err error) {
	if o.log != nil {
		e := audit.Entry{
			Action: "review.submit", Item: mr.String(), DryRun: o.dryRun,
			Params: map[string]any{
				"drafts": res.Uploaded + res.Resumed, "uploaded": res.Uploaded, "resumed": res.Resumed,
				"published": res.Published, "assumed": res.Assumed, "foreign": res.Foreign,
				"summary": hasSummary, "approve": approve, "approved": res.Approved,
			},
		}
		switch {
		case err != nil:
			e.Outcome, e.Error = "error", err.Error()
		case o.dryRun:
			e.Outcome = "skipped"
		}
		_ = o.log.Append(e) // a failed audit write must not undo a published review
	}
	if o.bus != nil && err == nil && !o.dryRun && (res.Published > 0 || res.Approved) {
		ev := events.New("review.submitted", events.Success, submittedTitle(mr, res))
		ev.Subject, ev.Transient = mr.String(), true
		o.bus.Publish(ev)
	}
}

func submittedTitle(mr forge.Ref, res *Result) string {
	parts := []string{}
	if res.Published > 0 {
		parts = append(parts, plural(res.Published, "comment"))
	}
	if res.Approved {
		parts = append(parts, "approved")
	}
	return fmt.Sprintf("Review submitted on %s: %s", mr, strings.Join(parts, ", "))
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
