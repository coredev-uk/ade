package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadConfig(t *testing.T, src string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"defaults are clean", "", nil},
		{"valid forges", "[forges.a]\nkind = \"gitlab\"\nurl = \"https://gl.example.com\"\ntoken_env = \"T\"\n[forges.b]\nkind = \"github\"\ntoken_cmd = \"gh auth token\"\n", nil},
		{"bad kind", "[forges.a]\nkind = \"gitea\"\ntoken_env = \"T\"\n", []string{"error: forges.a.kind: unknown kind \"gitea\""}},
		{"bad protocol", "[forges.a]\nkind = \"github\"\ntoken_env = \"T\"\ngit_protocol = \"ftp\"\n", []string{"forges.a.git_protocol:"}},
		{"bad url", "[forges.a]\nkind = \"gitlab\"\nurl = \"gitlab.example.com\"\ntoken_env = \"T\"\n", []string{"forges.a.url:"}},
		{"no credential source warns", "[forges.a]\nkind = \"github\"\n", []string{"warn: forges.a:"}},
		{"empty project source", "[[projects]]\nsource = \"\"\n", []string{"projects[0].source: required"}},
		{"bad feature branches", "[update]\nfeature_branches = \"merge\"\n", []string{"update.feature_branches:"}},
		{"negative interval", "[update]\ninterval = \"-5m\"\n", []string{"update.interval:"}},
		{"zero concurrency", "[update]\nconcurrency = 0\n", []string{"update.concurrency:"}},
		{"quiet ok", "[notify]\nquiet = \"19:00-08:00\"\n", nil},
		{"quiet malformed", "[notify]\nquiet = \"evenings\"\n", []string{"notify.quiet:"}},
		{"quiet out of range", "[notify]\nquiet = \"25:00-08:00\"\n", []string{"notify.quiet:", "25:00"}},
		{"quiet bad minutes", "[notify]\nquiet = \"19:00-08:61\"\n", []string{"08:61"}},
		{"audit retain days", "[records]\naudit_retain = \"365d\"\n", nil},
		{"audit retain junk", "[records]\naudit_retain = \"a year\"\n", []string{"records.audit_retain:"}},
		{"bad host mode", "[host]\nmode = \"tmux\"\n", []string{"host.mode:"}},
		{"bad glyphs", "[host]\nglyphs = \"emoji\"\n", []string{"host.glyphs:"}},
		{"unknown keys warn", "[user]\nhandel = \"x\"\n", []string{"warn: user.handel: unknown key"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertIssues(t, loadConfig(t, tt.src).Validate(), tt.want...)
		})
	}
}
