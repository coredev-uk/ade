package project

import (
	"context"
	"path/filepath"

	"github.com/coredev-uk/ade/internal/git"
)

// RepoDir is <root>/<project>/<repo>, the directory holding a repo's .bare and
// its worktrees (2.2).
func (s *Service) RepoDir(project, repo string) string {
	return filepath.Join(s.App.Root, project, repo)
}

// GitClient returns a git client authorised for the origin of the bare clone
// at bare, resolving the forge's read token the way clone and update do. Callers
// outside this package (start work) use it to fetch and add worktrees.
func (s *Service) GitClient(ctx context.Context, forgeName, bare string) (git.Client, error) {
	url, err := (git.Client{}).RemoteURL(ctx, bare)
	if err != nil {
		return git.Client{}, err
	}
	return s.client(ctx, forgeName, url)
}

// ClientFor returns a git client authorised for rawURL: https remotes on a
// configured forge host (forgeName's own first) get that forge's token, any
// other URL runs bare. Used for clones that are not a project repo, such as
// the team dotfiles.
func (s *Service) ClientFor(ctx context.Context, forgeName, rawURL string) (git.Client, error) {
	return s.client(ctx, forgeName, rawURL)
}

// PushClient returns the client for `git push` to the origin of the bare clone
// at bare, authorised with the forge's write token. GitClient, which every
// other git call uses, carries the read token.
func (s *Service) PushClient(ctx context.Context, forgeName, bare string) (git.Client, error) {
	url, err := (git.Client{}).RemoteURL(ctx, bare)
	if err != nil {
		return git.Client{}, err
	}
	return s.pushClient(ctx, forgeName, url)
}
