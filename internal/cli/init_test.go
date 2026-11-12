package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/doctor"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/onboard"
)

const initToken = "INIT_TEST_TOKEN"

// initEnv is a fresh machine: an empty HOME, no XDG overrides, no config. The
// real ~/.config/ade is never consulted.
type initEnv struct {
	t        *testing.T
	home     string
	srv      *fake.Server
	manifest string
}

func newInitEnv(t *testing.T) *initEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "ADE_CONFIG"} {
		t.Setenv(k, "")
	}
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.invalid")
	t.Setenv(initToken, fake.Token)

	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)

	// One local remote stands in for the api repo (explicit clone url).
	base := t.TempDir()
	bare, work := filepath.Join(base, "api.git"), filepath.Join(base, "api-work")
	for _, args := range [][]string{
		{"init", "--bare", "-b", "main", bare},
		{"clone", "--quiet", bare, work},
	} {
		gitIn(t, base, args...)
	}
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("api\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "README")
	gitIn(t, work, "commit", "--quiet", "-m", "init")
	gitIn(t, work, "push", "--quiet", "origin", "main")

	manifest := filepath.Join(t.TempDir(), "team.ade.toml")
	body := `name = "team"
forge = "team-gl"

[tracker]
project    = "` + fake.GroupPath + `/tracker"
iterations = { source = "milestones" }

[labels.type]
pattern = "type::{value}"

[labels.app]
pattern = "app::{value}"

[repos.api]
path = "` + fake.GroupPath + `/api"
url  = "file://` + bare + `"
app  = ["api"]
`
	if err := os.WriteFile(manifest, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return &initEnv{t: t, home: home, srv: srv, manifest: manifest}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (e *initEnv) configPath() string { return filepath.Join(e.home, ".config", "ade", "config.toml") }

// init runs `ade init <args>` with --json so the result can be inspected.
func (e *initEnv) init(args ...string) (initResult, string, error) {
	e.t.Helper()
	asJSON(e.t)
	cmd := newInitCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader("")) // not a terminal
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.Background())
	var res initResult
	if out.Len() > 0 {
		if jerr := json.Unmarshal(out.Bytes(), &res); jerr != nil {
			e.t.Fatalf("stdout is not the init JSON: %v\n%s", jerr, out.String())
		}
	}
	return res, errOut.String(), err
}

func (e *initEnv) args(extra ...string) []string {
	return append([]string{
		"--yes", "--forge", "gitlab", "--host", e.srv.URL, "--token-env", initToken,
		"--git-protocol", "https", "--project", e.manifest,
	}, extra...)
}

// onlyToolErrors reports whether every doctor error is a missing tool, which
// is all a machine without zellij or lazygit raises; it says nothing about init.
func onlyToolErrors(checks []doctor.Check) bool {
	for _, c := range checks {
		if c.Status == doctor.Error && c.Group != "tools" {
			return false
		}
	}
	return true
}

// doctorOnly reports whether err is just `ade doctor` finding missing tools
// (exit 1) after everything else succeeded; checks nil means any doctor exit.
func doctorOnly(err error, checks []doctor.Check) bool {
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		return false
	}
	return checks == nil || onlyToolErrors(checks)
}

func TestInitScripted(t *testing.T) {
	e := newInitEnv(t)
	res, progress, err := e.init(e.args()...)
	if err != nil && !doctorOnly(err, res.Doctor) {
		t.Fatalf("init: %v\n%s", err, progress)
	}

	// The config is written where a fresh HOME says, with the manifest's forge name.
	cfg, lerr := config.Load(e.configPath())
	if lerr != nil || cfg.Missing {
		t.Fatalf("config not written at %s: %v", e.configPath(), lerr)
	}
	f, ok := cfg.Forges["team-gl"]
	if !ok {
		t.Fatalf("forge team-gl missing: %+v", cfg.Forges)
	}
	if f.Kind != "gitlab" || f.TokenEnv != initToken || f.GitProtocol != "https" || f.URL != e.srv.URL {
		t.Errorf("forge = %+v", f)
	}
	if cfg.User.Handle == "" {
		t.Error("handle was not taken from the forge")
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Source != e.manifest {
		t.Errorf("projects = %+v, want the manifest registered once", cfg.Projects)
	}
	if b, _ := os.ReadFile(e.configPath()); strings.Contains(string(b), fake.Token) {
		t.Error("the token value was written to the config")
	}

	// The repo was cloned under the default root with its baseline worktree.
	if _, err := os.Stat(filepath.Join(e.home, "ade", "team", "api", "main", "README")); err != nil {
		t.Errorf("baseline worktree missing: %v", err)
	}
	if len(res.Sync) != 1 || res.Sync[0].Err != "" || len(res.Sync[0].Items) == 0 {
		t.Errorf("first sync produced no items: %+v", res.Sync)
	}
	for _, want := range []string{"token ok: authenticated as", "first sync", "ade doctor", "next steps"} {
		if !strings.Contains(progress, want) {
			t.Errorf("progress output lacks %q:\n%s", want, progress)
		}
	}
}

func TestInitNeverOverwritesWithoutForce(t *testing.T) {
	e := newInitEnv(t)
	if err := os.MkdirAll(filepath.Dir(e.configPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	const mine = "# my careful config\n[user]\nhandle = \"keep\"\n"
	if err := os.WriteFile(e.configPath(), []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := e.init(e.args()...)
	if !errors.Is(err, onboard.ErrExists) || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want ErrExists pointing at --force", err)
	}
	if got, _ := os.ReadFile(e.configPath()); string(got) != mine {
		t.Fatalf("config changed without --force:\n%s", got)
	}

	_, _, err = e.init(e.args("--force")...)
	if err != nil && !doctorOnly(err, nil) {
		t.Fatalf("init --force: %v", err)
	}
	matches, _ := filepath.Glob(e.configPath() + ".bak-*")
	if len(matches) != 1 {
		t.Fatalf("backups = %v, want exactly one", matches)
	}
	if got, _ := os.ReadFile(matches[0]); string(got) != mine {
		t.Errorf("backup does not hold the original:\n%s", got)
	}
	if got, _ := os.ReadFile(e.configPath()); string(got) == mine {
		t.Error("--force did not replace the config")
	}
}

func TestInitRejectedTokenWritesNothing(t *testing.T) {
	e := newInitEnv(t)
	t.Setenv(initToken, "wrong-token")
	_, _, err := e.init(e.args()...)
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err = %v, want the forge rejecting the token", err)
	}
	if _, serr := os.Stat(e.configPath()); !os.IsNotExist(serr) {
		t.Errorf("a config was written for a token that does not work (stat: %v)", serr)
	}
}

func TestInitFlagErrors(t *testing.T) {
	e := newInitEnv(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no forge", []string{"--yes", "--token-env", initToken}, "--forge is required"},
		{"bad forge", []string{"--yes", "--forge", "bitbucket", "--token-env", initToken}, "gitlab or github"},
		{"missing manifest", []string{"--yes", "--forge", "gitlab", "--host", e.srv.URL, "--token-env", initToken, "--project", filepath.Join(e.home, "nope.toml")}, "manifest"},
		{"needs --yes off a terminal", []string{"--forge", "gitlab", "--token-env", initToken}, "--yes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := e.init(tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
			if _, serr := os.Stat(e.configPath()); !os.IsNotExist(serr) {
				t.Errorf("config written despite the error")
			}
		})
	}
}

func TestInitManifestsOnDifferentForgesAreRefused(t *testing.T) {
	e := newInitEnv(t)
	other := filepath.Join(t.TempDir(), "other.ade.toml")
	body, _ := os.ReadFile(e.manifest)
	if err := os.WriteFile(other, []byte(strings.Replace(string(body), `forge = "team-gl"`, `forge = "elsewhere"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.init(e.args("--project", other)...)
	if err == nil || !strings.Contains(err.Error(), "different forges") {
		t.Fatalf("err = %v, want different forges", err)
	}
}

func TestInitInvalidManifestIsRefusedBeforeWriting(t *testing.T) {
	e := newInitEnv(t)
	bad := filepath.Join(t.TempDir(), "bad.ade.toml")
	if err := os.WriteFile(bad, []byte("name = \"bad\"\nforge = \"team-gl\"\n[tracker]\nweight = { source = \"nonsense\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.init("--yes", "--forge", "gitlab", "--host", e.srv.URL, "--token-env", initToken, "--project", bad)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("err = %v, want an invalid-manifest error", err)
	}
	if _, serr := os.Stat(e.configPath()); !os.IsNotExist(serr) {
		t.Error("config written for an invalid manifest")
	}
}

func TestGatherPrompts(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", t.TempDir()) // no gh: the default token source is empty
	answers := strings.Join([]string{
		"GitHub", // forge, case-insensitive
		"",       // host: default
		"MY_GH",  // token env
		"nope",   // protocol: invalid, asked again
		"https",
		"/tmp/a.ade.toml", "https://example.com/b.ade.toml", "", // projects
		"/work/ade", // root
	}, "\n") + "\n"
	pr := &prompter{in: bufio.NewReader(strings.NewReader(answers)), out: &bytes.Buffer{}}
	a, sources, err := gather(pr, initFlags{})
	if err != nil {
		t.Fatal(err)
	}
	want := onboard.Answers{Kind: "github", TokenEnv: "MY_GH", GitProtocol: "https", Root: "/work/ade"}
	if a != want {
		t.Errorf("answers = %+v, want %+v", a, want)
	}
	if len(sources) != 2 || sources[0] != "/tmp/a.ade.toml" || sources[1] != "https://example.com/b.ade.toml" {
		t.Errorf("sources = %v", sources)
	}
}

func TestGatherFlagsSkipPromptsAndEOFIsAnError(t *testing.T) {
	// Every answer comes from flags: the prompter must never be read.
	pr := &prompter{in: bufio.NewReader(strings.NewReader("")), out: &bytes.Buffer{}}
	a, _, err := gather(pr, initFlags{kind: "gitlab", host: "git.example.com", tokenCmd: "pass show x", gitProtocol: "ssh", root: "~/src", projects: []string{"https://example.com/m.toml"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.URL != "https://git.example.com" || a.TokenCmd != "pass show x" || a.Root != "~/src" {
		t.Errorf("answers = %+v", a)
	}
	// Missing answers with a closed stdin fail instead of looping.
	if _, _, err := gather(pr, initFlags{}); err == nil || !strings.Contains(err.Error(), "input ended") {
		t.Errorf("err = %v, want input ended", err)
	}
}

// A self-hosted host typed at the prompt gets the CA and ssh-port questions;
// a bad port is an error, 22 is the default and stores nothing, and https
// cloning or the public host never asks.
func TestGatherAsksSSHPortForSelfHostedHost(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")
	run := func(answers ...string) (onboard.Answers, string, error) {
		out := &bytes.Buffer{}
		pr := &prompter{in: bufio.NewReader(strings.NewReader(strings.Join(answers, "\n") + "\n")), out: out}
		a, _, err := gather(pr, initFlags{})
		return a, out.String(), err
	}
	// forge, host, token env, clone over, [CA, ssh port], projects (blank), root (blank)
	a, out, err := run("gitlab", "git.corp.example", "GL", "ssh", "", "2222", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.SSHPort != 2222 || !strings.Contains(out, "SSH port of the forge [22]") {
		t.Errorf("ssh port = %d, prompt shown: %q", a.SSHPort, out)
	}
	if a, _, err = run("gitlab", "git.corp.example", "GL", "ssh", "", "", "", ""); err != nil || a.SSHPort != 0 {
		t.Errorf("default port: %d, %v", a.SSHPort, err)
	}
	if _, _, err = run("gitlab", "git.corp.example", "GL", "ssh", "", "ssh-port"); err == nil || !strings.Contains(err.Error(), "not a number") {
		t.Errorf("err = %v, want not a number", err)
	}
	if _, out, err = run("gitlab", "git.corp.example", "GL", "https", "", "", ""); err != nil || strings.Contains(out, "SSH port") {
		t.Errorf("https cloning asked for an ssh port: %v %q", err, out)
	}
	if _, out, err = run("gitlab", "", "GL", "ssh", "", ""); err != nil || strings.Contains(out, "SSH port") || strings.Contains(out, "CA bundle") {
		t.Errorf("gitlab.com asked a self-hosted question: %v %q", err, out)
	}
}

func TestVersionCommand(t *testing.T) {
	old := Version
	Version = "1.2.3-test"
	t.Cleanup(func() { Version = old })
	var out bytes.Buffer
	cmd := newVersionCmd()
	cmd.SetOut(&out)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "ade 1.2.3-test\n" {
		t.Errorf("version output = %q", got)
	}
}
