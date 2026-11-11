package config

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var updateSchema = flag.Bool("update-schema", false, "rewrite docs/schema/*.json from the Go structs")

// The committed schemas are what editors load; they must match the structs.
func TestCommittedSchemasAreCurrent(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "schema")
	for _, kind := range []SchemaKind{SchemaConfig, SchemaManifest} {
		want, err := Schema(kind)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, SchemaFile(kind))
		if *updateSchema {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (generate with: ade config schema --write docs/schema)", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s is stale: regenerate with `ade config schema --write docs/schema`", path)
		}
	}
}

func TestSchemaIsDeterministic(t *testing.T) {
	a, _ := Schema(SchemaConfig)
	b, _ := Schema(SchemaConfig)
	if string(a) != string(b) {
		t.Fatal("two runs differ: the committed file would flap")
	}
}

func TestSchemaDocsNameRealKeys(t *testing.T) {
	for k := range schemaDocs {
		if !schemaHasKey(k) {
			t.Errorf("schemaDocs[%q]: no such key in the structs", k)
		}
	}
	for k := range schemaEnums {
		if !schemaHasKey(k) {
			t.Errorf("schemaEnums[%q]: no such key in the structs", k)
		}
	}
	for k := range schemaPatterns {
		if !schemaHasKey(k) {
			t.Errorf("schemaPatterns[%q]: no such key in the structs", k)
		}
	}
}

func schemaAt(t *testing.T, kind SchemaKind, path string) map[string]any {
	t.Helper()
	raw, err := Schema(kind)
	if err != nil {
		t.Fatal(err)
	}
	var n map[string]any
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	for part := range strings.SplitSeq(path, ".") {
		if items, ok := n["items"].(map[string]any); ok {
			n = items
		}
		var next any
		if part == "*" {
			next = n["additionalProperties"]
		} else {
			next = n["properties"].(map[string]any)[part]
		}
		m, ok := next.(map[string]any)
		if !ok {
			t.Fatalf("%s: no %q", path, part)
		}
		n = m
	}
	return n
}

func TestSchemaReflectsTheStructs(t *testing.T) {
	// Types: duration string, optional bool, port union, pointer-to-bool draft.
	if iv := schemaAt(t, SchemaConfig, "update.interval"); iv["type"] != "string" || iv["pattern"] == nil {
		t.Errorf("update.interval = %v", iv)
	}
	if ob := schemaAt(t, SchemaConfig, "update.on_start"); ob["type"] != "boolean" {
		t.Errorf("update.on_start = %v", ob)
	}
	if d := schemaAt(t, SchemaManifest, "conventions.mr.draft"); d["type"] != "boolean" {
		t.Errorf("conventions.mr.draft = %v", d)
	}
	if p := schemaAt(t, SchemaManifest, "repos.*.tasks.*.port"); p["oneOf"] == nil {
		t.Errorf("port = %v", p)
	}
	// Maps and arrays of tables.
	if f := schemaAt(t, SchemaConfig, "forges.*.kind"); f["type"] != "string" {
		t.Errorf("forges.*.kind = %v", f)
	}
	if s := schemaAt(t, SchemaConfig, "projects.source"); s["type"] != "string" {
		t.Errorf("projects.source = %v", s)
	}
	// Typo detection: structs are closed, and `-` fields never appear.
	root := schemaAt(t, SchemaConfig, "host")
	if root["additionalProperties"] != false {
		t.Errorf("host must be a closed object: %v", root)
	}
	props := schemaAt(t, SchemaConfig, "host")["properties"].(map[string]any)
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"editor", "glyphs", "mode"}) {
		t.Errorf("host keys = %v", keys)
	}
	raw, _ := Schema(SchemaConfig)
	for _, hidden := range []string{`"Undecoded"`, `"undecoded"`, `"File"`, `"Missing"`, `"Overlay"`} {
		if strings.Contains(string(raw), hidden) {
			t.Errorf("schema leaks internal field %s", hidden)
		}
	}
}

// Every enum in the schema is a value the validator accepts, and the other
// way round for the ones the validator lists, so editor and `ade config check` agree.
func TestSchemaEnumsAgreeWithValidation(t *testing.T) {
	for _, v := range schemaEnums["config:host.editor"] {
		cfg := Defaults()
		cfg.Host.Editor = v
		if HasErrors(cfg.Validate()) {
			t.Errorf("host.editor %q is in the schema but fails validation: %v", v, cfg.Validate())
		}
	}
	cfg := Defaults()
	cfg.Host.Editor = "emacs"
	if !HasErrors(cfg.Validate()) {
		t.Error("an editor outside the schema's enum must fail validation")
	}
	retain := schemaAt(t, SchemaConfig, "records.audit_retain")
	if retain["pattern"] != retainFmt.String() {
		t.Errorf("audit_retain pattern %v", retain["pattern"])
	}
}
