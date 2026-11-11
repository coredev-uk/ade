package agile

import (
	"context"
	"fmt"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

// MyVelocity is the signed-in user's throughput over the last closed sprints
// of one ADE project: only issues assigned to them count, so Home's header can
// set the sprint's load against what they usually finish. It reads the
// manifest and the sync cache like Review, and asks the forge for the past
// sprints' issues only (no timelines).
func (c *Collector) MyVelocity(ctx context.Context, project string, n int) (Velocity, error) {
	results, err := c.cached()
	if err != nil {
		return Velocity{}, err
	}
	res, err := pickProject(results, project)
	if err != nil {
		return Velocity{}, err
	}
	if res.Window.Current == nil {
		return Velocity{}, fmt.Errorf("project %q has no running sprint", project)
	}
	mans, warns := c.manifests(ctx)
	m := mans[res.Project]
	if m == nil {
		return Velocity{}, fmt.Errorf("project %q: manifest unavailable (%v)", project, warns)
	}
	f, err := c.forgeFor(ctx, m)
	if err != nil {
		return Velocity{}, err
	}
	return BuildMyVelocity(ctx, f, m, res.Sprints, *res.Window.Current, res.Me, n)
}

// BuildMyVelocity reads the issues me had in each of the n sprints that ended
// before cur and averages what they finished (n <= 0: DefaultVelocitySprints).
func BuildMyVelocity(ctx context.Context, f forge.Forge, m *config.Manifest, sprints []domain.Sprint, cur domain.Sprint, me forge.User, n int) (Velocity, error) {
	if n <= 0 {
		n = DefaultVelocitySprints
	}
	var past []PastSprint
	for _, sp := range pastSprints(sprints, cur, n) {
		q, ok := sprintQuery(m.Tracker.Project, sp)
		if !ok {
			continue
		}
		q.Assignee = me.Username
		issues, err := f.ListIssues(ctx, q)
		if err != nil {
			return Velocity{}, fmt.Errorf("velocity: %s: %w", sp.Title, brief(err))
		}
		past = append(past, PastSprint{Sprint: sp, Issues: issues})
	}
	return velocity(past, m), nil
}
