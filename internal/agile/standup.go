package agile

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
)

// Entry sources.
const (
	SourceForge  = "forge"  // the forge's activity feed or cached forge state
	SourceAudit  = "audit"  // ADE's own audit log: work that never reached the forge
	SourceEvents = "events" // changes on your items noticed by a sync
)

// Entry is one thing that happened.
type Entry struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"` // mr.opened, mr.merged, issue.closed, comment, push, audit, ...
	Ref    string    `json:"ref,omitempty"`
	Text   string    `json:"text"`
	Source string    `json:"source"`
}

// Focus is something on today's plate.
type Focus struct {
	Ref    string `json:"ref"`
	Title  string `json:"title"`
	Stage  string `json:"stage,omitempty"`
	Court  string `json:"court,omitempty"`
	Reason string `json:"reason,omitempty"`
	Weight *int   `json:"weight,omitempty"`
}

// Blocker is something in your way.
type Blocker struct {
	Ref    string `json:"ref"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// Standup is the three-part report. Yesterday is the previous working day;
// its window runs on to this morning, so weekend work is not lost.
type Standup struct {
	Date      string    `json:"date"`                // the standup day, YYYY-MM-DD
	Yesterday string    `json:"yesterday"`           // the previous working day, YYYY-MM-DD
	Done      []Entry   `json:"done"`                // what changed since then, oldest first
	Today     []Focus   `json:"today"`               // your court
	Blockers  []Blocker `json:"blockers"`            //
	Narrative string    `json:"narrative,omitempty"` // optional AI summary of the facts above
	// Warnings name sources that could not be read; the standup then rests on the rest.
	Warnings []string `json:"warnings,omitempty"`
}

// StandupInput is everything BuildStandup interprets. The caller fetches.
type StandupInput struct {
	Date time.Time // the standup day (any instant within it)
	Cal  Calendar
	// Me lists your identity on each forge involved.
	Me      []forge.User
	Items   []domain.WorkItem // cached work items
	Reviews []forge.MR        // MRs awaiting your review
	// Activity is forge events of the projects involved; only yours within the
	// window count.
	Activity []forge.Event
	Audit    []audit.Entry
	Inbox    []events.Event
	// Now dates "waiting on others for more than a day".
	Now time.Time
}

// Window returns [from, to) of the standup: the previous working day's
// midnight to the standup day's midnight.
func (in StandupInput) Window() (from, to time.Time) {
	to = in.Cal.Day(in.Date)
	return in.Cal.PrevWorkday(to), to
}

// inboxKinds are the sync events that say what happened on your items; merges
// are left out because the forge feed reports them first-hand.
var inboxKinds = map[string]string{
	"ci.failed": "ci", "ci.passed": "ci", "mr.approved": "approved", "mr.comment": "thread",
}

// auditPrefixes select the audit actions that are work (as opposed to
// housekeeping such as syncs and config).
var auditPrefixes = []string{"forge.", "review.", "agent.", "plan.", "workspace.", "mr."}

// BuildStandup assembles the standup of in.Date.
func BuildStandup(in StandupInput) Standup {
	from, to := in.Window()
	s := Standup{
		Date:      in.Cal.Day(in.Date).Format("2006-01-02"),
		Yesterday: from.Format("2006-01-02"),
		Done:      []Entry{},
		Today:     []Focus{},
		Blockers:  []Blocker{},
	}
	s.Done = doneWithin(in, from, to)
	s.Today = today(in)
	s.Blockers = blockers(in)
	return s
}

// doneWithin is what you did in [from, to), oldest first: your forge events,
// your merges and closes the cache proves, your ADE actions and the changes on
// your items a sync noticed.
func doneWithin(in StandupInput, from, to time.Time) []Entry {
	within := func(t time.Time) bool { return !t.Before(from) && t.Before(to) }
	d := newDone()
	for _, e := range in.Activity {
		if within(e.At) && isMe(in.Me, e.Author) {
			d.forgeEvent(e)
		}
	}
	for _, wi := range in.Items {
		d.cachedItem(wi, in.Me, within)
	}
	for _, e := range in.Audit {
		if within(e.TS) && auditWorthy(e) {
			d.add(Entry{At: e.TS, Kind: "audit", Ref: e.Item, Text: auditText(e), Source: SourceAudit})
		}
	}
	for _, e := range in.Inbox {
		if k, ok := inboxKinds[e.Kind]; ok && within(e.Time) {
			d.add(Entry{At: e.Time, Kind: k, Ref: e.Subject, Text: e.Title, Source: SourceEvents})
		}
	}
	return d.entries()
}

// LastDay is your last working day before today and what you did on it. The
// day is found from your own work, not the calendar: the last day you pushed
// commits or opened or merged an MR.
type LastDay struct {
	Date      string    `json:"date"` // YYYY-MM-DD; "" when no such day was found
	Done      []Entry   `json:"done"` // oldest first
	Narrative string    `json:"narrative,omitempty"`
	Warnings  []string  `json:"warnings,omitempty"`
	From      time.Time `json:"-"` // the day's midnight, in the calendar's zone
}

// BuildLastDay finds the day of your most recent push, MR opened or MR merged
// before in.Date's day, and collects everything you did on it.
func BuildLastDay(in StandupInput) LastDay {
	before := in.Cal.Day(in.Date)
	var last time.Time
	seen := func(t time.Time) {
		if t.Before(before) && t.After(last) {
			last = t
		}
	}
	for _, e := range in.Activity {
		if isMe(in.Me, e.Author) && workEvent(e) {
			seen(e.At)
		}
	}
	for _, wi := range in.Items {
		for _, mr := range wi.MRs {
			if mr.MergedAt != nil && isMe(in.Me, mr.Author) {
				seen(*mr.MergedAt)
			}
		}
	}
	if last.IsZero() {
		return LastDay{Done: []Entry{}}
	}
	from := in.Cal.Day(last)
	return LastDay{Date: from.Format("2006-01-02"), From: from, Done: doneWithin(in, from, from.AddDate(0, 0, 1))}
}

// workEvent is a feed event that marks a day as worked: commits pushed, an MR
// opened or merged.
func workEvent(e forge.Event) bool {
	if e.Target.IsZero() {
		return e.Branch != "" && strings.HasPrefix(e.Action, "pushed")
	}
	if e.Target.Kind != forge.KindMR {
		return false
	}
	switch e.Action {
	case "opened", "created", "accepted", "merged":
		return true
	}
	return false
}

func isMe(me []forge.User, u forge.User) bool {
	return slices.ContainsFunc(me, func(m forge.User) bool { return sameUser(m, u) })
}

func sameUser(a, b forge.User) bool {
	return a.Username != "" && strings.EqualFold(a.Username, b.Username)
}

// done accumulates entries, dropping duplicates of the same fact reported by
// two sources (a merge is in the feed and on the cached MR).
type done struct {
	seen     map[string]bool
	list     []Entry
	comments map[string]int // target -> index in list
	counts   map[string]int
	pushes   map[string]int
}

func newDone() *done {
	return &done{seen: map[string]bool{}, comments: map[string]int{}, counts: map[string]int{}, pushes: map[string]int{}}
}

func (d *done) add(e Entry) {
	key := e.Kind + "|" + e.Ref + "|" + e.Text
	if e.Source == SourceForge && e.Ref != "" && (strings.HasPrefix(e.Kind, "mr.") || strings.HasPrefix(e.Kind, "issue.")) {
		key = e.Kind + "|" + e.Ref // forge facts dedupe on what happened to what
	}
	if d.seen[key] {
		return
	}
	d.seen[key] = true
	d.list = append(d.list, e)
}

func (d *done) entries() []Entry {
	out := slices.Clone(d.list)
	slices.SortStableFunc(out, func(a, b Entry) int { return a.At.Compare(b.At) })
	if out == nil {
		out = []Entry{}
	}
	return out
}

func short(ref forge.Ref) string {
	proj := ref.Project
	if i := strings.LastIndex(proj, "/"); i >= 0 {
		proj = proj[i+1:]
	}
	sep := "#"
	if ref.Kind == forge.KindMR {
		sep = "!"
	}
	return fmt.Sprintf("%s%s%d", proj, sep, ref.IID)
}

func withTitle(label, title string) string {
	if title == "" {
		return label
	}
	return label + ": " + title
}

func (d *done) forgeEvent(e forge.Event) {
	if e.Target.IsZero() {
		if e.Branch != "" && strings.HasPrefix(e.Action, "pushed") {
			d.push(e)
		}
		return
	}
	ref, label := e.Target.String(), short(e.Target)
	kind := "issue"
	noun := "issue"
	if e.Target.Kind == forge.KindMR {
		kind, noun = "mr", "MR"
	}
	switch e.Action {
	case "opened", "created":
		d.add(Entry{At: e.At, Kind: kind + ".opened", Ref: ref, Text: "opened " + noun + " " + withTitle(label, e.Title), Source: SourceForge})
	case "closed":
		d.add(Entry{At: e.At, Kind: kind + ".closed", Ref: ref, Text: "closed " + noun + " " + withTitle(label, e.Title), Source: SourceForge})
	case "accepted", "merged":
		d.add(Entry{At: e.At, Kind: "mr.merged", Ref: ref, Text: "merged MR " + withTitle(label, e.Title), Source: SourceForge})
	case "commented on":
		d.comment(e, ref, label)
	case "approved":
		d.add(Entry{At: e.At, Kind: "mr.approved", Ref: ref, Text: "approved MR " + withTitle(label, e.Title), Source: SourceForge})
	}
}

// comment folds several comments on one target into one entry.
func (d *done) comment(e forge.Event, ref, label string) {
	if i, ok := d.comments[ref]; ok {
		d.counts[ref]++
		d.list[i].At = later(d.list[i].At, e.At)
		d.list[i].Text = fmt.Sprintf("commented on %s (%d comments)", label, d.counts[ref])
		return
	}
	d.counts[ref] = 1
	d.comments[ref] = len(d.list)
	d.list = append(d.list, Entry{At: e.At, Kind: "comment", Ref: ref, Text: "commented on " + label, Source: SourceForge})
}

func (d *done) push(e forge.Event) {
	if i, ok := d.pushes[e.Branch]; ok {
		d.list[i].At = later(d.list[i].At, e.At)
		return
	}
	d.pushes[e.Branch] = len(d.list)
	d.list = append(d.list, Entry{At: e.At, Kind: "push", Text: "pushed " + e.Branch, Source: SourceForge})
}

// cachedItem reports what the cached state proves: your MRs merged and your
// issues closed inside the window. It backs up the feed, which can be
// unavailable or paged away.
func (d *done) cachedItem(wi domain.WorkItem, me []forge.User, within func(time.Time) bool) {
	for _, mr := range wi.MRs {
		if mr.MergedAt != nil && within(*mr.MergedAt) && isMe(me, mr.Author) {
			d.add(Entry{At: *mr.MergedAt, Kind: "mr.merged", Ref: mr.Ref.String(), Text: "merged MR " + withTitle(short(mr.Ref), mr.Title), Source: SourceForge})
		}
	}
	if is := wi.Issue; is.ClosedAt != nil && within(*is.ClosedAt) && wi.Assigned {
		d.add(Entry{At: *is.ClosedAt, Kind: "issue.closed", Ref: is.Ref.String(), Text: "closed issue " + withTitle(short(is.Ref), is.Title), Source: SourceForge})
	}
}

func auditWorthy(e audit.Entry) bool {
	if e.DryRun || e.Outcome != "ok" {
		return false
	}
	for _, p := range auditPrefixes {
		if strings.HasPrefix(e.Action, p) {
			return true
		}
	}
	return false
}

func auditText(e audit.Entry) string {
	subject := e.Item
	if subject == "" {
		subject = e.Target
	}
	if subject == "" {
		return e.Action
	}
	return e.Action + " " + subject
}

// today lists the open items in your court plus review requests: furthest
// along first, then heavier, then by reference.
func today(in StandupInput) []Focus {
	var items []domain.WorkItem
	for _, wi := range in.Items {
		if wi.Assigned && wi.Court == domain.CourtYou && wi.Stage != domain.StageDone {
			items = append(items, wi)
		}
	}
	slices.SortStableFunc(items, func(a, b domain.WorkItem) int {
		if c := cmp.Compare(b.Stage, a.Stage); c != 0 {
			return c
		}
		if c := cmp.Compare(weightOf(b), weightOf(a)); c != 0 {
			return c
		}
		return cmp.Or(cmp.Compare(a.Ref.Project, b.Ref.Project), cmp.Compare(a.Ref.IID, b.Ref.IID))
	})
	out := make([]Focus, 0, len(items)+len(in.Reviews))
	for _, wi := range items {
		out = append(out, Focus{
			Ref: wi.Ref.String(), Title: wi.Issue.Title, Stage: wi.Stage.String(), Court: string(wi.Court),
			Reason: wi.Reason, Weight: wi.Weight,
		})
	}
	reviews := slices.Clone(in.Reviews)
	slices.SortStableFunc(reviews, func(a, b forge.MR) int {
		return cmp.Or(cmp.Compare(a.Ref.Project, b.Ref.Project), cmp.Compare(a.Ref.IID, b.Ref.IID))
	})
	for _, mr := range reviews {
		out = append(out, Focus{Ref: mr.Ref.String(), Title: mr.Title, Stage: "review", Court: string(domain.CourtYou), Reason: "review requested"})
	}
	return out
}

func weightOf(wi domain.WorkItem) int {
	if wi.Weight == nil {
		return 0
	}
	return *wi.Weight
}

// waitingFor is how long an open MR must have sat idle before it is a blocker.
const waitingFor = 24 * time.Hour

// blockers lists your assigned items that are blocked, have a red pipeline,
// or have waited on others for more than a day.
func blockers(in StandupInput) []Blocker {
	out := []Blocker{}
	for _, wi := range in.Items {
		if !wi.Assigned || wi.Stage == domain.StageDone {
			continue
		}
		ref := wi.Ref.String()
		if wi.Blocked || wi.Court == domain.CourtBlocked {
			out = append(out, Blocker{Ref: ref, Title: wi.Issue.Title, Reason: orDefault(wi.Reason, "blocked")})
		}
		for _, mr := range wi.MRs {
			if mr.State != "opened" {
				continue
			}
			if mr.Pipeline != nil && mr.Pipeline.Status == "failed" {
				out = append(out, Blocker{Ref: ref, Title: wi.Issue.Title, Reason: "pipeline failed on " + short(mr.Ref)})
			}
			if wi.Court == domain.CourtReviewers && !in.Now.IsZero() && in.Now.Sub(mr.UpdatedAt) > waitingFor {
				out = append(out, Blocker{Ref: ref, Title: wi.Issue.Title, Reason: fmt.Sprintf("%s waiting on reviewers for %s", short(mr.Ref), ago(in.Now.Sub(mr.UpdatedAt)))})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b Blocker) int { return cmp.Compare(a.Ref, b.Ref) })
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func ago(d time.Duration) string {
	days := int(d / (24 * time.Hour))
	if days <= 1 {
		return "over a day"
	}
	return fmt.Sprintf("%d days", days)
}

// Narrator turns the deterministic standup facts into a short spoken-style
// summary. It is optional: a nil Narrator leaves the standup deterministic.
type Narrator interface {
	Narrate(ctx context.Context, s Standup) (string, error)
}

// WithNarrative returns s with the narrator's summary attached. A nil
// narrator, or one that fails, leaves s untouched; the error is returned so
// the caller can say the narrative is missing.
func WithNarrative(ctx context.Context, n Narrator, s Standup) (Standup, error) {
	if n == nil {
		return s, nil
	}
	text, err := n.Narrate(ctx, s)
	if err != nil {
		return s, err
	}
	s.Narrative = strings.TrimSpace(text)
	return s, nil
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
