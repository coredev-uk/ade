package gitlab_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

const trackerProj = group + "/tracker"

func TestHierarchyReadsParentsAndChildren(t *testing.T) {
	srv := fake.New(fake.Free)
	defer srv.Close()
	cases := srv.SeedHierarchyCases()
	c := gitlab.New(srv.URL, fake.Token)
	ctx := context.Background()

	refs := []forge.Ref{cases.HierarchyOnly.Child, cases.LinkOnly.Child, cases.HierarchyOutside.Child}
	got, err := c.Parents(ctx, refs)
	if err != nil {
		t.Fatal(err)
	}
	want := map[forge.Ref]forge.Ref{
		cases.HierarchyOnly.Child:    cases.HierarchyOnly.Tracker,
		cases.HierarchyOutside.Child: cases.HierarchyOutside.Tracker,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parents = %v, want %v (a link is not a hierarchy parent)", got, want)
	}

	kids, err := c.Children(ctx, cases.HierarchyOnly.Tracker)
	if err != nil || !reflect.DeepEqual(kids, []forge.Ref{cases.HierarchyOnly.Child}) {
		t.Errorf("Children = %v, %v", kids, err)
	}
}

func TestHierarchyBatchesAtMost25IssuesPerRequest(t *testing.T) {
	srv := fake.New(fake.Free)
	defer srv.Close()
	c := gitlab.New(srv.URL, fake.Token)
	var refs []forge.Ref
	for i := 1; i <= 60; i++ {
		refs = append(refs, issueRef(apiProj, i))
	}
	if _, err := c.Parents(context.Background(), refs); err != nil {
		t.Fatal(err)
	}
	if n := graphqlRequests(srv); n != 3 {
		t.Errorf("60 issues took %d requests, want 3", n)
	}
}

func TestSetParentPlacesTheChildUnderTheTracker(t *testing.T) {
	srv := fake.New(fake.Free)
	defer srv.Close()
	c := gitlab.New(srv.URL, fake.Token)
	child, parent := issueRef(apiProj, 1), issueRef(trackerProj, 3)
	if err := c.SetParent(context.Background(), child, parent); err != nil {
		t.Fatal(err)
	}
	if got, ok := srv.Parent(child); !ok || got != parent {
		t.Errorf("parent of %s = %v, %v", child, got, ok)
	}
}

func TestSetParentRefusedIsErrParentRefused(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithRefusedParents())
	defer srv.Close()
	c := gitlab.New(srv.URL, fake.Token)
	err := c.SetParent(context.Background(), issueRef(apiProj, 1), issueRef(trackerProj, 3))
	if !errors.Is(err, forge.ErrParentRefused) {
		t.Fatalf("err = %v, want ErrParentRefused", err)
	}
	if _, ok := srv.Parent(issueRef(apiProj, 1)); ok {
		t.Error("a refused parent was set")
	}
}

func TestHierarchyUnavailableIsRememberedAndSilent(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithoutHierarchy())
	defer srv.Close()
	c := gitlab.New(srv.URL, fake.Token)
	ctx := context.Background()
	refs := []forge.Ref{issueRef(apiProj, 1)}
	if _, err := c.Parents(ctx, refs); !errors.Is(err, forge.ErrUnavailable) {
		t.Fatalf("Parents err = %v, want ErrUnavailable", err)
	}
	before := graphqlRequests(srv)
	if _, err := c.Children(ctx, refs[0]); !errors.Is(err, forge.ErrUnavailable) {
		t.Errorf("Children err = %v, want ErrUnavailable", err)
	}
	if err := c.SetParent(ctx, refs[0], issueRef(trackerProj, 1)); !errors.Is(err, forge.ErrUnavailable) {
		t.Errorf("SetParent err = %v, want ErrUnavailable", err)
	}
	if n := graphqlRequests(srv); n != before {
		t.Errorf("%d more requests after the instance said it has no hierarchy", n-before)
	}
	if u := c.Unavailable(); len(u) != 0 {
		t.Errorf("the hierarchy is optional, nothing to report: %v", u)
	}
}
