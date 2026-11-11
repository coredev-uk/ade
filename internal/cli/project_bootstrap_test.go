package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/bootstrap"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/paths"
)

const bootstrapToken = "BOOTSTRAP_TEST_TOKEN"

// bootstrapEnv is an app whose user config has one forge, "work", pointing at a fake GitLab.
type bootstrapEnv struct {
	t   *testing.T
	srv *fake.Server
	app *app.App
	dir string
}

func newBootstrapEnv(t *testing.T, forges ...string) *bootstrapEnv {
	t.Helper()
	t.Setenv(bootstrapToken, fake.Token)
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	log, err := audit.Open(dir, "user")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	if len(forges) == 0 {
		forges = []string{"work"}
	}
	cfg.Forges = map[string]config.Forge{}
	for _, n := range forges {
		cfg.Forges[n] = config.Forge{Kind: "gitlab", URL: srv.URL, TokenEnv: bootstrapToken}
	}
	a := &app.App{Paths: paths.Paths{CacheDir: dir}, Config: &cfg, Audit: log, Bus: events.NewBus()}
	t.Cleanup(func() { a.Close() })
	return &bootstrapEnv{t: t, srv: srv, app: a, dir: dir}
}

// run executes `ade project bootstrap <args>` and returns stdout and stderr together.
func (e *bootstrapEnv) run(args ...string) (string, error) {
	e.t.Helper()
	cmd := newProjectBootstrapCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("")) // not a terminal
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, e.app))
	return out.String(), err
}

func (e *bootstrapEnv) out(name string) string { return filepath.Join(e.dir, name) }

func TestBootstrapWritesAValidManifest(t *testing.T) {
	e := newBootstrapEnv(t)
	out := e.out("gen.ade.toml")
	text, err := e.run("--forge", "work", "--group", fake.GroupPath, "--yes", "--out", out)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	m, err := config.ParseManifest(b, out)
	if err != nil {
		t.Fatal(err)
	}
	if issues := m.Validate(e.app.Config); len(issues) > 0 {
		t.Errorf("issues: %v", issues)
	}
	if m.Tracker.Project != fake.GroupPath+"/tracker" || m.Forge != "work" || len(m.Repos) != 2 {
		t.Errorf("manifest = %+v", m)
	}
	for _, want := range []string{"tracker   " + fake.GroupPath + "/tracker", "wrote " + out, "ade init --project " + out, "git+" + e.srv.URL + "/" + fake.GroupPath + "/tracker.git#.ade.toml"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	for _, r := range e.srv.Requests() {
		if r.Method != "GET" && !strings.HasSuffix(r.Path, "/graphql") {
			t.Errorf("bootstrap without --publish sent %s %s", r.Method, r.Path)
		}
	}
}

func TestBootstrapNeverOverwritesWithoutForce(t *testing.T) {
	e := newBootstrapEnv(t)
	out := e.out("gen.ade.toml")
	if err := os.WriteFile(out, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := len(e.srv.Requests())
	_, err := e.run("--group", fake.GroupPath, "--yes", "--out", out)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a refusal naming --force", err)
	}
	if b, _ := os.ReadFile(out); string(b) != "# mine\n" {
		t.Errorf("the file was touched: %q", b)
	}
	if len(e.srv.Requests()) != before {
		t.Error("the refusal came after talking to the forge")
	}
	if text, err := e.run("--group", fake.GroupPath, "--yes", "--out", out, "--force"); err != nil {
		t.Fatalf("--force: %v\n%s", err, text)
	}
	if b, _ := os.ReadFile(out); !strings.Contains(string(b), "[tracker]") {
		t.Errorf("--force did not replace the file: %q", b)
	}
}

func TestBootstrapDryRunWritesNothingAndPrintsTheManifest(t *testing.T) {
	e := newBootstrapEnv(t)
	out := e.out("gen.ade.toml")
	text, err := e.run("--group", fake.GroupPath, "--yes", "--out", out, "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("--dry-run wrote the file")
	}
	if !strings.Contains(text, "[tracker]") || !strings.Contains(text, "[labels.workflow]") {
		t.Errorf("the manifest was not printed:\n%s", text)
	}
}

func TestBootstrapJSON(t *testing.T) {
	e := newBootstrapEnv(t)
	asJSON(t)
	text, err := e.run("--group", fake.GroupPath, "--yes", "--out", "-")
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	var res bootstrapResult
	start := strings.Index(text, "{\n")
	if start < 0 {
		t.Fatalf("no JSON in the output:\n%s", text)
	}
	dec := json.NewDecoder(strings.NewReader(text[start:]))
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, text)
	}
	if res.Tracker != fake.GroupPath+"/tracker" || len(res.Repos) != 2 || res.Written || !strings.Contains(res.Manifest, "[tracker]") {
		t.Errorf("result = %+v", res)
	}
}

func TestBootstrapFlagErrors(t *testing.T) {
	e := newBootstrapEnv(t, "work", "other")
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no group":         {[]string{"--forge", "work", "--yes"}, "--group is required"},
		"which forge":      {[]string{"--group", "g", "--yes"}, "pass --forge"},
		"unknown forge":    {[]string{"--forge", "nope", "--group", "g", "--yes"}, "not in your user config"},
		"unknown group":    {[]string{"--forge", "work", "--group", "nope", "--yes"}, "nope"},
		"unknown tracker":  {[]string{"--forge", "work", "--group", fake.GroupPath, "--tracker", "zzz", "--yes"}, "not a repository"},
		"no repos matched": {[]string{"--forge", "work", "--group", fake.GroupPath, "--match", "zzz*", "--yes"}, "--match"},
		"invalid name":     {[]string{"--forge", "work", "--group", fake.GroupPath, "--name", "has space", "--yes"}, "invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"--out", e.out(strings.ReplaceAll(name, " ", "-"))}, c.args...)
			_, err := e.run(args...)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to mention %q", err, c.want)
			}
			if _, serr := os.Stat(e.out(strings.ReplaceAll(name, " ", "-"))); serr == nil {
				t.Error("a failed run left a file")
			}
		})
	}
}

func TestBootstrapPublishDryRunThenReal(t *testing.T) {
	e := newBootstrapEnv(t)
	out := e.out("gen.ade.toml")
	base := []string{"--group", fake.GroupPath, "--yes", "--out", out, "--publish"}

	text, err := e.run(append(base, "--dry-run")...)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	for _, want := range []string{"publish (dry run: nothing is written)", "create branch " + bootstrap.PublishBranch, "title: " + "Add the ADE project manifest", "ade init --project 'git+"} {
		if !strings.Contains(text, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, text)
		}
	}
	if len(e.srv.FileCommits()) != 0 {
		t.Error("the dry run committed")
	}
	dry, err := e.app.Audit.Query(audit.Filter{Action: "forge.mr.create"})
	if err != nil || len(dry) != 1 || !dry[0].DryRun || dry[0].Outcome != "skipped" {
		t.Errorf("dry run audit = %+v %v", dry, err)
	}

	text, err = e.run(base...)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if !strings.Contains(text, "merge request: "+fake.GroupPath+"/tracker!") {
		t.Errorf("the merge request is not reported:\n%s", text)
	}
	if cs := e.srv.FileCommits(); len(cs) != 1 || cs[0].Path != ".ade.toml" || cs[0].Branch != bootstrap.PublishBranch {
		t.Errorf("commits = %+v", cs)
	}
	for _, action := range []string{"forge.branch.create", "forge.file.commit"} {
		if es, _ := e.app.Audit.Query(audit.Filter{Action: action}); len(es) != 1 {
			t.Errorf("%s audited %d times, want once", action, len(es))
		}
	}

	// Again, same flags, no --force: the local file is already this manifest, so it is
	// neither a refusal nor a rewrite; the same MR, no new write.
	if text, err = e.run(base...); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if !strings.Contains(text, "is already this manifest") {
		t.Errorf("the re-run should say the file is unchanged:\n%s", text)
	}
	if len(e.srv.FileCommits()) != 1 {
		t.Errorf("a second run committed again: %+v", e.srv.FileCommits())
	}
	if es, _ := e.app.Audit.Query(audit.Filter{Action: "forge.mr.create"}); len(es) != 2 { // the dry run and the real one
		t.Errorf("forge.mr.create entries = %d, want 2", len(es))
	}
}

func TestBootstrapPublishDoesNotOverwriteADifferentLocalFile(t *testing.T) {
	e := newBootstrapEnv(t)
	out := e.out("gen.ade.toml")
	if err := os.WriteFile(out, []byte("# hand edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := e.run("--group", fake.GroupPath, "--yes", "--out", out, "--publish")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a refusal naming --force", err)
	}
	if b, _ := os.ReadFile(out); string(b) != "# hand edited\n" {
		t.Errorf("the file was touched: %q", b)
	}
	if len(e.srv.FileCommits()) != 0 {
		t.Error("nothing may be published when the local write was refused")
	}
}

func TestBootstrapPromptsNumberTheOptions(t *testing.T) {
	var out bytes.Buffer
	pr := &prompter{in: bufio.NewReader(strings.NewReader("9\nnope\n2\n\n")), out: &out}
	c := promptChooser{pr}
	opts := []string{"g/alpha  (0 issues)", "g/beta  (0 issues)"}

	got, err := c.Choose("Which tracker?", opts, opts[0])
	if err != nil || got != opts[1] {
		t.Fatalf("= %q %v, want the second option after two bad answers", got, err)
	}
	if !strings.Contains(out.String(), "1) g/alpha") || !strings.Contains(out.String(), "enter a number from 1 to 2") {
		t.Errorf("prompt output:\n%s", out.String())
	}
	if got, err = c.Choose("Again?", opts, opts[1]); err != nil || got != opts[1] {
		t.Errorf("an empty answer takes the default: %q %v", got, err)
	}
	if _, err := c.Choose("EOF?", opts, opts[0]); err == nil {
		t.Error("input ending before an answer must be an error")
	}
}

func TestPickBootstrapForge(t *testing.T) {
	two := &config.Config{Forges: map[string]config.Forge{"b": {Kind: "gitlab"}, "a": {Kind: "github"}}}
	one := &config.Config{Forges: map[string]config.Forge{"only": {Kind: "gitlab"}}}
	none := &config.Config{}

	if got, err := pickBootstrapForge(nil, one, ""); err != nil || got != "only" {
		t.Errorf("the only forge is the default: %q %v", got, err)
	}
	if got, err := pickBootstrapForge(nil, two, "b"); err != nil || got != "b" {
		t.Errorf("named: %q %v", got, err)
	}
	if _, err := pickBootstrapForge(nil, two, ""); err == nil || !strings.Contains(err.Error(), "a, b") {
		t.Errorf("several forges unattended: %v, want the sorted choices", err)
	}
	if _, err := pickBootstrapForge(nil, none, ""); err == nil || !strings.Contains(err.Error(), "ade init") {
		t.Errorf("no forge: %v", err)
	}
	pr := &prompter{in: bufio.NewReader(strings.NewReader("b\n")), out: &bytes.Buffer{}}
	if got, err := pickBootstrapForge(pr, two, ""); err != nil || got != "b" {
		t.Errorf("asked: %q %v", got, err)
	}
}
