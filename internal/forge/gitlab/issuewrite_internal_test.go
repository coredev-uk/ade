package gitlab

import (
	"os"
	"testing"
)

// The document the driver sends is the one TestQueriesMatchSchema validates.
func TestSetIterationMutationEmbedded(t *testing.T) {
	src, err := os.ReadFile("queries/set_iteration.graphql")
	if err != nil {
		t.Fatal(err)
	}
	if setIterationMutation != string(src) {
		t.Fatal("setIterationMutation is not queries/set_iteration.graphql")
	}
}
