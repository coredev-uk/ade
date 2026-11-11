package fake_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// get issues a raw request so the fake's wire behaviour is checked without
// the driver in between.
func get(t *testing.T, s *fake.Server, method, path, token, body string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, s.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

const tracker = "/api/v4/projects/ade-demo-env%2Ftracker"

func TestTierGating(t *testing.T) {
	for _, tier := range []fake.Tier{fake.Free, fake.Premium} {
		t.Run(tier.String(), func(t *testing.T) {
			s := fake.New(tier)
			t.Cleanup(s.Close)
			premium := tier == fake.Premium

			code, _, _ := get(t, s, "GET", "/api/v4/groups/ade-demo-env/iterations", fake.Token, "")
			if want := map[bool]int{false: 403, true: 200}[premium]; code != want {
				t.Errorf("iterations = %d, want %d", code, want)
			}

			_, _, body := get(t, s, "GET", tracker+"/issues/1", fake.Token, "")
			var issue map[string]any
			if err := json.Unmarshal(body, &issue); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"weight", "iteration", "epic"} {
				if _, has := issue[key]; has != premium {
					t.Errorf("issue has %q = %v, want %v", key, has, premium)
				}
			}

			// Approval rules answer 200 on every tier, as on gitlab.com Free.
			code, _, body = get(t, s, "GET", tracker+"/approval_rules", fake.Token, "")
			var rules []any
			if err := json.Unmarshal(body, &rules); err != nil || code != 200 || (len(rules) > 0) != premium {
				t.Errorf("approval_rules = %d %s", code, body)
			}

			_, _, body = get(t, s, "GET", "/api/v4/projects/ade-demo-env%2Fapi/merge_requests/1/approvals", fake.Token, "")
			var appr map[string]any
			if err := json.Unmarshal(body, &appr); err != nil {
				t.Fatal(err)
			}
			if appr["merge_request_approvers_available"] != premium || (appr["approvals_required"] == float64(2)) != premium {
				t.Errorf("approvals = %v", appr)
			}
		})
	}
}

func TestProtocolBehaviour(t *testing.T) {
	s := fake.New(fake.Free)
	t.Cleanup(s.Close)

	if code, _, _ := get(t, s, "GET", "/api/v4/user", "", ""); code != 401 {
		t.Errorf("no token = %d, want 401", code)
	}
	if code, _, _ := get(t, s, "GET", "/api/v4/user", "wrong", ""); code != 401 {
		t.Errorf("wrong token = %d, want 401", code)
	}
	if code, _, _ := get(t, s, "GET", "/api/v4/nothing", fake.Token, ""); code != 404 {
		t.Errorf("unknown route = %d, want 404", code)
	}
	if code, _, _ := get(t, s, "GET", "/api/v4/issues?scope=all", fake.Token, ""); code != 400 {
		t.Errorf("global scope=all = %d, want 400 as on gitlab.com", code)
	}
	if code, _, _ := get(t, s, "GET", tracker+"/issues/999", fake.Token, ""); code != 404 {
		t.Errorf("missing issue = %d", code)
	}
	if code, _, _ := get(t, s, "GET", "/api/v4/projects/1001/issues/1", fake.Token, ""); code != 200 {
		t.Errorf("numeric project id = %d, want 200", code)
	}

	// Pagination: headers, no X-Total.
	code, h, body := get(t, s, "GET", tracker+"/issues?per_page=4&page=2&state=all", fake.Token, "")
	var page []any
	if err := json.Unmarshal(body, &page); err != nil || code != 200 || len(page) != 4 {
		t.Fatalf("page 2 = %d, %d items, %v", code, len(page), err)
	}
	if h.Get("X-Next-Page") != "3" || h.Get("X-Prev-Page") != "1" || h.Get("X-Total") != "" || !strings.Contains(h.Get("Link"), `rel="next"`) {
		t.Errorf("pagination headers = %v", h)
	}
	_, h, _ = get(t, s, "GET", tracker+"/issues?per_page=4&page=3&state=all", fake.Token, "")
	if h.Get("X-Next-Page") != "" {
		t.Errorf("last page announces a next page: %v", h)
	}
}

func TestGraphQLShapes(t *testing.T) {
	query := func(s *fake.Server, vars string) map[string]any {
		_, _, body := get(t, s, "POST", "/api/graphql", fake.Token,
			`{"query":"{ group(fullPath:$path){ iterationCadences(first:1){nodes{id}} workItemTypes(name: EPIC){nodes{name}} } }","variables":`+vars+`}`)
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	free := fake.New(fake.Free)
	t.Cleanup(free.Close)
	out := query(free, `{"path":"ade-demo-env"}`)
	group := out["data"].(map[string]any)["group"].(map[string]any)
	if group["iterationCadences"] != nil || out["errors"] == nil {
		t.Errorf("Free: gated field must be null next to an error: %v", out)
	}
	if nodes := group["workItemTypes"].(map[string]any)["nodes"].([]any); len(nodes) != 0 {
		t.Errorf("Free lists work item types: %v", nodes)
	}

	prem := fake.New(fake.Premium)
	t.Cleanup(prem.Close)
	out = query(prem, `{"path":"ade-demo-env"}`)
	group = out["data"].(map[string]any)["group"].(map[string]any)
	if group["iterationCadences"] == nil || out["errors"] != nil {
		t.Errorf("Premium: %v", out)
	}
	if out := query(prem, `{"path":"ghost"}`); out["data"].(map[string]any)["group"] != nil {
		t.Errorf("unknown group must be null: %v", out)
	}
}

func TestMutatorsValidateTheirTargets(t *testing.T) {
	s := fake.New(fake.Free)
	t.Cleanup(s.Close)
	ghostIssue := forge.Ref{Project: "ade-demo-env/tracker", IID: 99, Kind: forge.KindIssue}
	ghostMR := forge.Ref{Project: "ade-demo-env/api", IID: 99, Kind: forge.KindMR}
	apiMR := forge.Ref{Project: "ade-demo-env/api", IID: 1, Kind: forge.KindMR}

	if err := s.CloseIssue(ghostIssue); err == nil {
		t.Error("closing a missing issue must fail")
	}
	if err := s.SetPipelineStatus(ghostMR, "failed"); err == nil {
		t.Error("pipeline of a missing MR must fail")
	}
	if err := s.MergeMR(ghostMR); err == nil {
		t.Error("merging a missing MR must fail")
	}
	if _, err := s.AddNote(apiMR, "", "nobody", "hi"); err == nil {
		t.Error("note from an unknown user must fail")
	}
	if _, err := s.AddNote(apiMR, "no-such-thread", "sandbox-dev", "hi"); err == nil {
		t.Error("reply to a missing thread must fail")
	}
	if err := s.ResolveThread(apiMR, "no-such-thread", true); err == nil {
		t.Error("resolving a missing thread must fail")
	}
	if err := s.MergeMR(apiMR); err != nil {
		t.Fatal(err)
	}
	if err := s.MergeMR(apiMR); err == nil {
		t.Error("merging twice must fail")
	}
}

func TestAdvanceMovesTimestamps(t *testing.T) {
	s := fake.New(fake.Free)
	t.Cleanup(s.Close)
	s.Advance(3 * 24 * time.Hour)
	ref := forge.Ref{Project: "ade-demo-env/tracker", IID: 3, Kind: forge.KindIssue}
	if err := s.CloseIssue(ref); err != nil {
		t.Fatal(err)
	}
	_, _, body := get(t, s, "GET", tracker+"/issues/3", fake.Token, "")
	var issue struct {
		ClosedAt string `json:"closed_at"`
	}
	if err := json.Unmarshal(body, &issue); err != nil || !strings.HasPrefix(issue.ClosedAt, "2026-11-14T12:00:00") {
		t.Errorf("closed_at = %q (%v), want the advanced clock", issue.ClosedAt, err)
	}
}
