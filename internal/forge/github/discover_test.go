package github_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/github"
	"github.com/coredev-uk/ade/internal/forge/github/fake"
)

var (
	_ forge.Discoverer = (*github.Client)(nil)
	_ forge.FileWriter = (*github.Client)(nil)
)

func TestReposListsArchivedAndPaginates(t *testing.T) {
	c, srv := newClient(t, fake.WithPageSize(2))
	srv.ArchiveRepo(srv.AddRepo("old")[len("ade-demo/"):])
	repos, err := c.Repos(context.Background(), "ade-demo")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	archived := map[string]bool{}
	for _, r := range repos {
		got = append(got, r.Path)
		archived[r.Name] = r.Archived
	}
	slices.Sort(got)
	if want := []string{"ade-demo/api", "ade-demo/old", "ade-demo/tracker", "ade-demo/web"}; !slices.Equal(got, want) {
		t.Errorf("repos = %v, want %v", got, want)
	}
	if !archived["old"] || archived["api"] {
		t.Errorf("archived = %v", archived)
	}
	if _, err := c.Repos(context.Background(), "nobody"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown owner: %v, want ErrNotFound", err)
	}
}

func TestLabelsAndCadences(t *testing.T) {
	c, _ := newClient(t)
	labels, err := c.Labels(context.Background(), "ade-demo/tracker")
	if err != nil || !slices.Contains(labels, "type:story") || !slices.Contains(labels, "workflow:in progress") {
		t.Errorf("labels = %v %v", labels, err)
	}
	if _, err := c.Cadences(context.Background(), "ade-demo"); !errors.Is(err, forge.ErrUnavailable) {
		t.Errorf("cadences: %v, want ErrUnavailable", err)
	}
}

func TestCommitFileCreatesThenUpdates(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	if _, err := c.CreateBranch(ctx, "ade-demo/tracker", "topic", "main"); err != nil {
		t.Fatal(err)
	}
	if err := c.CommitFile(ctx, "ade-demo/tracker", "topic", "dir/a.toml", []byte("one = 1\n"), "add"); err != nil {
		t.Fatal(err)
	}
	if err := c.CommitFile(ctx, "ade-demo/tracker", "topic", "dir/a.toml", []byte("one = 2\n"), "update"); err != nil {
		t.Fatalf("an existing file must be replaced: %v", err)
	}
	if got, ok := srv.FileAt("tracker", "topic", "dir/a.toml"); !ok || got != "one = 2\n" {
		t.Errorf("file on topic = %q %v", got, ok)
	}
	if _, ok := srv.FileAt("tracker", "main", "dir/a.toml"); ok {
		t.Error("the default branch must not have it")
	}
	if err := c.CommitFile(ctx, "ade-demo/tracker", "no-such-branch", "x", []byte("x"), "m"); err == nil {
		t.Error("committing to a missing branch succeeded")
	}
}

// A user's own repositories are only complete (private ones included) through
// /user/repos; the fake's viewer is demo-user.
func TestReposOfTheAuthenticatedUserUseTheOwnListing(t *testing.T) {
	c, srv := newClient(t)
	repos, err := c.Repos(context.Background(), "demo-user")
	if err != nil || len(repos) != 3 {
		t.Fatalf("repos = %v %v", repos, err)
	}
	var paths []string
	for _, r := range srv.Requests() {
		paths = append(paths, r.Path)
	}
	if !slices.ContainsFunc(paths, func(p string) bool { return strings.HasSuffix(p, "/user/repos") }) {
		t.Errorf("requests = %v: want /user/repos for the viewer's own namespace", paths)
	}
	if slices.ContainsFunc(paths, func(p string) bool { return strings.Contains(p, "/users/demo-user/repos") }) {
		t.Errorf("requests = %v: the public listing would miss private repos", paths)
	}
}
