package gitlab

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// capabilitiesQuery is the only GraphQL document the driver sends. Every
// document in queries/ is validated against schema.graphql by the tests, so a
// typo or a field GitLab removed fails the build instead of a user's session.
//
//go:embed queries/capabilities.graphql
var capabilitiesQuery string

type graphqlError struct {
	Message string `json:"message"`
}

// graphql POSTs query to /api/graphql and decodes `data` into out. GraphQL
// reports permission and licence failures as field-level errors next to
// partial data (the gated field is null), so errors are only a failure when
// no data came back at all.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return fmt.Errorf("gitlab graphql: %w", err)
	}
	resp, err := c.do(ctx, request{method: http.MethodPost, path: "/api/graphql", root: true, body: body})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var res struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphqlError  `json:"errors"`
	}
	if err := decode(resp, "/graphql", &res); err != nil {
		return err
	}
	if len(res.Data) == 0 || bytes.Equal(res.Data, []byte("null")) {
		return fmt.Errorf("gitlab graphql: %w", firstError(res.Errors))
	}
	if err := json.Unmarshal(res.Data, out); err != nil {
		return fmt.Errorf("gitlab graphql: decoding data: %w", err)
	}
	return nil
}

func firstError(errs []graphqlError) error {
	if len(errs) == 0 {
		return errors.New("empty response")
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Message
	}
	return errors.New(strings.Join(msgs, "; "))
}
