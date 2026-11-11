package agile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
)

var (
	me    = forge.User{Username: "sandbox-dev"}
	rae   = forge.User{Username: "reviewer-rae"}
	api1  = forge.Ref{Project: "g/api", IID: 1, Kind: forge.KindMR}
	trk1  = forge.Ref{Project: "g/tracker", IID: 1}
	trk3  = forge.Ref{Project: "g/tracker", IID: 3}
	trk5  = forge.Ref{Project: "g/tracker", IID: 5}
	trk10 = forge.Ref{Project: "g/tracker", IID: 10}
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func ev(when, action, typ string, target forge.Ref, by forge.User) forge.Event {
	return forge.Event{At: at(when), Action: action, TargetType: typ, Target: target, Title: "t", Author: by}
}

func texts(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Text
	}
	return out
}

func TestStandupOnMondayReportsFriday(t *testing.T) {
	in := StandupInput{
		Date: date("2026-11-09"), // a Monday
		Cal:  utcCal(),
		Me:   []forge.User{me},
		Activity: []forge.Event{
			ev("2026-11-05 16:00", "opened", "MergeRequest", api1, me),   // Thursday: before the window
			ev("2026-11-06 09:30", "accepted", "MergeRequest", api1, me), // Friday
			ev("2026-11-07 11:00", "opened", "Issue", trk3, me),          // Saturday: after Friday, still news
			ev("2026-11-06 10:00", "opened", "Issue", trk1, rae),         // someone else's
			ev("2026-11-09 08:00", "closed", "Issue", trk5, me),          // Monday itself: not yesterday
		},
	}
	s := BuildStandup(in)
	if s.Yesterday != "2026-11-06" || s.Date != "2026-11-09" {
		t.Fatalf("dates = %s / %s, want standup 2026-11-09 over 2026-11-06", s.Date, s.Yesterday)
	}
	got := texts(s.Done)
	if len(got) != 2 || !strings.HasPrefix(got[0], "merged MR api!1") || !strings.HasPrefix(got[1], "opened issue tracker#3") {
		t.Fatalf("Done = %q, want Friday's merge then Saturday's issue", got)
	}
}

func TestStandupTuesdayUsesMonday(t *testing.T) {
	in := StandupInput{
		Date: date("2026-11-10"), Cal: utcCal(), Me: []forge.User{me},
		Activity: []forge.Event{
			ev("2026-11-06 09:30", "accepted", "MergeRequest", api1, me),
			ev("2026-11-09 14:00", "closed", "Issue", trk10, me),
		},
	}
	s := BuildStandup(in)
	if s.Yesterday != "2026-11-09" || len(s.Done) != 1 || !strings.HasPrefix(s.Done[0].Text, "closed issue tracker#10") {
		t.Fatalf("got %s %q", s.Yesterday, texts(s.Done))
	}
}

func TestStandupMergeInFeedAndCacheIsOneEntry(t *testing.T) {
	merged := at("2026-11-06 09:30")
	wi := domain.WorkItem{
		Ref: trk1, Assigned: true,
		MRs: []forge.MR{{Ref: api1, Title: "t", State: "merged", Author: me, MergedAt: &merged}},
	}
	in := StandupInput{
		Date: date("2026-11-09"), Cal: utcCal(), Me: []forge.User{me}, Items: []domain.WorkItem{wi},
		Activity: []forge.Event{ev("2026-11-06 09:30", "accepted", "MergeRequest", api1, me)},
	}
	if got := texts(BuildStandup(in).Done); len(got) != 1 {
		t.Fatalf("merge reported %d times: %q", len(got), got)
	}
	// Without the feed (it was unavailable) the cache still says it.
	in.Activity = nil
	if got := texts(BuildStandup(in).Done); len(got) != 1 {
		t.Fatalf("cache alone: %q", got)
	}
}

func TestStandupFoldsCommentsAndPushes(t *testing.T) {
	push := forge.Event{At: at("2026-11-06 10:00"), Action: "pushed to", Branch: "1-rate-limit", Author: me}
	in := StandupInput{
		Date: date("2026-11-09"), Cal: utcCal(), Me: []forge.User{me},
		Activity: []forge.Event{
			ev("2026-11-06 11:00", "commented on", "Note", api1, me),
			ev("2026-11-06 11:05", "commented on", "Note", api1, me),
			ev("2026-11-06 11:07", "commented on", "Note", api1, me),
			push, push,
		},
	}
	got := texts(BuildStandup(in).Done)
	want := []string{"pushed 1-rate-limit", "commented on api!1 (3 comments)"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Done = %q, want %q", got, want)
	}
}

func TestStandupAuditAndInbox(t *testing.T) {
	in := StandupInput{
		Date: date("2026-11-09"), Cal: utcCal(), Me: []forge.User{me},
		Audit: []audit.Entry{
			{TS: at("2026-11-06 12:00"), Action: "agent.run", Item: trk1.String(), Outcome: "ok"},
			{TS: at("2026-11-06 12:01"), Action: "sync.run", Outcome: "ok"},                       // housekeeping
			{TS: at("2026-11-06 12:02"), Action: "forge.mr.merge", Outcome: "error"},              // failed
			{TS: at("2026-11-06 12:03"), Action: "forge.mr.merge", Outcome: "ok", DryRun: true},   // dry run
			{TS: at("2026-11-05 12:00"), Action: "agent.run", Item: trk1.String(), Outcome: "ok"}, // too early
		},
		Inbox: []events.Event{
			{Time: at("2026-11-06 13:00"), Kind: "mr.approved", Subject: trk1.String(), Title: "api!1 approved by reviewer-rae"},
			{Time: at("2026-11-06 13:01"), Kind: "mr.merged", Title: "api!1 merged"}, // the feed says it first-hand
		},
	}
	s := BuildStandup(in)
	got := texts(s.Done)
	if len(got) != 2 || got[0] != "agent.run g/tracker#1" || got[1] != "api!1 approved by reviewer-rae" {
		t.Fatalf("Done = %q", got)
	}
	if s.Done[0].Source != SourceAudit || s.Done[1].Source != SourceEvents {
		t.Fatalf("sources = %s, %s", s.Done[0].Source, s.Done[1].Source)
	}
}

func weight(n int) *int { return &n }

func TestStandupTodayAndBlockers(t *testing.T) {
	now := at("2026-11-11 09:00")
	failed := &forge.Pipeline{Status: "failed"}
	items := []domain.WorkItem{
		{Ref: trk3, Assigned: true, Court: domain.CourtYou, Stage: domain.StageAssigned, Reason: "start work", Weight: weight(2), Issue: forge.Issue{Title: "Retry webhooks"}},
		{
			Ref: trk1, Assigned: true, Court: domain.CourtYou, Stage: domain.StageReview, Reason: "2 unresolved threads", Weight: weight(3), Issue: forge.Issue{Title: "Rate-limit"},
			MRs: []forge.MR{{Ref: api1, State: "opened", Pipeline: failed, UpdatedAt: now.Add(-time.Hour)}},
		},
		{Ref: trk5, Assigned: true, Court: domain.CourtBlocked, Blocked: true, Reason: "blocked by label", Issue: forge.Issue{Title: "Timestamps"}},
		{Ref: trk10, Assigned: true, Court: domain.CourtYou, Stage: domain.StageDone, Issue: forge.Issue{Title: "Upgrade"}},
		{Ref: forge.Ref{Project: "g/tracker", IID: 7}, Assigned: false, Court: domain.CourtYou, Issue: forge.Issue{Title: "Not mine"}},
		{
			Ref: forge.Ref{Project: "g/tracker", IID: 8}, Assigned: true, Court: domain.CourtReviewers, Stage: domain.StageReview, Issue: forge.Issue{Title: "Scheduled"},
			MRs: []forge.MR{{Ref: forge.Ref{Project: "g/web", IID: 2, Kind: forge.KindMR}, State: "opened", UpdatedAt: now.Add(-50 * time.Hour)}},
		},
	}
	review := forge.MR{Ref: forge.Ref{Project: "g/web", IID: 9, Kind: forge.KindMR}, Title: "Dark mode"}
	s := BuildStandup(StandupInput{Date: now, Cal: utcCal(), Me: []forge.User{me}, Items: items, Reviews: []forge.MR{review}, Now: now})

	var today []string
	for _, f := range s.Today {
		today = append(today, f.Ref)
	}
	want := []string{"g/tracker#1", "g/tracker#3", "g/web!9"} // furthest along first; review requests last; done, blocked, others' items absent
	if strings.Join(today, " ") != strings.Join(want, " ") {
		t.Fatalf("Today = %v, want %v", today, want)
	}

	var blockers []string
	for _, b := range s.Blockers {
		blockers = append(blockers, b.Ref+" "+b.Reason)
	}
	for _, w := range []string{
		"g/tracker#1 pipeline failed on api!1",
		"g/tracker#5 blocked by label",
		"g/tracker#8 web!2 waiting on reviewers for 2 days",
	} {
		if !contains(blockers, w) {
			t.Errorf("missing blocker %q in %q", w, blockers)
		}
	}
	if len(blockers) != 3 {
		t.Errorf("blockers = %q", blockers)
	}
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

func TestStandupEmptyHasEmptyListsNotNull(t *testing.T) {
	s := BuildStandup(StandupInput{Date: date("2026-11-09"), Cal: utcCal()})
	if s.Done == nil || s.Today == nil || s.Blockers == nil {
		t.Fatal("empty sections must be empty lists so JSON consumers see [] not null")
	}
	out := RenderStandup(s, Text)
	for _, want := range []string{"Yesterday · Fri 6 Nov", "nothing recorded", "nothing in your court", "none"} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
}

type fixedNarrator struct {
	text string
	err  error
	got  Standup
}

func (n *fixedNarrator) Narrate(_ context.Context, s Standup) (string, error) {
	n.got = s
	return n.text, n.err
}

func TestWithNarrative(t *testing.T) {
	s := BuildStandup(StandupInput{Date: date("2026-11-09"), Cal: utcCal()})
	if got, err := WithNarrative(context.Background(), nil, s); err != nil || got.Narrative != "" {
		t.Fatalf("nil narrator must leave the standup deterministic: %v %q", err, got.Narrative)
	}
	n := &fixedNarrator{text: "  Friday went fine.\n"}
	got, err := WithNarrative(context.Background(), n, s)
	if err != nil || got.Narrative != "Friday went fine." || n.got.Date != s.Date {
		t.Fatalf("got %q, %v", got.Narrative, err)
	}
	boom := &fixedNarrator{err: errors.New("budget exhausted")}
	got, err = WithNarrative(context.Background(), boom, s)
	if err == nil || got.Narrative != "" {
		t.Fatalf("a failing narrator must not invent text: %q, %v", got.Narrative, err)
	}
	if !strings.Contains(RenderStandup(Standup{Date: s.Date, Yesterday: s.Yesterday, Narrative: "Hello."}, Markdown), "Hello.") {
		t.Error("narrative missing from markdown")
	}
}
