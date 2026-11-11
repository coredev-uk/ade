package gitlab

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func loadSchema(t *testing.T) *ast.Schema {
	t.Helper()
	src, err := os.ReadFile("schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: string(src)})
	if err != nil {
		t.Fatalf("schema.graphql does not parse: %v", err)
	}
	return schema
}

// Every document in queries/ must validate against GitLab's public schema:
// a typo or a field GitLab removed fails here, with no instance needed.
func TestQueriesMatchSchema(t *testing.T) {
	schema := loadSchema(t)
	files, err := filepath.Glob("queries/*.graphql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no query documents found: %v", err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if _, errs := gqlparser.LoadQueryWithRules(schema, string(src), nil); len(errs) > 0 {
				t.Fatalf("invalid against schema.graphql: %v", errs)
			}
		})
	}
}

// The embedded document the driver sends is the one the test validates.
func TestEmbeddedQueryIsValidated(t *testing.T) {
	src, err := os.ReadFile("queries/capabilities.graphql")
	if err != nil {
		t.Fatal(err)
	}
	if capabilitiesQuery != string(src) {
		t.Fatal("capabilitiesQuery is not queries/capabilities.graphql")
	}
}

// Guards the guard: the validator must reject what a removed or misspelled
// field looks like, otherwise the test above proves nothing.
func TestSchemaRejectsUnknownFields(t *testing.T) {
	schema := loadSchema(t)
	bad := []string{
		`query { group(fullPath: "g") { iterationCadencez(first: 1) { nodes { id } } } }`,
		`query { group(fullPath: "g") { workItemTypes(name: NOT_A_TYPE) { nodes { name } } } }`,
		`query($path: ID!) { group(fullPath: $path) { id`,
	}
	for _, q := range bad {
		if _, errs := gqlparser.LoadQueryWithRules(schema, q, nil); len(errs) == 0 {
			t.Errorf("accepted invalid document: %s", q)
		}
	}
}
