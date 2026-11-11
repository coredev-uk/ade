package startwork

import (
	"context"
	"errors"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/workspace"
)

// WorkspaceOpener adapts workspace.Open to Env.Workspace: it opens the tab (or
// VS Code window) for a worktree and hands over the work item's own trees of
// the other repos. A host that cannot open a tab (a plain terminal) returns its
// hint as the error, which Execute reports as a note, not a failure.
func WorkspaceOpener(a *app.App) func(context.Context, WorkspaceRequest) error {
	return WorkspaceOpenerIn(a, "")
}

// WorkspaceOpenerIn is WorkspaceOpener for a zellij session other than `ade`
// (empty: the default). Only tests use it, with throwaway `ade-test-*` sessions.
func WorkspaceOpenerIn(a *app.App, session string) func(context.Context, WorkspaceRequest) error {
	return func(ctx context.Context, req WorkspaceRequest) error {
		opts := []workspace.OpenOption{
			workspace.WithItemTrees(req.ItemTrees), workspace.WithTitle(req.Title),
			workspace.WithItem(workspace.ItemRef{Project: req.Project, Forge: req.Forge, Item: req.Item}),
		}
		if req.Layout != "" {
			opts = append(opts, workspace.WithLayout(req.Layout))
		}
		if session != "" {
			opts = append(opts, workspace.WithSession(session))
		}
		res, err := workspace.Open(ctx, a, req.Project, req.Repo, req.Worktree, opts...)
		if err != nil {
			return err
		}
		if res.Hint != "" {
			return errors.New(res.Hint)
		}
		return nil
	}
}
