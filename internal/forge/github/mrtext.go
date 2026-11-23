package github

import (
	"context"
	"net/http"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

var _ forge.MRTextWriter = (*Client)(nil)

type wireCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"commit"`
}

// MRCommits implements forge.MRTextWriter. GitHub lists a pull request's
// commits oldest first, the order the work was done in.
func (c *Client) MRCommits(ctx context.Context, ref forge.Ref) ([]forge.Commit, error) {
	if err := wantKind(ref, forge.KindMR); err != nil {
		return nil, err
	}
	raw, err := getAll[wireCommit](ctx, c, pullPath(ref)+"/commits", nil, false)
	if err != nil {
		return nil, err
	}
	out := make([]forge.Commit, len(raw))
	for i, w := range raw {
		title, _, _ := strings.Cut(w.Commit.Message, "\n")
		out[i] = forge.Commit{SHA: w.SHA, Title: title, Message: w.Commit.Message, Author: w.Commit.Author.Name}
	}
	return out, nil
}

// SetMRDescription implements forge.MRTextWriter: PATCH /pulls/{n} with the
// body only, so the title, base and state are left alone. GitHub has no quick
// actions, so the text is stored as given.
func (c *Client) SetMRDescription(ctx context.Context, ref forge.Ref, description string) error {
	if err := wantKind(ref, forge.KindMR); err != nil {
		return err
	}
	return c.send(ctx, http.MethodPatch, pullPath(ref), map[string]string{"body": description}, nil)
}
