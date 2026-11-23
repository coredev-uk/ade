package gitlab

import (
	"os"
	"testing"
)

// The rereview mutation the driver sends is the document the schema test
// validates (queries/*.graphql against schema.graphql).
func TestEmbeddedRereviewIsValidated(t *testing.T) {
	src, err := os.ReadFile("queries/reviewer_rereview.graphql")
	if err != nil {
		t.Fatal(err)
	}
	if reviewerRereviewMutation != string(src) {
		t.Fatal("reviewerRereviewMutation is not queries/reviewer_rereview.graphql")
	}
	if !isMutation(reviewerRereviewMutation) {
		t.Fatal("the document is not a mutation: it would go out with the read token")
	}
}
