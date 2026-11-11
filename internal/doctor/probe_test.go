package doctor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

const (
	probeGroup   = fake.GroupPath
	probeTracker = probeGroup + "/tracker"
)

func probeConfig(t *testing.T, url, token string) Options {
	t.Helper()
	t.Setenv("ADE_PROBE_TEST_TOKEN", token)
	return Options{
		Config: &config.Config{Forges: map[string]config.Forge{
			"gl": {Kind: "gitlab", URL: url, TokenEnv: "ADE_PROBE_TEST_TOKEN"},
			"gh": {Kind: "github", URL: "https://github.invalid", TokenEnv: "ADE_PROBE_TEST_TOKEN"},
		}},
		Timeout: 5 * time.Second,
	}
}

func target() ProbeTarget {
	return ProbeTarget{Manifest: "demo", Forge: "gl", Project: probeTracker, Repos: []string{probeGroup + "/api", probeGroup + "/web"}}
}

func endpoint(t *testing.T, r ProbeReport, name string) Endpoint {
	t.Helper()
	for _, e := range r.Endpoints {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no endpoint %q in %+v", name, r.Endpoints)
	return Endpoint{}
}

func shape(t *testing.T, e Endpoint, field string) string {
	t.Helper()
	for _, f := range e.Fields {
		if f.Name == field {
			return f.Shape
		}
	}
	t.Fatalf("endpoint %q reports no field %q: %+v", e.Name, field, e.Fields)
	return ""
}

func TestProbeFreeAndPremium(t *testing.T) {
	for _, tier := range []fake.Tier{fake.Free, fake.Premium} {
		t.Run(tier.String(), func(t *testing.T) {
			srv := fake.New(tier)
			t.Cleanup(srv.Close)
			premium := tier == fake.Premium
			reports := Probe(context.Background(), probeConfig(t, srv.URL, fake.Token), []ProbeTarget{target()})
			if len(reports) != 1 {
				t.Fatalf("%d reports", len(reports))
			}
			r := reports[0]
			if r.Failed() || r.Skipped != "" {
				t.Fatalf("probe failed: %+v", r)
			}

			if e := endpoint(t, r, "current user"); e.Status != 200 || shape(t, e, "username") != "string" {
				t.Errorf("user = %+v", e)
			}
			issues := endpoint(t, r, "issues")
			if issues.Status != 200 || shape(t, issues, "references") != "object" || shape(t, issues, "task_completion_status") != "object" ||
				shape(t, issues, "labels") != "array" || shape(t, issues, "milestone") != "object" {
				t.Errorf("issues = %+v", issues)
			}
			wantWeight, wantIteration := "absent", "absent"
			if premium {
				wantWeight, wantIteration = "number", "object"
			}
			if got := shape(t, issues, "weight"); got != wantWeight {
				t.Errorf("weight = %s, want %s", got, wantWeight)
			}
			if got := shape(t, issues, "iteration"); got != wantIteration {
				t.Errorf("iteration = %s, want %s", got, wantIteration)
			}
			if !contains(issues.Headers, "x-next-page") || !contains(issues.Headers, "link") || contains(issues.Headers, "x-total") {
				t.Errorf("pagination headers = %v", issues.Headers)
			}
			if links := endpoint(t, r, "issue links"); links.Status != 200 || shape(t, links, "link_type") != "string" {
				t.Errorf("links = %+v", links)
			}
			if ms := endpoint(t, r, "group milestones"); shape(t, ms, "start_date") != "string" {
				t.Errorf("milestones = %+v", ms)
			}

			its := endpoint(t, r, "group iterations")
			if premium {
				if its.Status != 200 || shape(t, its, "title") != "null" || shape(t, its, "state") != "number" {
					t.Errorf("iterations = %+v", its)
				}
				joined := strings.Join(its.Notes, "\n")
				for _, want := range []string{"state=upcoming: `state` is the integer code 1", "state=current: `state` is the integer code 2", "state=closed: `state` is the integer code 3"} {
					if !strings.Contains(joined, want) {
						t.Errorf("iteration state codes missing %q in %q", want, joined)
					}
				}
			} else if its.Status != 403 || len(its.Fields) != 0 {
				t.Errorf("Free iterations = %+v, want a bare 403", its)
			}

			// The tracker holds no MR: the probe must move on to a repo.
			if tr := endpoint(t, r, "merge requests ("+probeTracker+")"); tr.Status != 200 || len(tr.Notes) == 0 {
				t.Errorf("tracker MR sample = %+v", tr)
			}
			if mr := endpoint(t, r, "merge requests ("+probeGroup+"/api)"); shape(t, mr, "pipeline") != "object" || shape(t, mr, "head_pipeline") != "absent" {
				t.Errorf("api MR list = %+v", mr)
			}
			if mr := endpoint(t, r, "merge request"); shape(t, mr, "head_pipeline") != "object" || shape(t, mr, "draft") != "bool" || shape(t, mr, "detailed_merge_status") != "string" {
				t.Errorf("single MR = %+v", mr)
			}
			appr := endpoint(t, r, "merge request approvals")
			if shape(t, appr, "approvals_required") != "number" || shape(t, appr, "merge_request_approvers_available") != "bool" {
				t.Errorf("approvals = %+v", appr)
			}
			if d := endpoint(t, r, "merge request discussions"); shape(t, d, "notes[].resolvable") != "bool" || shape(t, d, "notes") != "array" {
				t.Errorf("discussions = %+v", d)
			}
			if rules := endpoint(t, r, "approval rules"); rules.Status != 200 || (len(rules.Fields) > 0) != premium {
				t.Errorf("approval rules = %+v", rules)
			}

			if r.Caps == nil || r.CapsError != "" {
				t.Fatalf("caps = %+v, %q", r.Caps, r.CapsError)
			}
			c := r.Caps
			for name, got := range map[string]bool{
				"iterations": c.Iterations, "cadences": c.Cadences, "weight": c.Weight,
				"approval_rules": c.ApprovalRules, "blocks_links": c.BlocksLinks, "epics": c.Epics,
			} {
				if got != premium {
					t.Errorf("caps.%s = %v, want %v", name, got, premium)
				}
			}
		})
	}
}

// The report is shape only: nothing a user wrote and no credential may appear
// in it, in either output form.
func TestProbeReportsNoValues(t *testing.T) {
	srv := fake.New(fake.Premium)
	t.Cleanup(srv.Close)
	reports := Probe(context.Background(), probeConfig(t, srv.URL, fake.Token), []ProbeTarget{target()})
	b, err := json.Marshal(reports)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, leak := range []string{
		fake.Token, "Rate-limit the export endpoint", "sandbox-dev", "Sandbox Dev", "reviewer-rae", "Tenants can trigger",
		"Self-serve exports", "2026-11-02", "cba12b9f93fb", "1-rate-limit",
	} {
		if strings.Contains(out, leak) {
			t.Errorf("report leaks %q", leak)
		}
	}
	// Paths are templates, never concrete.
	if strings.Contains(out, "%2F") || strings.Contains(out, "/issues/1") {
		t.Errorf("report contains a concrete path: %s", out)
	}
}

func TestProbeFailures(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	ctx := context.Background()

	t.Run("rejected token", func(t *testing.T) {
		r := Probe(ctx, probeConfig(t, srv.URL, "wrong"), []ProbeTarget{target()})[0]
		if !r.Failed() || endpoint(t, r, "current user").Status != 401 {
			t.Errorf("report = %+v", r)
		}
	})
	t.Run("gated 403 alone is a finding, not a failure", func(t *testing.T) {
		r := Probe(ctx, probeConfig(t, srv.URL, fake.Token), []ProbeTarget{target()})[0]
		if endpoint(t, r, "group iterations").Status != 403 || r.Failed() {
			t.Errorf("Failed = %v for %+v", r.Failed(), r)
		}
	})
	t.Run("unreachable host", func(t *testing.T) {
		dead := fake.New(fake.Free)
		url := dead.URL
		dead.Close()
		r := Probe(ctx, probeConfig(t, url, fake.Token), []ProbeTarget{target()})[0]
		if !r.Failed() || endpoint(t, r, "current user").Error == "" {
			t.Errorf("report = %+v", r)
		}
		if strings.Contains(endpoint(t, r, "current user").Error, fake.Token) {
			t.Error("transport error leaks the token")
		}
		if r.CapsError == "" || r.Caps != nil {
			t.Errorf("caps = %+v / %q", r.Caps, r.CapsError)
		}
	})
	t.Run("unknown forge", func(t *testing.T) {
		tg := target()
		tg.Forge = "nope"
		if r := Probe(ctx, probeConfig(t, srv.URL, fake.Token), []ProbeTarget{tg})[0]; !r.Failed() || !strings.Contains(r.Err, `"nope"`) {
			t.Errorf("report = %+v", r)
		}
	})
	t.Run("missing token", func(t *testing.T) {
		o := probeConfig(t, srv.URL, "")
		if r := Probe(ctx, o, []ProbeTarget{target()})[0]; !r.Failed() || r.Err == "" || len(r.Endpoints) != 0 {
			t.Errorf("report = %+v", r)
		}
	})
	t.Run("unreadable manifest passes through", func(t *testing.T) {
		tg := ProbeTarget{Manifest: "bad.toml", Err: "boom"}
		if r := Probe(ctx, probeConfig(t, srv.URL, fake.Token), []ProbeTarget{tg})[0]; !r.Failed() || r.Err != "boom" || len(r.Endpoints) != 0 {
			t.Errorf("report = %+v", r)
		}
	})
	t.Run("github is skipped, not failed", func(t *testing.T) {
		tg := target()
		tg.Forge = "gh"
		if r := Probe(ctx, probeConfig(t, srv.URL, fake.Token), []ProbeTarget{tg})[0]; r.Failed() || r.Skipped == "" || len(r.Endpoints) != 0 {
			t.Errorf("report = %+v", r)
		}
	})
	t.Run("no config", func(t *testing.T) {
		if r := Probe(ctx, Options{}, []ProbeTarget{target()})[0]; !r.Failed() {
			t.Errorf("report = %+v", r)
		}
	})
}

// A tracker with no merge request of its own and no repos to fall back on
// reports that nothing could be sampled rather than failing.
func TestProbeWithoutMergeRequestSample(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	tg := target()
	tg.Repos = nil
	r := Probe(context.Background(), probeConfig(t, srv.URL, fake.Token), []ProbeTarget{tg})[0]
	if r.Failed() {
		t.Fatalf("failed: %+v", r)
	}
	skipped := endpoint(t, r, "merge requests")
	if skipped.Status != 0 || len(skipped.Notes) != 1 || !strings.Contains(skipped.Notes[0], "skipped") {
		t.Errorf("no-sample note = %+v", skipped)
	}
}

func TestShapeOf(t *testing.T) {
	o := object{
		"s": json.RawMessage(`"x"`), "n": json.RawMessage(`3`), "f": json.RawMessage(`-1.5`), "b": json.RawMessage(`false`),
		"z": json.RawMessage(`null`), "o": json.RawMessage(`{"a":1}`), "a": json.RawMessage(`[1]`),
		"notes": json.RawMessage(`[{"resolvable":true,"position":null},{"resolvable":false}]`),
		"empty": json.RawMessage(`[]`), "scalar": json.RawMessage(`7`),
	}
	tests := map[string]string{
		"s": "string", "n": "number", "f": "number", "b": "bool", "z": "null", "o": "object", "a": "array",
		"missing": "absent", "notes[].resolvable": "bool", "notes[].position": "null", "notes[].nope": "absent",
		"empty[].x": "absent", "scalar[].x": "absent", "missing[].x": "absent",
	}
	for field, want := range tests {
		if got := shapeOf(o, field); got != want {
			t.Errorf("shapeOf(%q) = %q, want %q", field, got, want)
		}
	}
}

func TestObjects(t *testing.T) {
	if o, err := objects([]byte(` [{"a":1},{"a":2}] `)); err != nil || len(o) != 2 || string(o[0]["a"]) != "1" {
		t.Errorf("list: %v, %v", o, err)
	}
	if o, err := objects([]byte(`[]`)); err != nil || len(o) != 0 {
		t.Errorf("empty list: %v, %v", o, err)
	}
	if o, err := objects([]byte(`{"a":1}`)); err != nil || len(o) != 1 || string(o[0]["a"]) != "1" {
		t.Errorf("object: %v, %v", o, err)
	}
	for _, bad := range []string{`<html>`, `[1,2]`, ``} {
		if _, err := objects([]byte(bad)); err == nil {
			t.Errorf("objects(%q) accepted", bad)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
