package ai

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/ledger"
	"github.com/coredev-uk/ade/internal/review"
)

// ReviewComment is one structured comment from the review role.
type ReviewComment struct {
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Side       string `json:"side"`
	Severity   string `json:"severity"`
	Body       string `json:"body"`
	Suggestion string `json:"suggestion,omitempty"`
}

// Rejected is a comment that could not become a draft, and why.
type Rejected struct {
	Comment ReviewComment `json:"comment"`
	Reason  string        `json:"reason"`
}

// maxReviewComments bounds one pass: the prompt asks for at most 10, this is
// the backstop if the model ignores it.
const maxReviewComments = 12

// ParseReview reads the review role's answer.
func ParseReview(text string) ([]ReviewComment, error) {
	var cs []ReviewComment
	if err := decodeJSON(text, &cs); err != nil {
		return nil, fmt.Errorf("review: the model's answer is not the expected JSON array: %w", err)
	}
	if len(cs) > maxReviewComments {
		cs = cs[:maxReviewComments]
	}
	return cs, nil
}

// draftBody renders a comment as the text a reviewer would post.
func (c ReviewComment) draftBody() string {
	body := strings.TrimSpace(c.Body)
	if s := strings.TrimRight(c.Suggestion, "\n"); strings.TrimSpace(s) != "" {
		body += "\n\n```suggestion:-0+0\n" + s + "\n```"
	}
	return body
}

// batchNits folds several nits into one comment, anchored at the first nit's
// line, so the author gets one nit comment, not many (docs/PLAN.md §6.9).
func batchNits(cs []ReviewComment) []ReviewComment {
	var nits []ReviewComment
	var rest []ReviewComment
	for _, c := range cs {
		if review.Severity(strings.ToLower(c.Severity)) == review.SeverityNit {
			nits = append(nits, c)
		} else {
			rest = append(rest, c)
		}
	}
	if len(nits) < 2 {
		return cs
	}
	var b strings.Builder
	b.WriteString("Nits:")
	for _, n := range nits {
		fmt.Fprintf(&b, "\n- %s:%d %s", n.Path, n.Line, strings.TrimSpace(n.Body))
	}
	first := nits[0]
	first.Body, first.Suggestion = b.String(), ""
	return append(rest, first)
}

// AddReviewDrafts turns comments into agent drafts (Source=agent, pending)
// in sess, which validates each against the MR's latest diff: a comment on a
// line the diff does not show is rejected, never sent to the forge. Each
// draft is audited as review.comment.draft with the profile that wrote it,
// so triage rates can be attributed. Drafts already in the session with the
// same place and body are skipped, so a repeated pass adds nothing twice.
func (s *Service) AddReviewDrafts(sess *review.Session, cs []ReviewComment, profile string) (added []review.Draft, rejected []Rejected, err error) {
	// Validate first, then batch: a nit that cannot be placed must not be
	// folded into one that can.
	var valid []ReviewComment
	for _, c := range cs {
		if verr := checkComment(sess.Diff, c); verr != nil {
			rejected = append(rejected, Rejected{Comment: c, Reason: verr.Error()})
			continue
		}
		valid = append(valid, c)
	}
	for _, c := range batchNits(valid) {
		d := c.draft()
		if dup(sess.Drafts, d) {
			continue
		}
		got, aerr := sess.Add(d)
		if aerr != nil {
			if isInvalidDraft(aerr) || errors.Is(aerr, review.ErrNoFile) || errors.Is(aerr, review.ErrNotInDiff) {
				rejected = append(rejected, Rejected{Comment: c, Reason: aerr.Error()})
				continue
			}
			return added, rejected, aerr
		}
		added = append(added, got)
		if aerr := s.Audit.Append(audit.Entry{
			Action: ledger.DraftAction, Item: sess.MR.String(), Target: got.ID,
			Params: map[string]any{"profile": profile, "severity": string(got.Severity), "path": got.Path, "line": got.Line},
		}); aerr != nil {
			return added, rejected, fmt.Errorf("audit: %w", aerr)
		}
	}
	return added, rejected, nil
}

// draft converts the comment to an agent draft, pending triage.
func (c ReviewComment) draft() review.Draft {
	side := review.SideNew
	if strings.EqualFold(c.Side, "old") {
		side = review.SideOld
	}
	return review.Draft{
		Path: strings.TrimSpace(c.Path), Line: c.Line, Side: side,
		Body: c.draftBody(), Severity: review.Severity(strings.ToLower(strings.TrimSpace(c.Severity))),
		Source: review.SourceAgent, State: review.StatePending,
	}
}

// checkComment reports why c could not become a draft on this diff.
func checkComment(d forge.Diff, c ReviewComment) error {
	dr, err := review.Normalize(c.draft(), time.Now())
	if err != nil {
		return err
	}
	_, err = review.PositionFor(d, dr)
	return err
}

// isInvalidDraft reports a Normalize validation failure (bad severity, empty
// body, line missing): the model's fault, so the comment is rejected rather
// than the whole pass failing.
func isInvalidDraft(err error) bool { return strings.HasPrefix(err.Error(), "review: ") }

func dup(ds []review.Draft, d review.Draft) bool {
	for _, x := range ds {
		if x.Path == d.Path && x.Line == d.Line && x.Side == d.Side && strings.TrimSpace(x.Body) == strings.TrimSpace(d.Body) {
			return true
		}
	}
	return false
}

// Decision is a triage verdict on an agent draft.
type Decision string

const (
	Accept Decision = "accept"
	Edit   Decision = "edit"
	Deny   Decision = "deny"
)

// AuditDecision records a triage decision as review.comment.<decision>, the
// entries the budget page's accept/edit/deny rates are computed from.
func AuditDecision(log *audit.Log, mr forge.Ref, draftID string, d Decision, reason string) error {
	var p map[string]any
	if reason != "" {
		p = map[string]any{"reason": reason}
	}
	return log.Append(audit.Entry{Action: "review.comment." + string(d), Item: mr.String(), Target: draftID, Params: p})
}
