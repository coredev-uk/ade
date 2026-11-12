package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/records"
)

// itemWithMR is a tracker item whose MR in api comes from branch.
func itemWithMR(iid int, branch string) domain.WorkItem {
	wi := adoptItem7()
	wi.Ref.IID = iid
	wi.Issue.Ref = wi.Ref
	wi.Children = nil
	wi.MRs = []forge.MR{{Ref: forge.Ref{Project: adoptAPI, IID: iid, Kind: forge.KindMR}, State: "opened", SourceBranch: branch}}
	return wi
}

type itemsJSON struct {
	Ref       forge.Ref      `json:"ref"`
	MRs       []forge.MR     `json:"mrs"`
	Worktrees []itemWorktree `json:"worktrees"`
}

func TestItemsJSONListsWorktreesOfEachItem(t *testing.T) {
	e := newCLIEnv(t)
	api, _ := e.gcLayout()
	branch := filepath.Base(api)
	e.cacheDemoGL(itemWithMR(1, branch), itemWithMR(2, "other-branch"))
	if err := os.WriteFile(filepath.Join(api, "wip.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	asJSON(t)

	out, err := e.run(newItemsCmd)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var items []itemsJSON
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) != 2 {
		t.Fatalf("json = %v\n%s", err, out)
	}
	byIID := map[int]itemsJSON{items[0].Ref.IID: items[0], items[1].Ref.IID: items[1]}
	if len(byIID[1].MRs) != 1 {
		t.Errorf("the item lost its mrs: %+v", byIID[1])
	}
	want := itemWorktree{Repo: "api", Path: api, Branch: branch, Dirty: true, HasUpstream: true}
	if got := byIID[1].Worktrees; len(got) != 1 || got[0] != want {
		t.Errorf("item 1 worktrees = %+v, want [%+v]", got, want)
	}
	if !strings.Contains(out, `"worktrees": []`) || len(byIID[2].Worktrees) != 0 {
		t.Errorf("an item without a worktree must give an empty list:\n%s", out)
	}
}

func TestItemShowsWorktrees(t *testing.T) {
	e := newCLIEnv(t)
	e.app.RecordsDir = t.TempDir()
	api, _ := e.gcLayout()
	wi := itemWithMR(1, filepath.Base(api))
	e.cacheDemoGL(wi)
	if _, _, err := records.New(e.app.RecordsDir).WriteSnapshot("sandbox", wi, nil, 5); err != nil {
		t.Fatal(err)
	}
	ref := adoptTracker + "#1"

	out, err := e.item(ref)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "worktrees:\n  api  " + filepath.Base(api) + "  " + api + "  clean, in sync\n"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}

	asJSON(t)
	out, err = e.item(ref)
	var js itemOut
	if err != nil || json.Unmarshal([]byte(out), &js) != nil || len(js.Worktrees) != 1 || js.Worktrees[0].Path != api || !js.Worktrees[0].HasUpstream {
		t.Fatalf("json = %+v err %v\n%s", js, err, out)
	}
}
