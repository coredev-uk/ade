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

// iids repeat across a group's projects: when the homonyms overflow a page the
// read follows the cursor instead of dropping the oldest issues.
func TestHierarchyFollowsPageInfoWhenAChunkOverflowsAPage(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithWorkItemPageSize(2))
	defer srv.Close()
	cases := srv.SeedHierarchyCases()
	c := gitlab.New(srv.URL, fake.Token)
	// iid of each child is shared with issues of the other projects, so a
	// chunk of three iids matches more than two nodes.
	refs := []forge.Ref{cases.HierarchyOnly.Child, cases.HierarchyOutside.Child, cases.LinkOnly.Child}
	got, err := c.Parents(context.Background(), refs)
	if err != nil {
		t.Fatal(err)
	}
	want := map[forge.Ref]forge.Ref{
		cases.HierarchyOnly.Child:    cases.HierarchyOnly.Tracker,
		cases.HierarchyOutside.Child: cases.HierarchyOutside.Tracker,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parents = %v, want %v", got, want)
	}
	if n := graphqlRequests(srv); n < 2 {
		t.Errorf("%d request(s) with a page of 2: the cursor was not followed", n)
	}
}

func TestSetParentRefusalIsCachedOnlyForTypeNesting(t *testing.T) {
	child, parent := issueRef(apiProj, 1), issueRef(trackerProj, 3)
	for msg, refused := range map[string]bool{
		"No matching work item found. Make sure that you are adding a valid work item ID.": true,
		"#1 cannot be added: it's not allowed to add this type of parent":                  true,
		"You do not have permission to update this work item":                              false,
	} {
		srv := fake.New(fake.Free, fake.WithParentError(msg))
		c := gitlab.New(srv.URL, fake.Token)
		err := c.SetParent(context.Background(), child, parent)
		srv.Close()
		if err == nil || errors.Is(err, forge.ErrParentRefused) != refused {
			t.Errorf("%q: err = %v, want ErrParentRefused = %v", msg, err, refused)
		}
	}
}

// A refused write says nothing about the read queries.
func TestSetParentForbiddenDoesNotDisableTheHierarchyReads(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithForbiddenParents())
	defer srv.Close()
	cases := srv.SeedHierarchyCases()
	c := gitlab.New(srv.URL, fake.Token)
	ctx := context.Background()
	if err := c.SetParent(ctx, issueRef(apiProj, 1), issueRef(trackerProj, 3)); err == nil {
		t.Fatal("a forbidden write succeeded")
	}
	got, err := c.Parents(ctx, []forge.Ref{cases.HierarchyOnly.Child})
	if err != nil || got[cases.HierarchyOnly.Child] != cases.HierarchyOnly.Tracker {
		t.Errorf("Parents after a forbidden write = %v, %v", got, err)
	}
}
