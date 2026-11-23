package forgesync

import (
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
)

// A single-repo project lists the tracker project as its own code repo
// ([repos.mono] path = tracker). Every issue is one work item, with the MRs
// that close it attached; the repo is not read twice into children of
// themselves.
func TestSyncTrackerThatIsAlsoTheCodeRepo(t *testing.T) {
	body := strings.Replace(manifestTmpl, "[repos.api]\npath = \"g/api\"\napp  = [\"api\"]\n\n[repos.web]\npath = \"g/web\"\napp  = [\"web\"]\n",
		"[repos.mono]\npath = \"g/mono\"\napp  = [\"mono\"]\n", 1)
	if !strings.Contains(body, "[repos.mono]") {
		t.Fatal("manifest template changed")
	}
	h := newHarness(t, manifestFile{"mono", strings.NewReplacer("NAME", "mono", "TRACKER", "mono", "SOURCE", "milestones").Replace(body)})

	s42 := milestone(42, "Sprint 42", "2026-11-02", "2026-11-13")
	h.f.milestones = []forge.Milestone{s42}
	closes := forge.MR{
		Ref: ref("g/mono!1"), Title: "Rate limit", State: "opened", SourceBranch: "rate-limit",
		Description: "Closes #1", Author: user("me"), UpdatedAt: testNow,
	}
	byBranch := forge.MR{
		Ref: ref("g/mono!2"), Title: "Draft: Paginate", State: "opened", Draft: true,
		SourceBranch: "2-paginate", Author: user("me"), UpdatedAt: testNow,
	}
	h.f.issues = []forge.Issue{
		issue("g/mono#1", "Rate-limit exports", &s42, "me"),
		issue("g/mono#2", "Paginate audit log", &s42, "me"),
		issue("g/mono#3", "Someone else's", &s42, "rae"),
	}
	h.f.mrs = []forge.MR{closes, byBranch}
	h.f.mrDetail = map[forge.Ref]forge.MR{closes.Ref: closes, byBranch.Ref: byBranch}

	res := h.sync()
	if len(res) != 1 || res[0].Err != "" {
		t.Fatalf("results = %+v", res)
	}
	want := []string{"g/mono#1", "g/mono#2", "g/mono#3"}
	if got := refs(res[0]); !slices.Equal(got, want) {
		t.Fatalf("items = %v, want %v: each issue is one work item, none twice", got, want)
	}
	items := byRef(res[0])
	for _, ref := range want {
		if n := len(items[ref].Children); n != 0 {
			t.Errorf("%s has %d children: the tracker issue is the work item itself", ref, n)
		}
	}
	if mrs := items["g/mono#1"].MRs; len(mrs) != 1 || mrs[0].Ref.String() != "g/mono!1" {
		t.Errorf("mono#1 MRs = %+v", mrs)
	}
	if mrs := items["g/mono#2"].MRs; len(mrs) != 1 || mrs[0].Ref.String() != "g/mono!2" {
		t.Errorf("mono#2 MRs = %+v", mrs)
	}
	if len(items["g/mono#3"].MRs) != 0 {
		t.Errorf("mono#3 MRs = %+v", items["g/mono#3"].MRs)
	}
}
