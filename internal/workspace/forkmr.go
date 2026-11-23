package workspace

import (
	"context"
	"fmt"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/project"
)

// TargetForForkMR is the target of a fork MR, whose source branch is not in
// the repo: the detached mr-<iid> tree at the MR's head ref (fetched and added
// when missing, like `ade mr checkout`), opened with the review layout against
// the MR's target branch. The tree has no branch, so there is nothing to push.
func TargetForForkMR(ctx context.Context, a *app.App, m *config.Manifest, mr forge.MR) (Target, error) {
	repo, ok := repoByPath(m, mr.Ref.Project)
	if !ok {
		return Target{}, fmt.Errorf("%s is not in a repo of project %s", mr.Ref, m.Name)
	}
	tree, err := project.CheckoutMR(ctx, a, m, repo, mr, false)
	if err != nil {
		return Target{}, err
	}
	return Target{
		Project: m.Name, Repo: repo, Worktree: tree.Worktree, Title: fmt.Sprintf("review-%d", mr.Ref.IID),
		Layout: LayoutReview, BaseRef: "origin/" + mr.TargetBranch,
	}, nil
}
