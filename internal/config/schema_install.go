package config

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const schemaPrefix = "#:schema"

var releaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// SchemaDirective is the first-line comment that points taplo and Even Better
// TOML at a schema: a path (relative to the TOML file) or a URL.
func SchemaDirective(target string) string { return schemaPrefix + " " + target }

// SchemaDirectiveTarget returns the target of the `#:schema` directive on the
// first line of data; ok is false when there is none.
func SchemaDirectiveTarget(data []byte) (target string, ok bool) {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	s := strings.TrimSuffix(string(line), "\r")
	rest, ok := strings.CutPrefix(s, schemaPrefix)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// WithSchemaDirective prepends the directive for target unless the first line
// already is one, which is kept whatever it points at.
func WithSchemaDirective(data []byte, target string) []byte {
	if bytes.HasPrefix(data, []byte(schemaPrefix)) {
		return data
	}
	out := make([]byte, 0, len(data)+len(target)+len(schemaPrefix)+2)
	out = append(out, SchemaDirective(target)...)
	out = append(out, '\n')
	return append(out, data...)
}

// InstallSchemas writes both schemas into dir, rewriting only files whose
// content differs, and returns the paths it wrote.
func InstallSchemas(dir string) (written []string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	for _, k := range []SchemaKind{SchemaConfig, SchemaManifest} {
		doc, err := Schema(k)
		if err != nil {
			return written, err
		}
		path := filepath.Join(dir, SchemaFile(k))
		if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, doc) {
			continue
		}
		if err := os.WriteFile(path, doc, 0o644); err != nil {
			return written, err
		}
		written = append(written, path)
	}
	return written, nil
}

// SchemaURL is where the committed schema of kind for an ADE version lives:
// the release tag for x.y.z, main for dev and commit-hash builds.
func SchemaURL(kind SchemaKind, version string) string {
	ref := "main"
	if releaseVersion.MatchString(version) {
		ref = "v" + version
	}
	return "https://raw.githubusercontent.com/coredev-uk/ade/" + ref + "/docs/schema/" + SchemaFile(kind)
}
