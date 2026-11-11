package review

import (
	"context"
	"strconv"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forge"
)

// AuditedWriter wraps a forge.MRWriter so that every write leaves a `forge.*`
// audit entry (success or failure), and, in dry-run mode, performs none and
// records what would have happened. Reads pass straight through. Comment
// bodies are not logged, only their length: they live on the forge.
//
// Actions: forge.thread.reply, forge.thread.resolve, forge.thread.create,
// forge.approve, forge.unapprove, forge.mr.draft, forge.mr.merge,
// forge.pipeline.retry, forge.draftnote.add, forge.draftnote.delete,
// forge.draftnote.publish.
type AuditedWriter struct {
	forge.MRWriter
	log *audit.Log
	dry bool
}

// Audited wraps w. A nil log still honours dryRun.
func Audited(w forge.MRWriter, log *audit.Log, dryRun bool) *AuditedWriter {
	return &AuditedWriter{MRWriter: w, log: log, dry: dryRun}
}

// DryRun reports whether writes are suppressed.
func (a *AuditedWriter) DryRun() bool { return a.dry }

// do runs fn unless dry-run, and audits the attempt.
func (a *AuditedWriter) do(action, item, target string, params map[string]any, fn func() error) error {
	var err error
	if !a.dry {
		err = fn()
	}
	if a.log != nil {
		e := audit.Entry{Action: action, Item: item, Target: target, Params: params, DryRun: a.dry}
		switch {
		case err != nil:
			e.Outcome, e.Error = "error", err.Error()
		case a.dry:
			e.Outcome = "skipped"
		}
		_ = a.log.Append(e) // never mask the forge result with an audit failure
	}
	return err
}

func (a *AuditedWriter) RetryPipeline(ctx context.Context, project string, pipelineID int64) error {
	return a.do("forge.pipeline.retry", project, strconv.FormatInt(pipelineID, 10), nil, func() error {
		return a.MRWriter.RetryPipeline(ctx, project, pipelineID)
	})
}

func (a *AuditedWriter) Reply(ctx context.Context, mr forge.Ref, discussionID, body string) (n forge.Note, err error) {
	err = a.do("forge.thread.reply", mr.String(), discussionID, map[string]any{"chars": len(body)}, func() (e error) {
		n, e = a.MRWriter.Reply(ctx, mr, discussionID, body)
		return
	})
	return n, err
}

func (a *AuditedWriter) Resolve(ctx context.Context, mr forge.Ref, discussionID string, resolved bool) error {
	return a.do("forge.thread.resolve", mr.String(), discussionID, map[string]any{"resolved": resolved}, func() error {
		return a.MRWriter.Resolve(ctx, mr, discussionID, resolved)
	})
}

func (a *AuditedWriter) Comment(ctx context.Context, mr forge.Ref, body string, pos *forge.DiffPosition) (d forge.Discussion, err error) {
	params := map[string]any{"chars": len(body)}
	if pos != nil {
		params["path"], params["old_line"], params["new_line"] = pos.NewPath, pos.OldLine, pos.NewLine
	}
	err = a.do("forge.thread.create", mr.String(), "", params, func() (e error) {
		d, e = a.MRWriter.Comment(ctx, mr, body, pos)
		return
	})
	return d, err
}

func (a *AuditedWriter) Approve(ctx context.Context, mr forge.Ref, sha string) error {
	return a.do("forge.approve", mr.String(), "", map[string]any{"sha": sha}, func() error {
		return a.MRWriter.Approve(ctx, mr, sha)
	})
}

func (a *AuditedWriter) Unapprove(ctx context.Context, mr forge.Ref) error {
	return a.do("forge.unapprove", mr.String(), "", nil, func() error { return a.MRWriter.Unapprove(ctx, mr) })
}

func (a *AuditedWriter) SetDraft(ctx context.Context, mr forge.Ref, draft bool) error {
	return a.do("forge.mr.draft", mr.String(), "", map[string]any{"draft": draft}, func() error {
		return a.MRWriter.SetDraft(ctx, mr, draft)
	})
}

func (a *AuditedWriter) Merge(ctx context.Context, mr forge.Ref, o forge.MergeOpts) error {
	params := map[string]any{
		"sha": o.SHA, "squash": o.Squash, "remove_source_branch": o.RemoveSourceBranch, "auto_merge": o.AutoMerge,
	}
	if o.Message != "" {
		params["message"] = o.Message
	}
	if o.SquashMessage != "" {
		params["squash_message"] = o.SquashMessage
	}
	return a.do("forge.mr.merge", mr.String(), "", params, func() error { return a.MRWriter.Merge(ctx, mr, o) })
}

func (a *AuditedWriter) AddDraftNote(ctx context.Context, mr forge.Ref, body string, pos *forge.DiffPosition, inReplyTo string) (n forge.DraftNote, err error) {
	params := map[string]any{"chars": len(body)}
	if pos != nil {
		params["path"], params["old_line"], params["new_line"] = pos.NewPath, pos.OldLine, pos.NewLine
	}
	if a.dry {
		n.Body = body // a dry run has no id to hand back
	}
	err = a.do("forge.draftnote.add", mr.String(), inReplyTo, params, func() (e error) {
		n, e = a.MRWriter.AddDraftNote(ctx, mr, body, pos, inReplyTo)
		return
	})
	return n, err
}

func (a *AuditedWriter) DeleteDraftNote(ctx context.Context, mr forge.Ref, id int64) error {
	return a.do("forge.draftnote.delete", mr.String(), strconv.FormatInt(id, 10), nil, func() error {
		return a.MRWriter.DeleteDraftNote(ctx, mr, id)
	})
}

func (a *AuditedWriter) PublishDraftNotes(ctx context.Context, mr forge.Ref) error {
	return a.do("forge.draftnote.publish", mr.String(), "", nil, func() error { return a.MRWriter.PublishDraftNotes(ctx, mr) })
}
