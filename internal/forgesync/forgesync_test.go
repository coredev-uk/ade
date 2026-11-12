package forgesync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/records"
)

const manifestTmpl = `
name = "NAME"
forge = "f"

[tracker]
project = "g/TRACKER"
iterations = { source = "SOURCE" }

[labels.type]
pattern = "type::{value}"

[labels.app]
pattern = "app::{value}"

[repos.api]
path = "g/api"
app  = ["api"]

[repos.web]
path = "g/web"
app  = ["web"]
`

// harness wires a Service to an in-memory forge and a temp app.
type harness struct {
	t   *testing.T
	app *app.App
	f   *memForge
	svc *Service
	dir string
}

// manifestFile is a manifest to register: file name stem plus TOML body.
type manifestFile struct{ stem, body string }

func newHarness(t *testing.T, manifests ...manifestFile) *harness {
	t.Helper()
	dir := t.TempDir()
	rec := filepath.Join(dir, "data")
	al, err := audit.Open(rec, "user")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Forges = map[string]config.Forge{"f": {Kind: "gitlab"}}
	cfg.Records.SnapshotComments = 10
	for _, m := range manifests {
		p := filepath.Join(dir, m.stem+".ade.toml")
		if err := os.WriteFile(p, []byte(m.body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.Projects = append(cfg.Projects, config.ProjectRef{Source: p})
	}
	a := &app.App{
		Paths:  paths.Paths{CacheDir: filepath.Join(dir, "cache"), DataDir: rec},
		Config: &cfg, RecordsDir: rec, Audit: al, Bus: events.NewBus(),
	}
	t.Cleanup(func() { a.Close() })
	f := newMemForge()
	h := &harness{t: t, app: a, f: f, dir: dir}
	h.svc = &Service{
		App: a, Now: func() time.Time { return testNow },
		Open: func(context.Context, string, config.Forge) (forge.Forge, error) { return f, nil },
	}
	return h
}

func manifest(stem, name, tracker, source string) manifestFile {
	r := strings.NewReplacer("NAME", name, "TRACKER", tracker, "SOURCE", source)
	return manifestFile{stem, r.Replace(manifestTmpl)}
}

func (h *harness) sync() []Result {
	h.t.Helper()
	res, err := h.svc.Sync(context.Background())
	if err != nil {
		h.t.Fatalf("Sync: %v", err)
	}
	return res
}

// listen returns a func draining the events published since the last call.
func (h *harness) listen() func() []events.Event {
	sub, cancel := h.app.Bus.Subscribe(256)
	h.t.Cleanup(cancel)
	return func() []events.Event {
		var out []events.Event
		for {
			select {
			case e := <-sub:
				out = append(out, e)
			default:
				return out
			}
		}
	}
}

func byRef(res Result) map[string]domain.WorkItem {
	m := map[string]domain.WorkItem{}
	for _, wi := range res.Items {
		m[wi.Ref.String()] = wi
	}
	return m
}

func refs(res Result) []string {
	var out []string
	for _, wi := range res.Items {
		out = append(out, wi.Ref.String())
	}
	return out
}

// seed fills the forge with a small sandbox-like project.
func seed(f *memForge) {
	s42 := milestone(42, "Sprint 42", "2026-11-02", "2026-11-13")
	s43 := milestone(43, "Sprint 43", "2026-11-16", "2026-11-27")
	s41 := milestone(41, "Sprint 41", "2026-10-19", "2026-10-30")
	f.milestones = []forge.Milestone{s41, s42, s43}

	closed := issue("g/tracker#4", "Already shipped", &s42)
	closed.State = "closed"
	f.issues = []forge.Issue{
		issue("g/tracker#1", "Rate-limit exports", &s42, "me"),
		issue("g/tracker#2", "Paginate audit log", &s42, "me"),
		issue("g/tracker#3", "Retry webhooks", &s42, "me"),
		closed,
		issue("g/tracker#5", "Old sprint item", &s41),
		issue("g/api#1", "Rate-limit exports (api)", nil, "me"),
		issue("g/api#2", "Paginate (api)", nil, "me"),
		issue("g/api#9", "Loose api chore", nil, "me"),
	}
	f.issues[6].Description = "Parent: g/tracker#2\n\nImplementation."
	f.links[ref("g/tracker#1")] = []forge.Link{{Ref: ref("g/api#1"), Type: "relates_to"}}

	rl := forge.MR{
		Ref: ref("g/api!1"), Title: "Rate limit", State: "opened", SourceBranch: "rate-limit",
		Description: "Closes #1", Author: user("me"), Reviewers: []forge.User{user("rae")}, UpdatedAt: testNow,
		WebURL: "https://forge.test/g/api/-/merge_requests/1",
	}
	pg := forge.MR{
		Ref: ref("g/api!2"), Title: "Draft: Paginate", State: "opened", Draft: true,
		SourceBranch: "2-paginate", Author: user("me"), UpdatedAt: testNow,
	}
	review := forge.MR{
		Ref: ref("g/api!5"), Title: "Someone else's change", State: "opened",
		Author: user("rae"), Reviewers: []forge.User{user("me")}, UpdatedAt: testNow,
	}
	f.mrs = []forge.MR{rl, pg, review}
	rlDetail := rl
	rlDetail.Pipeline = &forge.Pipeline{ID: 1, Status: "failed"}
	rlDetail.Unresolved, rlDetail.Resolved = 2, 1
	pgDetail := pg
	pgDetail.Pipeline = &forge.Pipeline{ID: 2, Status: "success"}
	reviewDetail := review
	reviewDetail.Pipeline = &forge.Pipeline{ID: 3, Status: "success"}
	f.mrDetail = map[forge.Ref]forge.MR{rl.Ref: rlDetail, pg.Ref: pgDetail, review.Ref: reviewDetail}
}

func TestSyncMapsChildrenAndMRs(t *testing.T) {
	h := newHarness(t, manifest("demo", "demo", "tracker", "milestones"))
	seed(h.f)

	res := h.sync()
	if len(res) != 1 || res[0].Err != "" {
		t.Fatalf("results = %+v", res)
	}
	r := res[0]
	if r.Tracker != "g/tracker" || r.Forge != "f" || r.Me.Username != "me" {
		t.Errorf("header = %q %q %+v", r.Tracker, r.Forge, r.Me)
	}
	want := []string{"g/tracker#1", "g/tracker#2", "g/tracker#3", "g/tracker#4", "g/api#9"}
	if got := refs(r); !slices.Equal(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	items := byRef(r)

	t1 := items["g/tracker#1"] // child by link, MR by "Closes #1"
	if len(t1.Children) != 1 || t1.Children[0].Ref.String() != "g/api#1" {
		t.Errorf("tracker#1 children = %+v", t1.Children)
	}
	if len(t1.MRs) != 1 || t1.MRs[0].Ref.String() != "g/api!1" {
		t.Fatalf("tracker#1 MRs = %+v", t1.MRs)
	}
	if t1.MRs[0].Pipeline == nil || t1.MRs[0].Pipeline.Status != "failed" || t1.MRs[0].Unresolved != 2 {
		t.Errorf("open MR lacks detail: %+v", t1.MRs[0])
	}
	if t1.Stage != domain.StageCI || t1.Court != domain.CourtYou {
		t.Errorf("tracker#1 stage/court = %v/%v", t1.Stage, t1.Court)
	}

	t2 := items["g/tracker#2"] // child by "Parent:" description, MR by branch prefix
	if len(t2.Children) != 1 || t2.Children[0].Ref.String() != "g/api#2" {
		t.Errorf("tracker#2 children = %+v", t2.Children)
	}
	if len(t2.MRs) != 1 || t2.MRs[0].Ref.String() != "g/api!2" || t2.Stage != domain.StageDraft {
		t.Errorf("tracker#2 MRs/stage = %+v / %v", t2.MRs, t2.Stage)
	}

	if items["g/tracker#4"].Stage != domain.StageDone {
		t.Errorf("closed sprint item stage = %v", items["g/tracker#4"].Stage)
	}
	loose := items["g/api#9"]
	if !loose.Assigned || len(loose.MRs) != 0 {
		t.Errorf("untracked item = %+v", loose)
	}
	if len(r.Reviews) != 1 || r.Reviews[0].Ref.String() != "g/api!5" || r.Reviews[0].Pipeline == nil {
		t.Errorf("reviews = %+v", r.Reviews)
	}
	if r.Window.Current == nil || r.Window.Current.Title != "Sprint 42" || r.Window.DaysLeft != 3 {
		t.Errorf("window = %+v", r.Window)
	}
}

func TestAutoFallsBackToMilestones(t *testing.T) {
	h := newHarness(t, manifest("demo", "demo", "tracker", "auto"))
	seed(h.f)
	h.f.iterErr = forge.ErrUnavailable

	r := h.sync()[0]
	if r.Err != "" {
		t.Fatal(r.Err)
	}
	if r.Window.Current == nil || r.Window.Current.Source != "milestones" || r.Window.Current.Title != "Sprint 42" {
		t.Errorf("window = %+v", r.Window)
	}
}

func TestAutoUsesIterationsWhenAvailable(t *testing.T) {
	h := newHarness(t, manifest("demo", "demo", "tracker", "auto"))
	seed(h.f)
	start, _ := forge.ParseDate("2026-11-09")
	due, _ := forge.ParseDate("2026-11-22")
	h.f.iterations = []forge.Iteration{{ID: 7, Title: "Cycle 5", State: "current", Start: start, Due: due}}

	r := h.sync()[0]
	if r.Window.Current == nil || r.Window.Current.Source != "gitlab-iterations" || r.Window.Current.Title != "Cycle 5" {
		t.Errorf("window = %+v", r.Window)
	}
}

func TestLabelSprints(t *testing.T) {
	m := manifest("demo", "demo", "tracker", "labels")
	m.body = strings.Replace(m.body, `iterations = { source = "labels" }`,
		`iterations = { source = "labels", pattern = "Sprint::{n}", start = "2026-11-02", length = "2w" }`, 1)
	h := newHarness(t, m)
	seed(h.f)
	h.f.issues[0].Labels = append(h.f.issues[0].Labels, "Sprint::1")
	h.f.issues[0].Milestone = nil

	r := h.sync()[0]
	if r.Err != "" {
		t.Fatal(r.Err)
	}
	if r.Window.Current == nil || r.Window.Current.Key != "Sprint::1" || r.Window.DaysLeft != 3 {
		t.Errorf("window = %+v", r.Window)
	}
}

func TestPerProjectErrorIsolation(t *testing.T) {
	h := newHarness(t,
		manifest("a", "alpha", "tracker", "milestones"),
		manifest("b", "beta", "broken", "milestones"),
		manifest("c", "gamma", "tracker", "milestones"),
	)
	seed(h.f)
	h.f.failProject = "g/broken"

	res, err := h.svc.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync returned %v; per-project failures belong in Result.Err", err)
	}
	if len(res) != 3 {
		t.Fatalf("results = %d", len(res))
	}
	if res[0].Err != "" || res[2].Err != "" || len(res[0].Items) == 0 || len(res[2].Items) == 0 {
		t.Errorf("healthy projects affected: %q / %q", res[0].Err, res[2].Err)
	}
	if res[1].Project != "beta" || !strings.Contains(res[1].Err, "boom") {
		t.Errorf("failed project = %+v", res[1])
	}

	entries, err := h.app.Audit.Query(audit.Filter{Action: "sync.run"})
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit = %v, %v", entries, err)
	}
	e := entries[0]
	if e.Actor != Actor || e.Outcome != "error" || e.Params["failed"] != float64(1) || e.Params["projects"] != float64(3) {
		t.Errorf("audit entry = %+v", e)
	}
}

func TestFailureKeepsCachedItems(t *testing.T) {
	h := newHarness(t, manifest("a", "alpha", "tracker", "milestones"))
	seed(h.f)
	h.sync()
	h.f.failProject = "g/tracker"
	h.sync()

	st, _ := h.app.Cache()
	cached, err := Load(st)
	if err != nil || len(cached) != 1 {
		t.Fatalf("Load = %v, %v", cached, err)
	}
	if !strings.Contains(cached[0].Err, "boom") || len(cached[0].Items) == 0 {
		t.Errorf("cached = err %q, %d items", cached[0].Err, len(cached[0].Items))
	}
}

func TestCacheRoundTrip(t *testing.T) {
	h := newHarness(t, manifest("a", "alpha", "tracker", "milestones"), manifest("b", "beta", "tracker", "milestones"))
	seed(h.f)
	res := h.sync()

	st, err := h.app.Cache()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(st)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(res)
	have, _ := json.Marshal(got)
	if string(want) != string(have) {
		t.Errorf("Load differs from Sync:\n got %s\nwant %s", have, want)
	}
	if h.f.meCalls != 1 || h.f.capsCalls != 1 {
		t.Errorf("me/caps fetched %d/%d times; both are cached for 24h across projects", h.f.meCalls, h.f.capsCalls)
	}
}

func kinds(evs []events.Event) map[string][]events.Event {
	m := map[string][]events.Event{}
	for _, e := range evs {
		m[e.Kind] = append(m[e.Kind], e)
	}
	return m
}

func TestEventsDiff(t *testing.T) {
	h := newHarness(t, manifest("demo", "demo", "tracker", "milestones"))
	seed(h.f)
	got := h.listen()

	h.sync()
	first := kinds(got())
	if len(first) != 1 || len(first[EvSyncDone]) != 1 || !first[EvSyncDone][0].Transient {
		t.Fatalf("first sync raised %v; want only a transient sync.done", first)
	}
	if title := first[EvSyncDone][0].Title; title != "synced 1 project · 5 items" {
		t.Errorf("summary = %q", title)
	}

	s43 := milestone(43, "Sprint 43", "2026-11-16", "2026-11-27")
	h.f.update(func(f *memForge) {
		f.issues = append(f.issues, issue("g/tracker#6", "Fresh assignment", &f.milestones[1], "me"))
		f.issues[2].Milestone = &s43 // tracker#3 moves to the next sprint
		d := f.mrDetail[ref("g/api!1")]
		d.Pipeline = &forge.Pipeline{ID: 1, Status: "success"} // failed -> success
		d.Unresolved++
		d.Approvals.ApprovedBy = []forge.User{user("rae")}
		f.mrDetail[d.Ref] = d
		for i := range f.mrs {
			if f.mrs[i].Ref == ref("g/api!2") {
				f.mrs[i].State = "merged"
			}
		}
		f.mrs = append(f.mrs, forge.MR{
			Ref: ref("g/api!6"), Title: "Please review", State: "opened",
			Author: user("rae"), Reviewers: []forge.User{user("me")}, WebURL: "https://forge.test/g/api/-/merge_requests/6",
		})
		f.mrDetail[ref("g/api!6")] = f.mrs[len(f.mrs)-1]
	})
	h.sync()
	second := kinds(got())

	want := map[string]events.Severity{
		EvIssueAssigned: events.Info, EvIterationMoved: events.Info, EvCIPassed: events.Success,
		EvMRComment: events.Info, EvMRApproved: events.Success, EvMRMerged: events.Success,
		EvReviewAsked: events.Info, EvSyncDone: events.Info,
	}
	for kind, sev := range want {
		if len(second[kind]) != 1 {
			t.Errorf("%s raised %d times, want 1 (all: %v)", kind, len(second[kind]), second)
			continue
		}
		if second[kind][0].Severity != sev || (kind != EvSyncDone && second[kind][0].Project != "demo") {
			t.Errorf("%s = %+v", kind, second[kind][0])
		}
	}
	if len(second) != len(want) {
		t.Errorf("unexpected kinds: %v", second)
	}
	if e := second[EvMRComment]; len(e) == 1 && (e[0].Subject != "g/tracker#1" || e[0].Title != "new thread on api!1" || e[0].Link == "") {
		t.Errorf("mr.comment = %+v", e[0])
	}
	if e := second[EvIssueAssigned]; len(e) == 1 && (e[0].Subject != "g/tracker#6" || e[0].Link == "") {
		t.Errorf("issue.assigned = %+v", e[0])
	}

	h.f.update(func(f *memForge) {
		d := f.mrDetail[ref("g/api!1")]
		d.Pipeline = &forge.Pipeline{ID: 1, Status: "failed"}
		f.mrDetail[d.Ref] = d
	})
	h.sync()
	third := kinds(got())
	if len(third[EvCIFailed]) != 1 || third[EvCIFailed][0].Severity != events.Error || len(third) != 2 {
		t.Errorf("third sync = %v", third)
	}
}

// A project whose first sync failed has no baseline: when it recovers, its
// items are not news (no "assigned to you" for every item).
func TestRecoveryAfterFailedFirstSyncIsQuiet(t *testing.T) {
	h := newHarness(t, manifest("demo", "demo", "tracker", "milestones"))
	seed(h.f)
	h.f.failProject = "g/tracker"
	got := h.listen()
	h.sync()
	got()
	h.f.update(func(f *memForge) { f.failProject = "" })
	h.sync()
	if ev := kinds(got()); len(ev[EvIssueAssigned]) != 0 {
		t.Errorf("recovery raised %d issue.assigned events, want 0", len(ev[EvIssueAssigned]))
	}
}

func TestSyncFailedEvent(t *testing.T) {
	h := newHarness(t, manifest("a", "alpha", "tracker", "milestones"), manifest("b", "beta", "broken", "milestones"))
	seed(h.f)
	h.f.failProject = "g/broken"
	got := h.listen()
	h.sync()

	ev := kinds(got())
	if len(ev[EvSyncDone]) != 0 || len(ev[EvSyncFailed]) != 1 {
		t.Fatalf("events = %v", ev)
	}
	e := ev[EvSyncFailed][0]
	if e.Severity != events.Warn || e.Transient || !strings.Contains(e.Detail, "beta") {
		t.Errorf("sync.failed = %+v", e)
	}

	// The same failure again is status, not news: it must not refill the inbox.
	h.sync()
	again := kinds(got())[EvSyncFailed]
	if len(again) != 1 || !again[0].Transient {
		t.Errorf("repeat failure = %+v, want one transient sync.failed", again)
	}
}

func TestRecordsWrittenOnlyWhenUpdatedAtChanges(t *testing.T) {
	h := newHarness(t, manifest("demo", "demo", "tracker", "milestones"))
	seed(h.f)
	t1 := ref("g/tracker#1")
	h.f.discussions[t1] = []forge.Discussion{note("Please add a burst test")}
	item := filepath.Join(records.New(h.app.RecordsDir).ItemDir("f", t1), records.ItemFile)

	h.sync()
	body, err := os.ReadFile(item)
	if err != nil {
		t.Fatalf("record not written: %v", err)
	}
	if !strings.Contains(string(body), "Please add a burst test") {
		t.Errorf("record lacks the tracker comment:\n%s", body)
	}
	if n := h.f.discCalls[t1]; n != 1 {
		t.Fatalf("notes fetched %d times", n)
	}

	mine := string(body) + "my own note survives\n"
	if err := os.WriteFile(item, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	h.sync() // nothing changed: no fetch, no write
	if n := h.f.discCalls[t1]; n != 1 {
		t.Errorf("unchanged item re-fetched notes (%d calls)", n)
	}
	if b, _ := os.ReadFile(item); string(b) != mine {
		t.Errorf("record rewritten without an UpdatedAt change")
	}

	h.f.update(func(f *memForge) { f.issues[0].UpdatedAt = testNow })
	h.sync()
	if n := h.f.discCalls[t1]; n != 2 {
		t.Errorf("changed item notes fetched %d times, want 2", n)
	}
	b, _ := os.ReadFile(item)
	if !strings.Contains(string(b), "my own note survives") {
		t.Errorf("user notes lost:\n%s", b)
	}
	if n := h.f.discCalls[ref("g/tracker#2")]; n != 1 {
		t.Errorf("untouched item tracker#2 fetched %d times", n)
	}
}

func TestRecordsSkipNotesFetchWhenListingSaysNoComments(t *testing.T) {
	h := newHarness(t, manifest("demo", "demo", "tracker", "milestones"))
	seed(h.f)
	none, one := 0, 1
	t1, t2 := ref("g/tracker#1"), ref("g/tracker#2")
	for i := range h.f.issues {
		switch h.f.issues[i].Ref {
		case t1:
			h.f.issues[i].Comments = &one
		case t2:
			h.f.issues[i].Comments = &none
		}
	}
	h.f.discussions[t1] = []forge.Discussion{note("Please add a burst test")}
	h.sync()

	if n := h.f.discCalls[t2]; n != 0 {
		t.Errorf("issue without comments fetched notes %d times", n)
	}
	if n := h.f.discCalls[t1]; n != 1 {
		t.Errorf("issue with a comment fetched notes %d times, want 1", n)
	}
	for _, r := range []forge.Ref{t1, t2} {
		if _, err := os.Stat(filepath.Join(records.New(h.app.RecordsDir).ItemDir("f", r), records.ItemFile)); err != nil {
			t.Errorf("record for %s missing: %v", r, err)
		}
	}
}

func TestOpenFactory(t *testing.T) {
	if _, err := openForge(context.Background(), "gh", config.Forge{Kind: "github"}); err == nil || !strings.Contains(err.Error(), "forge gh") {
		t.Errorf("github without a token: err = %v, want a token error naming the forge", err)
	}
	t.Setenv("ADE_TEST_GH_TOKEN", "tok")
	f, err := openForge(context.Background(), "gh", config.Forge{Kind: "github", TokenEnv: "ADE_TEST_GH_TOKEN"})
	if err != nil || f.Kind() != "github" {
		t.Errorf("github with a token: %v, %v", f, err)
	}
	if _, err := openForge(context.Background(), "x", config.Forge{Kind: "svn"}); err == nil {
		t.Error("unknown kind accepted")
	}
}

func TestManifestFetchFailureIsReported(t *testing.T) {
	h := newHarness(t)
	h.app.Config.Projects = []config.ProjectRef{{Source: filepath.Join(h.dir, "missing.ade.toml")}}
	res := h.sync()
	if len(res) != 1 || res[0].Err == "" {
		t.Errorf("results = %+v", res)
	}
}

func TestMRTargets(t *testing.T) {
	mr := forge.MR{
		Ref:          ref("g/api!7"),
		Description:  "Fixes #3 and closes g/web#4.\nResolves: #3\nmentions #99",
		SourceBranch: "12-slug",
	}
	got := mrTargets(mr)
	want := []forge.Ref{ref("g/api#3"), ref("g/web#4"), ref("g/api#12")}
	if !slices.Equal(got, want) {
		t.Errorf("targets = %v, want %v", got, want)
	}
}

// A sync that keeps failing raises sync.stalled once the last good sync is
// older than notify.sync_stalled_after, once per outage, and again for the
// next outage.
func TestSyncStalledEvent(t *testing.T) {
	h := newHarness(t, manifest("a", "alpha", "tracker", "milestones"))
	seed(h.f)
	now := testNow
	h.svc.Now = func() time.Time { return now }
	h.app.Config.Notify.SyncStalledAfter = config.Duration{Duration: 2 * time.Hour}
	got := h.listen()
	h.sync() // good sync at testNow
	got()

	h.f.update(func(f *memForge) { f.failProject = "g/tracker" })
	now = testNow.Add(time.Hour)
	h.sync()
	if ev := kinds(got()); len(ev[EvSyncStalled]) != 0 {
		t.Fatalf("an outage of 1h raised sync.stalled: %v", ev[EvSyncStalled])
	}

	now = testNow.Add(3 * time.Hour)
	h.sync()
	ev := kinds(got())[EvSyncStalled]
	if len(ev) != 1 {
		t.Fatalf("sync.stalled events = %v, want 1", ev)
	}
	e := ev[0]
	if e.Severity != events.Warn || e.Transient || e.Project != "alpha" || !strings.Contains(e.Title, "3h0m0s") || !strings.Contains(e.Detail, "boom") {
		t.Errorf("sync.stalled = %+v", e)
	}

	now = testNow.Add(5 * time.Hour)
	h.sync()
	if ev := kinds(got()); len(ev[EvSyncStalled]) != 0 {
		t.Errorf("the same outage raised sync.stalled again: %v", ev[EvSyncStalled])
	}

	// Recovery ends the outage; a new one raises it again.
	h.f.update(func(f *memForge) { f.failProject = "" })
	now = testNow.Add(6 * time.Hour)
	h.sync()
	got()
	h.f.update(func(f *memForge) { f.failProject = "g/tracker" })
	now = testNow.Add(9 * time.Hour)
	h.sync()
	if ev := kinds(got()); len(ev[EvSyncStalled]) != 1 {
		t.Errorf("second outage raised %d sync.stalled events, want 1", len(ev[EvSyncStalled]))
	}
}

func TestSyncStalledOff(t *testing.T) {
	h := newHarness(t, manifest("a", "alpha", "tracker", "milestones"))
	seed(h.f)
	now := testNow
	h.svc.Now = func() time.Time { return now }
	h.app.Config.Notify.SyncStalledAfter = config.Duration{}
	h.sync()
	h.f.update(func(f *memForge) { f.failProject = "g/tracker" })
	got := h.listen()
	now = testNow.Add(72 * time.Hour)
	h.sync()
	if ev := kinds(got()); len(ev[EvSyncStalled]) != 0 {
		t.Errorf("sync_stalled_after = 0 still raised %v", ev[EvSyncStalled])
	}
}
