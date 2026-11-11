package agile

import (
	"context"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// Your average counts only issues assigned to you: Sprint 41's one closed
// issue (#9, weight 1) is yours or it is not, and the average follows.
func TestBuildMyVelocityCountsOnlyYourIssues(t *testing.T) {
	srv := fake.New(fake.Premium, fake.WithClock(fakeNow))
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL, fake.Token, gitlab.WithClock(func() time.Time { return fakeNow }))
	ctx := context.Background()
	ms, err := c.Milestones(ctx, fake.GroupPath)
	if err != nil {
		t.Fatal(err)
	}
	sprints := domain.SprintsFromMilestones(ms)
	m := manifest()
	m.Tracker.Project = fakeTracker
	cur := domain.WindowAt(sprints, fakeNow, nil, 1).Current

	all, err := BuildReviewLive(ctx, c, ReviewLiveInput{
		Project: "sandbox", Manifest: m, Sprint: *cur, Sprints: sprints, Me: forge.User{Username: "sandbox-dev"},
		Cal: utcCal(), Now: fakeNow, VelocityN: 3,
	})
	if err != nil || len(all.Velocity.Sprints) != 1 {
		t.Fatalf("review velocity %+v err %v", all.Velocity, err)
	}

	nobody, err := BuildMyVelocity(ctx, c, m, sprints, *cur, forge.User{Username: "nobody-here"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if nobody.Average != nil {
		t.Errorf("a user with no issues has no average, got %v", *nobody.Average)
	}

	mine, err := BuildMyVelocity(ctx, c, m, sprints, *cur, forge.User{Username: "sandbox-dev"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Sprints) != 1 || mine.Sprints[0].Title != all.Velocity.Sprints[0].Title {
		t.Fatalf("mine = %+v, want the one past sprint %q", mine, all.Velocity.Sprints[0].Title)
	}
}
