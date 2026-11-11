package gitlab_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

var (
	_ forge.Discoverer = (*gitlab.Client)(nil)
	_ forge.FileWriter = (*gitlab.Client)(nil)
)

const trackerPath = group + "/tracker"

func TestReposListsArchivedAndPaginates(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithPageSize(2))
	t.Cleanup(srv.Close)
	srv.ArchiveProject(srv.AddProject("old"))
	c := gitlab.New(srv.URL, fake.Token)

	repos, err := c.Repos(context.Background(), group)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	archived := map[string]bool{}
	for _, r := range repos {
		got = append(got, r.Path)
		archived[r.Name] = r.Archived
		if r.DefaultBranch != "main" {
			t.Errorf("%s default branch = %q", r.Path, r.DefaultBranch)
		}
	}
	slices.Sort(got)
	if want := []string{group + "/api", group + "/old", group + "/tracker", group + "/web"}; !slices.Equal(got, want) {
		t.Errorf("repos = %v, want %v (4 across 2 pages)", got, want)
	}
	if !archived["old"] || archived["api"] {
		t.Errorf("archived = %v", archived)
	}
	if _, err := c.Repos(context.Background(), "no-such-group"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown group: %v, want ErrNotFound", err)
	}
}

func TestLabelsListsTheGroupsLabels(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	srv.AddLabel("kind/unused")
	c := gitlab.New(srv.URL, fake.Token)
	labels, err := c.Labels(context.Background(), trackerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"type::story", "workflow::in progress", "weight::5", "kind/unused"} {
		if !slices.Contains(labels, want) {
			t.Errorf("labels lack %q: %v", want, labels)
		}
	}
	if _, err := c.Labels(context.Background(), "nope/nope"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}
}

func TestCadencesPremiumAndFree(t *testing.T) {
	forEachTier(t, func(t *testing.T, tier fake.Tier, c *gitlab.Client, _ *fake.Server) {
		cads, err := c.Cadences(context.Background(), group)
		if tier == fake.Free {
			if !errors.Is(err, forge.ErrUnavailable) {
				t.Errorf("Free: %v, want ErrUnavailable", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(cads) != 2 || cads[0].Title != "Delivery" || cads[0].Weeks != 2 || cads[0].Start.String() != "2026-10-19" || !cads[0].Automatic {
			t.Errorf("cadences = %+v", cads)
		}
	})
}

func TestCommitFileCreatesThenUpdates(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, fake.Token)
	ctx := context.Background()
	if _, err := c.CreateBranch(ctx, trackerPath, "topic", "main"); err != nil {
		t.Fatal(err)
	}

	if err := c.CommitFile(ctx, trackerPath, "topic", "dir/a.toml", []byte("one = 1\n"), "add"); err != nil {
		t.Fatal(err)
	}
	if err := c.CommitFile(ctx, trackerPath, "topic", "dir/a.toml", []byte("one = 2\n"), "update"); err != nil {
		t.Fatalf("an existing file must be replaced: %v", err)
	}
	got, err := c.ReadFile(ctx, trackerPath, "topic", "dir/a.toml")
	if err != nil || string(got) != "one = 2\n" {
		t.Errorf("file on topic = %q %v", got, err)
	}
	if _, err := c.ReadFile(ctx, trackerPath, "main", "dir/a.toml"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("the default branch must not have it: %v", err)
	}
	if err := c.CommitFile(ctx, trackerPath, "no-such-branch", "x", []byte("x"), "m"); err == nil {
		t.Error("committing to a missing branch succeeded")
	}
	if err := c.CommitFile(ctx, trackerPath, "topic", "x", []byte("x"), ""); err == nil {
		t.Error("a commit without a message was sent")
	}
}
