package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
)

// checkManifest runs `ade config check --manifest <file> --json`.
func (e *initEnv) checkManifest(file string) ([]fileReport, error) {
	e.t.Helper()
	asJSON(e.t)
	cmd := newConfigCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"check", "--manifest", file})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.Background())
	var reports []fileReport
	if jerr := json.Unmarshal(out.Bytes(), &reports); jerr != nil {
		e.t.Fatalf("stdout is not the check JSON: %v\n%s", jerr, out.String())
	}
	return reports, err
}

func TestConfigCheckManifestWithoutUserConfig(t *testing.T) {
	e := newInitEnv(t)
	reports, err := e.checkManifest(e.manifest)
	if err != nil {
		t.Fatalf("a valid manifest failed without a user config: %v\n%+v", err, reports)
	}
	if len(reports) != 1 || reports[0].Kind != "manifest" {
		t.Fatalf("reports = %+v", reports)
	}
	is := reports[0].Issues
	if len(is) != 1 || is[0].Severity != config.SeverityWarn || is[0].Path != "forge" || !strings.Contains(is[0].Msg, "no user config") {
		t.Errorf("issues = %+v, want one forge warning", is)
	}

	// Real errors still fail.
	bad := strings.Replace(mustRead(t, e.manifest), `source = "milestones"`, `source = "sometimes"`, 1)
	if err := os.WriteFile(e.manifest, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.checkManifest(e.manifest); exitOf(err) != 1 {
		t.Errorf("invalid manifest: exit %d (%v), want 1", exitOf(err), err)
	}
}

func TestConfigCheckManifestUnknownForgeWithUserConfig(t *testing.T) {
	e := newInitEnv(t)
	if err := os.MkdirAll(filepath.Dir(e.configPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.configPath(), []byte("[forges.other]\nkind = \"gitlab\"\ntoken_env = \"X\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reports, err := e.checkManifest(e.manifest)
	if exitOf(err) != 1 {
		t.Fatalf("exit %d (%v), want 1: a config without the manifest's forge is an error\n%+v", exitOf(err), err, reports)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestConfigCheckManifestWithBrokenUserConfig(t *testing.T) {
	e := newInitEnv(t)
	if err := os.MkdirAll(filepath.Dir(e.configPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.configPath(), []byte("[forges.work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reports, err := e.checkManifest(e.manifest)
	if err != nil {
		t.Fatalf("a broken user config failed the manifest check: %v\n%+v", err, reports)
	}
	if len(reports) != 2 || reports[0].Kind != "config" || len(reports[0].Issues) == 0 || reports[0].Issues[0].Severity != config.SeverityWarn {
		t.Errorf("reports = %+v, want a config warning first", reports)
	}
}
