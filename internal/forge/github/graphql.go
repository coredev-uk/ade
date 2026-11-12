package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/coredev-uk/ade/internal/forge"
)

// The GraphQL documents ADE sends. Each carries an operation name; the fake
// server dispatches on it, and the live test validates every document against
// api.github.com. Field names are GitHub's own.
const (
	reviewThreadsQuery = `query ReviewThreads($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      id
      reviewThreads(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          isResolved
          isOutdated
          path
          line
          originalLine
          startLine
          diffSide
          comments(first: 100) {
            nodes {
              id
              databaseId
              body
              createdAt
              author { login ... on User { name } }
              pullRequestReview { state }
            }
          }
        }
      }
    }
  }
}`

	issuePRsQuery = `query IssuePRs($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    issue(number: $number) {
      closedByPullRequestsReferences(first: 50, includeClosedPrs: true) {
        nodes { number repository { nameWithOwner } }
      }
      timelineItems(first: 100, itemTypes: [CROSS_REFERENCED_EVENT]) {
        nodes {
          __typename
          ... on CrossReferencedEvent {
            source {
              __typename
              ... on PullRequest { number repository { nameWithOwner } }
            }
          }
        }
      }
    }
  }
}`

	resolveThreadMutation = `mutation ResolveThread($id: ID!) {
  resolveReviewThread(input: {threadId: $id}) { thread { id isResolved } }
}`

	unresolveThreadMutation = `mutation UnresolveThread($id: ID!) {
  unresolveReviewThread(input: {threadId: $id}) { thread { id isResolved } }
}`

	addReviewThreadMutation = `mutation AddReviewThread($input: AddPullRequestReviewThreadInput!) {
  addPullRequestReviewThread(input: $input) {
    thread { id comments(first: 1) { nodes { databaseId } } }
  }
}`

	addReviewThreadReplyMutation = `mutation AddReviewThreadReply($input: AddPullRequestReviewThreadReplyInput!) {
  addPullRequestReviewThreadReply(input: $input) {
    comment { id databaseId body createdAt author { login } }
  }
}`

	markReadyMutation = `mutation MarkReady($id: ID!) {
  markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { id isDraft } }
}`

	convertToDraftMutation = `mutation ConvertToDraft($id: ID!) {
  convertPullRequestToDraft(input: {pullRequestId: $id}) { pullRequest { id isDraft } }
}`

	enableAutoMergeMutation = `mutation EnableAutoMerge($input: EnablePullRequestAutoMergeInput!) {
  enablePullRequestAutoMerge(input: $input) {
    pullRequest { id autoMergeRequest { enabledAt } }
  }
}`
)

// GraphQLError is one entry of a GraphQL answer's errors array.
type GraphQLError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Path    []any  `json:"path"`
}

func (e GraphQLError) Error() string {
	if e.Type != "" {
		return e.Type + ": " + e.Message
	}
	return e.Message
}

// graphql runs one document and decodes its data into out. GitHub answers 200
// with an errors array for failures, so those become Go errors here: NOT_FOUND
// unwraps to forge.ErrNotFound, FORBIDDEN and INSUFFICIENT_SCOPES to ErrAuth.
// Data that arrived next to errors is still decoded into out before the error
// is returned.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return fmt.Errorf("github graphql: encoding request: %w", err)
	}
	op := operationName(query)
	resp, err := c.do(ctx, request{method: http.MethodPost, url: c.gql, path: "graphql " + op, body: body, read: !isMutation(query)})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []GraphQLError  `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("github graphql %s: decoding response: %w", op, err)
	}
	if len(env.Data) > 0 && string(env.Data) != "null" && out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("github graphql %s: decoding data: %w", op, err)
		}
	}
	if len(env.Errors) == 0 {
		return nil
	}
	errs := make([]error, len(env.Errors))
	for i, e := range env.Errors {
		errs[i] = graphqlError(e)
	}
	return fmt.Errorf("github graphql %s: %w", op, errors.Join(errs...))
}

type kindedError struct {
	GraphQLError
	kind error
}

func (e kindedError) Unwrap() error { return e.kind }

func graphqlError(e GraphQLError) error {
	switch strings.ToUpper(e.Type) {
	case "NOT_FOUND":
		return kindedError{e, forge.ErrNotFound}
	case "FORBIDDEN", "INSUFFICIENT_SCOPES":
		return kindedError{e, forge.ErrAuth}
	}
	return e
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

// operationName reads the name after "query" or "mutation".
func operationName(doc string) string {
	f := strings.Fields(doc)
	if len(f) < 2 {
		return ""
	}
	name, _, _ := strings.Cut(f[1], "(")
	return strings.TrimSpace(name)
}
