package gitlab

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

var _ forge.WeightWriter = (*Client)(nil)

// setWeightMutation is validated against schema.graphql with the other
// documents in queries/.
//
//go:embed queries/set_weight.graphql
var setWeightMutation string

// SetWeight implements forge.WeightWriter through GraphQL issueSetWeight: a
// null weight clears the field, which REST cannot do. An instance without
// weights answers with a null payload next to an error, which is
// forge.ErrUnavailable.
func (c *Client) SetWeight(ctx context.Context, ref forge.Ref, weight *int) error {
	if err := wantKind(ref, forge.KindIssue); err != nil {
		return err
	}
	if weight != nil && *weight < 1 {
		return fmt.Errorf("gitlab: weight %d on %s: want 1 or more, or nil to clear", *weight, ref)
	}
	vars := map[string]any{
		"projectPath": ref.Project,
		"iid":         strconv.Itoa(ref.IID),
		"weight":      weight,
	}
	var out struct {
		IssueSetWeight *struct {
			Errors []string `json:"errors"`
		} `json:"issueSetWeight"`
	}
	if err := c.graphql(ctx, setWeightMutation, vars, &out); err != nil {
		return err
	}
	if out.IssueSetWeight == nil {
		return fmt.Errorf("gitlab: issueSetWeight on %s: %w", ref, forge.ErrUnavailable)
	}
	if errs := out.IssueSetWeight.Errors; len(errs) > 0 {
		return fmt.Errorf("gitlab: issueSetWeight on %s: %s", ref, strings.Join(errs, "; "))
	}
	return nil
}
