// Package review is ADE's manual-review workflow: line comments collect as
// drafts (kept in cache.db so quitting loses nothing), and one submit sends
// them to the forge as a single batch (GitLab draft notes plus one
// bulk_publish) so the author gets one notification, not one per comment.
package review

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/coredev-uk/ade/internal/forge"
)

// Side says which version of the file a draft's line number refers to.
type Side string

const (
	SideNew Side = "new" // the default: a line of the file as it will be
	SideOld Side = "old" // a removed line, or an unchanged one counted in the old file
)

// Severity is how strongly a comment asks for change.
type Severity string

const (
	SeverityNone       Severity = ""
	SeverityBlocking   Severity = "blocking"
	SeveritySuggestion Severity = "suggestion"
	SeverityQuestion   Severity = "question"
	SeverityNit        Severity = "nit"
)

// Source says who wrote a draft.
type Source string

const (
	SourceYou   Source = "you"
	SourceAgent Source = "agent"
)

// State is a draft's triage state. Your own drafts are accepted as written;
// an agent's start pending until triaged.
type State string

const (
	StatePending  State = "pending"
	StateAccepted State = "accepted"
	StateDenied   State = "denied"
)

// Draft is one line comment on its way to the forge.
type Draft struct {
	ID       string   `json:"id"` // local
	Path     string   `json:"path"`
	OldPath  string   `json:"old_path,omitempty"` // set when the file was renamed
	Line     int      `json:"line"`               // on Side; 0 with an empty Path is an overview comment
	Side     Side     `json:"side"`
	Body     string   `json:"body"`
	Severity Severity `json:"severity,omitempty"`
	Source   Source   `json:"source"`
	State    State    `json:"state"`
	// RemoteID is the draft note this draft became on the forge, once uploaded;
	// Submit uses it to resume after a failure without duplicating comments.
	RemoteID int64 `json:"remote_id,omitempty"`
	// Published is set once the forge's batch publish succeeded.
	Published bool      `json:"published,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Selected reports whether Submit sends d: accepted drafts, and your own
// unless you denied them. Pending agent drafts wait for triage.
func (d Draft) Selected() bool {
	return d.State == StateAccepted || (d.Source == SourceYou && d.State != StateDenied)
}

// Overview reports whether d is a comment on the MR as a whole.
func (d Draft) Overview() bool { return d.Path == "" }

// Normalize fills defaults (id, side, source, state, time) and validates d.
func Normalize(d Draft, now time.Time) (Draft, error) {
	if d.ID == "" {
		d.ID = strings.ToLower(ulid.Make().String())
	}
	if d.Side == "" {
		d.Side = SideNew
	}
	if d.Source == "" {
		d.Source = SourceYou
	}
	if d.State == "" {
		d.State = StateAccepted
		if d.Source == SourceAgent {
			d.State = StatePending
		}
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now.UTC()
	}
	d.Body = strings.TrimSpace(d.Body)
	switch {
	case d.Body == "":
		return d, errors.New("review: a comment needs a body")
	case d.Side != SideNew && d.Side != SideOld:
		return d, fmt.Errorf("review: side %q: want new or old", d.Side)
	case d.Source != SourceYou && d.Source != SourceAgent:
		return d, fmt.Errorf("review: source %q: want you or agent", d.Source)
	case d.State != StatePending && d.State != StateAccepted && d.State != StateDenied:
		return d, fmt.Errorf("review: state %q: want pending, accepted or denied", d.State)
	case !validSeverity(d.Severity):
		return d, fmt.Errorf("review: severity %q: want blocking, suggestion, question or nit", d.Severity)
	case d.Path != "" && d.Line <= 0:
		return d, fmt.Errorf("review: a comment on %s needs a line", d.Path)
	case d.Path == "" && d.Line != 0:
		return d, errors.New("review: an overview comment has no line")
	}
	return d, nil
}

func validSeverity(s Severity) bool {
	switch s {
	case SeverityNone, SeverityBlocking, SeveritySuggestion, SeverityQuestion, SeverityNit:
		return true
	}
	return false
}

// PositionFor anchors a draft to the diff's latest version. It returns nil for
// an overview comment, and ErrNoFile or ErrNotInDiff when the draft's line is
// not in the diff.
func PositionFor(d forge.Diff, dr Draft) (*forge.DiffPosition, error) {
	if dr.Overview() {
		return nil, nil
	}
	locate := LineFor
	if dr.Side == SideOld {
		locate = OldLineFor
	}
	p, err := locate(d, dr.Path, dr.Line)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Session is an MR under review: the diff being commented on and the drafts
// collected so far. It lives in memory; the Store keeps the drafts.
type Session struct {
	MR     forge.Ref
	Diff   forge.Diff
	Drafts []Draft

	store *Store
	now   func() time.Time
}

// Open fetches the MR's latest diff and loads its saved drafts.
func Open(ctx context.Context, w forge.MRWriter, st *Store, mr forge.Ref) (*Session, error) {
	d, err := w.MRDiff(ctx, mr)
	if err != nil {
		return nil, err
	}
	return Resume(st, mr, d)
}

// Resume builds a session from a diff already in hand.
func Resume(st *Store, mr forge.Ref, d forge.Diff) (*Session, error) {
	drafts, err := st.List(mr)
	if err != nil {
		return nil, err
	}
	return &Session{MR: mr, Diff: d, Drafts: drafts, store: st, now: time.Now}, nil
}

// Add validates the draft against the diff (rejecting a line GitLab would
// refuse), fills in the renamed file's old path, saves it and returns it.
func (s *Session) Add(d Draft) (Draft, error) {
	d, err := Normalize(d, s.now())
	if err != nil {
		return d, err
	}
	if !d.Overview() {
		if _, err := PositionFor(s.Diff, d); err != nil {
			return d, err
		}
		if f, ok := File(s.Diff, d.Path); ok {
			d.Path, d.OldPath = f.NewPath, ""
			if f.OldPath != f.NewPath {
				d.OldPath = f.OldPath
			}
		}
	}
	if err := s.store.Put(s.MR, d); err != nil {
		return d, err
	}
	s.Drafts = append(s.Drafts, d)
	return d, nil
}

// Decide sets a draft's triage state.
func (s *Session) Decide(id string, st State) error {
	i := s.index(id)
	if i < 0 {
		return fmt.Errorf("review: no draft %q on %s", id, s.MR)
	}
	d := s.Drafts[i]
	d.State = st
	d, err := Normalize(d, s.now())
	if err != nil {
		return err
	}
	if err := s.store.Put(s.MR, d); err != nil {
		return err
	}
	s.Drafts[i] = d
	return nil
}

// Remove deletes a draft.
func (s *Session) Remove(id string) error {
	i := s.index(id)
	if i < 0 {
		return fmt.Errorf("review: no draft %q on %s", id, s.MR)
	}
	if err := s.store.Delete(s.MR, id); err != nil {
		return err
	}
	s.Drafts = append(s.Drafts[:i], s.Drafts[i+1:]...)
	return nil
}

func (s *Session) index(id string) int {
	return slices.IndexFunc(s.Drafts, func(d Draft) bool { return d.ID == id })
}

// Submit sends the session's drafts with Submit, saving progress after every
// step so a failed run resumes where it stopped, then drops what was
// published (and what was denied) from the store.
func (s *Session) Submit(ctx context.Context, w forge.MRWriter, summary string, approve bool, opts ...Option) (Result, error) {
	opts = append([]Option{WithDiff(s.Diff), WithProgress(func(ds []Draft) error {
		s.Drafts = ds
		return s.store.Save(s.MR, ds)
	})}, opts...)
	res, err := Submit(ctx, w, s.MR, s.Drafts, summary, approve, opts...)
	if err != nil || res.DryRun {
		return res, err
	}
	kept := make([]Draft, 0, len(res.Drafts))
	for _, d := range res.Drafts {
		if !d.Published && d.State != StateDenied {
			kept = append(kept, d)
		}
	}
	s.Drafts = kept
	return res, s.store.Save(s.MR, kept)
}

func now() time.Time { return time.Now() }
