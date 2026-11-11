package cli

import (
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
