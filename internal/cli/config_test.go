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

func TestConfigSchemaInstall(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	t.Setenv("ADE_CONFIG", cfgFile)
	if err := os.WriteFile(cfgFile, []byte("[user]\nhandle = \"a\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	own := "#:schema https://example.com/s.json\n[user]\n"
	local := filepath.Join(dir, "config.local.toml")
	if err := os.WriteFile(local, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func() string {
		t.Helper()
		cmd := newConfigCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"schema", "--install"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		if err := cmd.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	out := run()
	got, _ := os.ReadFile(cfgFile)
	if !strings.HasPrefix(string(got), "#:schema ./config.schema.json\n[user]") {
		t.Errorf("config.toml = %q", got)
	}
	if st, _ := os.Stat(cfgFile); st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
	if l, _ := os.ReadFile(local); string(l) != own {
		t.Errorf("config.local.toml with a directive was touched: %q", l)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.schema.json")); err != nil {
		t.Error(err)
	}
	if !strings.Contains(out, "updated "+cfgFile) {
		t.Errorf("output = %q", out)
	}
	if out := run(); out != "schemas up to date\n" {
		t.Errorf("second run = %q", out)
	}
}
