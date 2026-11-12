package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/agile"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/forgesync"
)

func (e *cliEnv) sprint(args ...string) (string, error) { return e.run(newSprintCmd, args...) }

// cacheProjects stores one synced project per result, as `ade sync` leaves them.
func cacheProjects(t *testing.T, e *cliEnv, results ...forgesync.Result) {
	t.Helper()
	st, err := e.app.Cache()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		b, _ := json.Marshal(r)
		if _, err := st.DB.Exec(`INSERT INTO sync_state (project, json) VALUES (?, ?)`, r.Project, string(b)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSprintReviewWithSeveralProjectsTakesTheFirstWithSprints(t *testing.T) {
	e := notesEnv(t)
	ms, err := gitlab.New(e.srv.URL, fake.Token).Milestones(context.Background(), fake.GroupPath)
	if err != nil {
		t.Fatal(err)
	}
	sprints := domain.SprintsFromMilestones(ms)
	synced := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	cacheProjects(t, e,
		forgesync.Result{Project: "aaa-no-sprints", Forge: "sandbox-gitlab", Items: []domain.WorkItem{{Project: "aaa-no-sprints"}}, SyncedAt: synced},
		forgesync.Result{Project: "demo-gl", Forge: "sandbox-gitlab", Tracker: trackerProj, Sprints: sprints, SyncedAt: synced},
		forgesync.Result{Project: "zzz-other", Forge: "sandbox-gitlab", Sprints: sprints, SyncedAt: synced},
	)
	asJSON(t)
	out, err := e.sprint("review")
	if err != nil {
		t.Fatalf("sprint review with several projects: %v\n%s", err, out)
	}
	var r agile.Review
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r.Project != "demo-gl" {
		t.Errorf("reviewed %q, want the first project with sprints (demo-gl)", r.Project)
	}
	if len(r.Warnings) == 0 || !strings.Contains(strings.Join(r.Warnings, "\n"), "reviewing demo-gl") {
		t.Errorf("warnings %q should say which project was chosen", r.Warnings)
	}
	if _, err := e.sprint("review", "--project", "nope"); err == nil || !strings.Contains(err.Error(), `no cached project "nope"`) {
		t.Errorf("--project nope: %v", err)
	}
}
