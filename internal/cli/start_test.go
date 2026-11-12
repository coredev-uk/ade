package cli

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/startwork"
)

func resolvedFor(t *testing.T, conv config.Conventions, repos ...string) *startwork.Resolved {
	t.Helper()
	m := &config.Manifest{Name: "demo", Conventions: conv, Repos: map[string]config.Repo{"api": {Path: "g/api"}, "web": {Path: "g/web"}}}
	return &startwork.Resolved{Manifest: m, Item: domain.WorkItem{Repos: repos}}
}

func TestStartFlagsMapToChoices(t *testing.T) {
	r := resolvedFor(t, config.Conventions{}, "api")

	ch, err := startFlags{}.choices(r)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ch.Repos, []string{"api"}) || ch.Mode != startwork.ModeDraft || ch.Child != startwork.ChildAuto {
		t.Errorf("defaults = %+v: want the routed repo, draft, auto", ch)
	}

	// conventions.mr.create = never means no MR by default.
	rn := resolvedFor(t, config.Conventions{MR: config.MRConv{Create: "never"}}, "api")
	if ch, _ := (startFlags{}).choices(rn); ch.Mode != startwork.ModeBranch {
		t.Errorf("mr.create=never mode = %v, want branch", ch.Mode)
	}

	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte("hand written"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err = startFlags{
		repos: []string{"web", "api"}, mode: "BRANCH", child: "never", template: "Hotfix",
		strip: []string{"assign_reviewer", "all"}, bodyFile: body, moveLabel: true,
	}.choices(r)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ch.Repos, []string{"web", "api"}) || ch.Mode != startwork.ModeBranch || ch.Child != startwork.ChildNever ||
		ch.Template != "Hotfix" || !slices.Equal(ch.StripActions, []string{"assign_reviewer", "*"}) || ch.MRBody != "hand written" || !ch.MoveLabel {
		t.Errorf("choices = %+v", ch)
	}
}

func TestStartFlagErrors(t *testing.T) {
	r := resolvedFor(t, config.Conventions{}, "api")
	for name, f := range map[string]startFlags{
		"bad mode":         {mode: "yolo"},
		"bad child":        {child: "sometimes"},
		"contradicting":    {moveLabel: true, noLabel: true},
		"missing the body": {bodyFile: "/nonexistent/body.md"},
	} {
		if _, err := f.choices(r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := (startFlags{mode: "yolo"}).choices(r); err == nil || !strings.Contains(err.Error(), "draft, branch or browse") {
		t.Errorf("mode error = %v, want the valid values", err)
	}
}

func TestStartBranchFlagPinsReposAndAddsThem(t *testing.T) {
	r := resolvedFor(t, config.Conventions{}, "api")
	ch, err := startFlags{branches: []string{"web=fix-cache", "api=feature/x"}}.choices(r)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ch.Repos, []string{"api", "web"}) {
		t.Errorf("repos = %v: want the routed repo, then web added by --branch", ch.Repos)
	}
	if want := map[string]string{"web": "fix-cache", "api": "feature/x"}; !maps.Equal(ch.Branches, want) {
		t.Errorf("branches = %v, want %v", ch.Branches, want)
	}

	ch, err = startFlags{repos: []string{"api"}, branches: []string{"api=fix"}}.choices(r)
	if err != nil || !slices.Equal(ch.Repos, []string{"api"}) {
		t.Errorf("--repo api --branch api=fix: repos %v, err %v: want api once", ch.Repos, err)
	}
}

func TestStartBranchFlagErrors(t *testing.T) {
	r := resolvedFor(t, config.Conventions{}, "api")
	for name, tc := range map[string]struct {
		f    startFlags
		want string
	}{
		"no equals":    {startFlags{branches: []string{"web"}}, "want repo=value"},
		"empty branch": {startFlags{branches: []string{"web="}}, "want repo=value"},
		"unknown repo": {startFlags{branches: []string{"db=fix"}}, "unknown repo"},
		"bad name":     {startFlags{branches: []string{"web=a b"}}, "not a valid branch name"},
		"browse":       {startFlags{mode: "browse", branches: []string{"web=fix"}}, "browse"},
	} {
		_, err := tc.f.choices(r)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}
