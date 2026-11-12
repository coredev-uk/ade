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
	"unicode"
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
	resp, err := c.do(ctx, request{method: http.MethodPost, path: "/api/graphql", root: true, body: body, read: !isMutation(query)})
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

// isMutation reports whether doc is a GraphQL mutation: its first keyword,
// after whitespace and # comment lines, is "mutation". Queries (including the
// bare { ... } form) are POSTs that only read.
func isMutation(doc string) bool {
	doc = strings.TrimSpace(doc)
	for strings.HasPrefix(doc, "#") {
		_, doc, _ = strings.Cut(doc, "\n")
		doc = strings.TrimSpace(doc)
	}
	end := strings.IndexFunc(doc, func(r rune) bool { return !unicode.IsLetter(r) })
	if end < 0 {
		end = len(doc)
	}
	return doc[:end] == "mutation"
}

// graphqlSchemaError is a document the instance's schema rejects: it names a
// type or field this GitLab release does not have (an old instance). It is
// not a transient failure; the feature is simply not available there.
type graphqlSchemaError struct{ msg string }

func (e *graphqlSchemaError) Error() string { return e.msg }

// schemaMessages are GraphQL validation errors that mean "this schema lacks
// it", as graphql-ruby words them.
var schemaMessages = []string{"doesn't exist on type", "isn't a defined input type", "Unknown type", "Undefined field"}

func firstError(errs []graphqlError) error {
	if len(errs) == 0 {
		return errors.New("empty response")
	}
	msgs := make([]string, len(errs))
	schema := true
	for i, e := range errs {
		msgs[i] = e.Message
		known := false
		for _, m := range schemaMessages {
			known = known || strings.Contains(e.Message, m)
		}
		schema = schema && known
	}
	msg := strings.Join(msgs, "; ")
	if schema {
		return &graphqlSchemaError{msg: msg}
	}
	return errors.New(msg)
}
