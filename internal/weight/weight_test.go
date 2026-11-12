package weight_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
	ghfake "github.com/coredev-uk/ade/internal/forge/github/fake"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/weight"
)

const (
	tracker = fake.GroupPath + "/tracker"
	apiProj = fake.GroupPath + "/api"
)

func iref(project string, iid int) forge.Ref {
	return forge.Ref{Project: project, IID: iid, Kind: forge.KindIssue}
}

func ptr(n int) *int { return &n }

func TestBuildPlan(t *testing.T) {
	label := config.WeightSource{Source: "label", Pattern: "weight::{value}"}
	issue := func(labels ...string) forge.Issue { return forge.Issue{Ref: iref(tracker, 1), Labels: labels} }
	cases := []struct {
		name       string
		is         forge.Issue
		caps       forge.Caps
		src        config.WeightSource
		to         *int
		wantMode   string
		add, rm    []string
		unchanged  bool
		wantErr    string
		fromWeight *int
	}{
		{
			name: "swap the label and remove the old one", is: issue("type::story", "weight::5"), src: label, to: ptr(2),
			wantMode: weight.ModeLabel, add: []string{"weight::2"}, rm: []string{"weight::5"}, fromWeight: ptr(5),
		},
		{name: "first weight adds only", is: issue("type::story"), src: label, to: ptr(3), wantMode: weight.ModeLabel, add: []string{"weight::3"}},
		{
			name: "every old weight label goes, even stray ones", is: issue("weight::1", "weight::8", "weight::2"), src: label, to: ptr(2),
			wantMode: weight.ModeLabel, rm: []string{"weight::1", "weight::8"}, fromWeight: ptr(8),
		},
		{name: "same weight is a no-op", is: issue("weight::2"), src: label, to: ptr(2), wantMode: weight.ModeLabel, unchanged: true, fromWeight: ptr(2)},
		{name: "none removes all", is: issue("weight::3", "app::api"), src: label, wantMode: weight.ModeLabel, rm: []string{"weight::3"}, fromWeight: ptr(3)},
		{name: "none on unweighted is a no-op", is: issue("app::api"), src: label, wantMode: weight.ModeLabel, unchanged: true},
		{name: "a non-numeric value is not a weight label", is: issue("weight::big"), src: label, to: ptr(1), wantMode: weight.ModeLabel, add: []string{"weight::1"}},
		{name: "default pattern", is: issue(), src: config.WeightSource{Source: "label"}, to: ptr(5), wantMode: weight.ModeLabel, add: []string{"weight::5"}},
		{
			name: "auto on Free uses labels", is: issue("weight::1"), src: config.WeightSource{Source: "auto"}, to: ptr(2),
			wantMode: weight.ModeLabel, add: []string{"weight::2"}, rm: []string{"weight::1"}, fromWeight: ptr(1),
		},
		{
			name: "auto on Premium writes the field", is: forge.Issue{Ref: iref(tracker, 1), Weight: ptr(3)}, caps: forge.Caps{Weight: true},
			src: config.WeightSource{Source: "auto"}, to: ptr(5), wantMode: weight.ModeField, fromWeight: ptr(3),
		},
		{
			name: "field already set", is: forge.Issue{Ref: iref(tracker, 1), Weight: ptr(3)}, caps: forge.Caps{Weight: true},
			src: config.WeightSource{Source: "gitlab"}, to: ptr(3), wantMode: weight.ModeField, unchanged: true, fromWeight: ptr(3),
		},
		{name: "label source wins on Premium", is: issue(), caps: forge.Caps{Weight: true}, src: label, to: ptr(2), wantMode: weight.ModeLabel, add: []string{"weight::2"}},
		{name: "gitlab source without the licence", is: issue(), src: config.WeightSource{Source: "gitlab"}, to: ptr(2), wantErr: "does not license"},
		{name: "none source", is: issue(), src: config.WeightSource{Source: "none"}, to: ptr(2), wantErr: `"none"`},
		{name: "zero is refused", is: issue(), src: label, to: ptr(0), wantErr: "1 or more"},
		{name: "negative is refused", is: issue(), src: label, to: ptr(-3), wantErr: "1 or more"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := weight.BuildPlan(tc.is, tc.caps, tc.src, tc.to)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Mode != tc.wantMode || p.Unchanged != tc.unchanged {
				t.Errorf("mode/unchanged = %s/%v, want %s/%v", p.Mode, p.Unchanged, tc.wantMode, tc.unchanged)
			}
			if !slices.Equal(p.Add, tc.add) || !slices.Equal(p.Remove, tc.rm) {
				t.Errorf("add/remove = %v/%v, want %v/%v", p.Add, p.Remove, tc.add, tc.rm)
			}
			if weight.Value(p.From) != weight.Value(tc.fromWeight) {
				t.Errorf("from = %s, want %s", weight.Value(p.From), weight.Value(tc.fromWeight))
			}
		})
	}
}

// harness is a fake GitLab of one tier with the tracker manifest and an audit log.
type harness struct {
	t   *testing.T
	c   *gitlab.Client
	srv *fake.Server
	app *app.App
	m   *config.Manifest
}

func newHarness(t *testing.T, tier fake.Tier, src config.WeightSource, opts ...fake.Option) *harness {
	t.Helper()
	clock := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	srv := fake.New(tier, append([]fake.Option{fake.WithClock(clock)}, opts...)...)
	t.Cleanup(srv.Close)
	log, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	return &harness{
		t: t, srv: srv, c: gitlab.New(srv.URL, fake.Token, gitlab.WithClock(func() time.Time { return clock })),
		app: &app.App{Audit: log},
		m:   &config.Manifest{Name: "demo", Forge: "gl", Tracker: config.Tracker{Project: tracker, Weight: src}},
	}
}

func (h *harness) target(iid int) *weight.Target {
	h.t.Helper()
	tg, err := weight.ResolveWith(context.Background(), h.c, h.m, iref(tracker, iid))
	if err != nil {
		h.t.Fatal(err)
	}
	return tg
}

func (h *harness) auditEntries() []audit.Entry {
	h.t.Helper()
	es, err := h.app.Audit.Query(audit.Filter{Action: "forge.issue.weight"})
	if err != nil {
		h.t.Fatal(err)
	}
	return es
}

func TestFreeSwapsLabelsAndChildFollows(t *testing.T) {
	h := newHarness(t, fake.Free, config.WeightSource{Source: "label", Pattern: "weight::{value}"})
	ctx := context.Background()
	// tracker#1 has child api#1 (Parent: line plus relates_to link). Weight both
	// as the team does on Free, then re-weight.
	if err := h.c.SetLabels(ctx, iref(tracker, 1), []string{"weight::5"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.c.SetLabels(ctx, iref(apiProj, 1), []string{"weight::5"}, nil); err != nil {
		t.Fatal(err)
	}
	// An issue that is only related (no Parent: line) must not follow.
	other, err := h.c.CreateIssue(ctx, apiProj, forge.IssueDraft{Title: "Related only", Labels: []string{"weight::8"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.c.LinkIssues(ctx, iref(tracker, 1), other.Ref, "relates_to"); err != nil {
		t.Fatal(err)
	}

	tg := h.target(1)
	if len(tg.Children) != 1 || tg.Children[0].Issue.Ref != iref(apiProj, 1) {
		t.Fatalf("children = %+v, want only api#1", tg.Children)
	}
	plans, err := tg.Plans(ptr(2), true)
	if err != nil || len(plans) != 2 || !plans[1].Child {
		t.Fatalf("plans = %+v, %v", plans, err)
	}
	res, err := weight.Apply(ctx, h.c, h.app, "demo", "", plans)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || !res[0].Applied || !res[1].Applied {
		t.Fatalf("results = %+v", res)
	}
	for _, ref := range []forge.Ref{iref(tracker, 1), iref(apiProj, 1)} {
		ls, _ := h.srv.IssueLabels(ref)
		if !slices.Contains(ls, "weight::2") || slices.Contains(ls, "weight::5") {
			t.Errorf("%s labels = %v, want weight::2 and no weight::5", ref, ls)
		}
	}
	if ls, _ := h.srv.IssueLabels(other.Ref); !slices.Contains(ls, "weight::8") {
		t.Errorf("the merely related issue was re-weighted: %v", ls)
	}
	es := h.auditEntries()
	if len(es) != 2 || es[0].Target != tracker+"#1" || es[0].Outcome != "ok" ||
		es[0].Params["from"] != "5" || es[0].Params["to"] != "2" || es[0].Params["mode"] != weight.ModeLabel || es[1].Params["child"] != true {
		t.Errorf("audit = %+v", es)
	}

	// Re-running is a no-op: nothing is written or audited.
	plans, _ = h.target(1).Plans(ptr(2), true)
	if res, err = weight.Apply(ctx, h.c, h.app, "demo", "", plans); err != nil || res[0].Applied || res[1].Applied {
		t.Fatalf("repeat: %+v, %v", res, err)
	}
	if n := len(h.auditEntries()); n != 2 {
		t.Errorf("repeat audited %d entries, want 2 in total", n)
	}

	// Back to unweighted: every weight label goes.
	plans, _ = h.target(1).Plans(nil, false)
	if _, err := weight.Apply(ctx, h.c, h.app, "demo", "", plans); err != nil {
		t.Fatal(err)
	}
	if ls, _ := h.srv.IssueLabels(iref(tracker, 1)); slices.ContainsFunc(ls, func(l string) bool { return strings.HasPrefix(l, "weight::") }) {
		t.Errorf("labels after none = %v", ls)
	}
}

func TestPremiumWritesTheField(t *testing.T) {
	h := newHarness(t, fake.Premium, config.WeightSource{Source: "auto"})
	ctx := context.Background()
	tg := h.target(8) // Scheduled exports, weight 8
	if !tg.Caps.Weight {
		t.Fatal("the fake Premium group should license weight")
	}
	plans, err := tg.Plans(ptr(5), false)
	if err != nil || plans[0].Mode != weight.ModeField || weight.Value(plans[0].From) != "8" {
		t.Fatalf("plan = %+v, %v", plans, err)
	}
	if _, err := weight.Apply(ctx, h.c, h.app, "demo", "", plans); err != nil {
		t.Fatal(err)
	}
	if is, _ := h.c.Issue(ctx, iref(tracker, 8)); is.Weight == nil || *is.Weight != 5 {
		t.Errorf("weight = %v, want 5", is.Weight)
	}
	plans, _ = h.target(8).Plans(nil, false)
	if _, err := weight.Apply(ctx, h.c, h.app, "demo", "", plans); err != nil {
		t.Fatal(err)
	}
	if is, _ := h.c.Issue(ctx, iref(tracker, 8)); is.Weight != nil {
		t.Errorf("weight after none = %d, want unweighted", *is.Weight)
	}
	es := h.auditEntries()
	if len(es) != 2 || es[0].Params["mode"] != weight.ModeField || es[1].Params["to"] != "none" {
		t.Errorf("audit = %+v", es)
	}
}

// In a user namespace weight is read from the tracker project, so a licensed
// account still writes the field and never falls back to labels.
func TestUserNamespaceWritesTheWeightField(t *testing.T) {
	h := newHarness(t, fake.Premium, config.WeightSource{Source: "auto"}, fake.WithUserNamespace())
	ctx := context.Background()
	tg := h.target(8)
	if !tg.Caps.UserNamespace || !tg.Caps.Weight {
		t.Fatalf("caps = %+v, want a user namespace that licenses weight", tg.Caps)
	}
	plans, err := tg.Plans(ptr(5), false)
	if err != nil || plans[0].Mode != weight.ModeField || len(plans[0].Add) != 0 {
		t.Fatalf("plan = %+v, %v", plans, err)
	}
	if _, err := weight.Apply(ctx, h.c, h.app, "demo", "", plans); err != nil {
		t.Fatal(err)
	}
	if is, _ := h.c.Issue(ctx, iref(tracker, 8)); is.Weight == nil || *is.Weight != 5 {
		t.Errorf("weight = %v, want 5", is.Weight)
	}
	if ls, _ := h.srv.IssueLabels(iref(tracker, 8)); slices.ContainsFunc(ls, func(l string) bool { return strings.HasPrefix(l, "weight::") }) {
		t.Errorf("labels = %v, want no weight label", ls)
	}
}

func TestFailedWriteIsAuditedAndStops(t *testing.T) {
	h := newHarness(t, fake.Free, config.WeightSource{Source: "label"})
	ctx := context.Background()
	// A plan for an issue that does not exist: the forge answers 404.
	bad := weight.Plan{Ref: iref(tracker, 999), Mode: weight.ModeLabel, Add: []string{"weight::2"}, To: ptr(2)}
	good := weight.Plan{Ref: iref(tracker, 1), Mode: weight.ModeLabel, Add: []string{"weight::3"}, To: ptr(3)}
	res, err := weight.Apply(ctx, h.c, h.app, "demo", "", []weight.Plan{bad, good})
	if err == nil || len(res) != 1 || res[0].Applied || res[0].Error == "" {
		t.Fatalf("res = %+v, err = %v; want one failed result and the error", res, err)
	}
	if ls, _ := h.srv.IssueLabels(iref(tracker, 1)); slices.Contains(ls, "weight::3") {
		t.Error("the plan after the failure was applied")
	}
	if es := h.auditEntries(); len(es) != 1 || es[0].Outcome != "error" || es[0].Error == "" {
		t.Errorf("audit = %+v, want one error entry", es)
	}
}

// GitHub has no weight field: auto falls to the label pattern, and the old
// label (weight:5 on the seeded tracker#1) is removed explicitly.
func TestGitHubWeightRidesOnLabels(t *testing.T) {
	srv := ghfake.New()
	t.Cleanup(srv.Close)
	c := github.New(srv.URL, ghfake.Token, github.WithSleep(func(context.Context, time.Duration) error { return nil }))
	log, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	m := &config.Manifest{Name: "demo-gh", Forge: "gh", Tracker: config.Tracker{
		Project: "ade-demo/tracker", Weight: config.WeightSource{Source: "auto", Pattern: "weight:{value}"},
	}}
	ref := iref("ade-demo/tracker", 1)
	tg, err := weight.ResolveWith(context.Background(), c, m, ref)
	if err != nil {
		t.Fatal(err)
	}
	if tg.Caps.Weight {
		t.Fatal("GitHub licenses no weight field")
	}
	plans, err := tg.Plans(ptr(2), true)
	if err != nil || plans[0].Mode != weight.ModeLabel || !slices.Equal(plans[0].Add, []string{"weight:2"}) || !slices.Equal(plans[0].Remove, []string{"weight:5"}) {
		t.Fatalf("plans = %+v, %v", plans, err)
	}
	if _, err := weight.Apply(context.Background(), c, &app.App{Audit: log}, "demo-gh", "", plans[:1]); err != nil {
		t.Fatal(err)
	}
	ls, _ := srv.Labels(ref)
	if !slices.Contains(ls, "weight:2") || slices.Contains(ls, "weight:5") {
		t.Errorf("labels = %v", ls)
	}
	if es, _ := log.Query(audit.Filter{Action: "forge.issue.weight"}); len(es) != 1 || es[0].Params["from"] != "5" || es[0].Params["to"] != "2" {
		t.Errorf("audit = %+v", es)
	}
}
