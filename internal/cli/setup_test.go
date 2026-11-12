package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// setupEnvVars points a Coder-style environment at the fake forge.
func (e *initEnv) setupEnvVars() {
	e.t.Setenv("ADE_SETUP_FORGE", "gitlab")
	e.t.Setenv("ADE_SETUP_HOST", e.srv.URL)
	e.t.Setenv("ADE_SETUP_TOKEN_ENV", initToken)
	e.t.Setenv("ADE_SETUP_GIT_PROTOCOL", "https")
	e.t.Setenv("ADE_SETUP_PROJECT", e.manifest)
}

// setup runs `ade setup <args>` with --json.
func (e *initEnv) setup(args ...string) (setupResult, string, error) {
	e.t.Helper()
	asJSON(e.t)
	cmd := newSetupCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.Background())
	var res setupResult
	if out.Len() > 0 {
		if jerr := json.Unmarshal(out.Bytes(), &res); jerr != nil {
			e.t.Fatalf("stdout is not the setup JSON: %v\n%s", jerr, out.String())
		}
	}
	return res, errOut.String(), err
}

func exitOf(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	if err != nil {
		return -1
	}
	return 0
}

func (e *initEnv) clonedFile() string {
	return filepath.Join(e.home, "ade", "team", "api", "main", "README")
}

func TestSetupCreatesConfigFromEnv(t *testing.T) {
	e := newInitEnv(t)
	e.setupEnvVars()
	e.t.Setenv("ADE_SETUP_HANDLE", "")

	res, progress, err := e.setup()
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, progress)
	}
	if res.Status != "ok" || !res.ConfigCreated {
		t.Errorf("status = %q created = %v, want ok/true\n%s", res.Status, res.ConfigCreated, progress)
	}
	cfg, lerr := config.Load(e.configPath())
	if lerr != nil || cfg.Missing {
		t.Fatalf("config not written: %v", lerr)
	}
	f := cfg.Forges["team-gl"]
	if f.Kind != "gitlab" || f.TokenEnv != initToken || f.URL != e.srv.URL {
		t.Errorf("forge = %+v", f)
	}
	if cfg.User.Handle == "" {
		t.Error("handle was not taken from the forge")
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Source != e.manifest {
		t.Errorf("projects = %+v", cfg.Projects)
	}
	if b, _ := os.ReadFile(e.configPath()); strings.Contains(string(b), "fake") && strings.Contains(string(b), initToken+"=") {
		t.Error("token written to the config")
	}
	if _, err := os.Stat(e.clonedFile()); err != nil {
		t.Errorf("baseline worktree missing: %v", err)
	}
	if len(res.Sync) != 1 || res.Sync[0].Error != "" || res.Sync[0].Items == 0 {
		t.Errorf("first sync: %+v", res.Sync)
	}
	if res.ZellijConfig == "" {
		t.Error("zellij preset not written")
	} else if _, err := os.Stat(res.ZellijConfig); err != nil {
		t.Error(err)
	}
	if len(res.Tools) == 0 {
		t.Error("no tool report")
	}
}

func TestSetupFlagsBeatEnv(t *testing.T) {
	e := newInitEnv(t)
	e.setupEnvVars()
	t.Setenv("ADE_SETUP_ROOT", filepath.Join(e.home, "from-env"))
	_, progress, err := e.setup("--root", filepath.Join(e.home, "from-flag"))
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, progress)
	}
	if _, err := os.Stat(filepath.Join(e.home, "from-flag", "team", "api", "main", "README")); err != nil {
		t.Errorf("--root was not used: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.home, "from-env")); err == nil {
		t.Error("the env root won over the flag")
	}
}

func TestSetupExistingConfigUntouchedAndSecondRunIsNoop(t *testing.T) {
	e := newInitEnv(t)
	e.setupEnvVars()
	if _, p, err := e.setup(); err != nil {
		t.Fatalf("first run: %v\n%s", err, p)
	}
	before, _ := os.ReadFile(e.configPath())

	// A different environment must not change an existing config.
	t.Setenv("ADE_SETUP_HANDLE", "someone-else")
	t.Setenv("ADE_SETUP_ROOT", filepath.Join(e.home, "elsewhere"))
	res, p, err := e.setup()
	if err != nil {
		t.Fatalf("second run: %v\n%s", err, p)
	}
	if res.ConfigCreated {
		t.Error("second run claims to have created the config")
	}
	after, _ := os.ReadFile(e.configPath())
	if !bytes.Equal(before, after) {
		t.Errorf("config changed:\n--- before\n%s\n--- after\n%s", before, after)
	}
	for _, rep := range res.Add {
		for _, r := range rep.Results {
			if r.Cloned {
				t.Errorf("second run cloned %s again", r.Repo)
			}
		}
	}
	if res.Update == nil || res.Update.Changed() {
		t.Errorf("second run changed repos: %+v", res.Update)
	}
	if _, err := os.Stat(filepath.Join(e.home, "elsewhere")); err == nil {
		t.Error("the env root created a second tree")
	}
}

func TestSetupRejectedTokenIsPartial(t *testing.T) {
	for _, tt := range []struct{ name, token, want string }{
		{"rejected", "wrong-token", "rejected"},
		{"empty", "", "empty"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newInitEnv(t)
			e.setupEnvVars()
			t.Setenv(initToken, tt.token)
			res, p, err := e.setup()
			if exitOf(err) != exitSetupPartial {
				t.Fatalf("exit = %d (%v), want %d\n%s", exitOf(err), err, exitSetupPartial, p)
			}
			if res.Status != "partial" || len(res.Tokens) != 1 || !strings.Contains(res.Tokens[0].Error, tt.want) {
				t.Errorf("result = %+v, want a %q token error", res, tt.want)
			}
			if len(res.Skipped) == 0 || res.Add != nil || res.Sync != nil {
				t.Errorf("forge steps ran anyway: %+v", res)
			}
			if _, err := os.Stat(e.clonedFile()); err == nil {
				t.Error("a repo was cloned without a working token")
			}
			if len(res.Tools) == 0 || res.ZellijConfig == "" {
				t.Error("machine steps were skipped along with the forge steps")
			}
			if strings.Contains(p, "Password") {
				t.Error("setup prompted")
			}
			// The config is there, so fixing the token and rerunning finishes the job.
			cfg, _ := config.Load(e.configPath())
			if cfg.Missing || len(cfg.Projects) != 1 {
				t.Fatalf("config after a token failure: missing=%v projects=%+v", cfg.Missing, cfg.Projects)
			}
		})
	}
}

func TestSetupRerunWithFixedTokenCompletes(t *testing.T) {
	e := newInitEnv(t)
	e.setupEnvVars()
	t.Setenv(initToken, "wrong-token")
	if _, _, err := e.setup(); exitOf(err) != exitSetupPartial {
		t.Fatalf("first run exit = %d (%v)", exitOf(err), err)
	}
	t.Setenv(initToken, fake.Token)
	res, p, err := e.setup()
	if err != nil {
		t.Fatalf("rerun: %v\n%s", err, p)
	}
	if res.ConfigCreated {
		t.Error("rerun recreated the config")
	}
	if _, err := os.Stat(e.clonedFile()); err != nil {
		t.Errorf("rerun did not clone: %v", err)
	}
}

func TestSetupInvalidConfigFails(t *testing.T) {
	e := newInitEnv(t)
	if err := os.MkdirAll(filepath.Dir(e.configPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	const broken = "this is = = not toml\n"
	if err := os.WriteFile(e.configPath(), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.setup()
	if exitOf(err) != exitSetupFailed {
		t.Fatalf("exit = %d (%v), want %d", exitOf(err), err, exitSetupFailed)
	}
	if got, _ := os.ReadFile(e.configPath()); string(got) != broken {
		t.Error("the invalid config was modified")
	}
}

func TestSetupNothingToCreateFrom(t *testing.T) {
	e := newInitEnv(t)
	res, _, err := e.setup()
	if exitOf(err) != exitSetupFailed {
		t.Fatalf("exit = %d (%v), want %d", exitOf(err), err, exitSetupFailed)
	}
	if !strings.Contains(strings.Join(res.Problems, " "), "ADE_SETUP_FORGE") {
		t.Errorf("problems = %v, want the variables to set", res.Problems)
	}
	if _, serr := os.Stat(e.configPath()); !os.IsNotExist(serr) {
		t.Error("a config was written from nothing")
	}
}

func TestSetupConfigFromTemplate(t *testing.T) {
	e := newInitEnv(t)
	tmpl := filepath.Join(t.TempDir(), "team.toml")
	body := "[user]\nhandle = \"template-user\"\n\n[forges.team-gl]\nkind = \"gitlab\"\nurl = \"" + e.srv.URL +
		"\"\ntoken_env = \"" + initToken + "\"\ngit_protocol = \"https\"\n\n[[projects]]\nsource = \"" + e.manifest + "\"\n"
	if err := os.WriteFile(tmpl, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	res, p, err := e.setup("--config-from", tmpl)
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, p)
	}
	if got, _ := os.ReadFile(e.configPath()); string(got) != body {
		t.Errorf("config is not the template:\n%s", got)
	}
	if !res.ConfigCreated {
		t.Error("config_created = false")
	}
	if _, err := os.Stat(e.clonedFile()); err != nil {
		t.Errorf("template project not cloned: %v", err)
	}

	// An invalid template is refused and nothing is written.
	e2 := newInitEnv(t)
	bad := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(bad, []byte("[forges.x]\nkind = \"bitbucket\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e2.setup("--config-from", bad); exitOf(err) != exitSetupFailed {
		t.Errorf("invalid template exit = %d (%v)", exitOf(err), err)
	}
	if _, serr := os.Stat(e2.configPath()); !os.IsNotExist(serr) {
		t.Error("invalid template was written")
	}
}

func TestSetupConcurrentRunsSerialise(t *testing.T) {
	e := newInitEnv(t)
	e.setupEnvVars()
	asJSON(t)
	const n = 3
	type out struct {
		res setupResult
		err error
		log string
	}
	outs := make([]out, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := newSetupCmd()
			var so, se bytes.Buffer
			cmd.SetOut(&so)
			cmd.SetErr(&se)
			cmd.SetArgs(nil)
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			outs[i].err = cmd.ExecuteContext(context.Background())
			_ = json.Unmarshal(so.Bytes(), &outs[i].res)
			outs[i].log = se.String()
		}()
	}
	wg.Wait()
	created, cloned, waited := 0, 0, 0
	for i, o := range outs {
		if o.err != nil {
			t.Errorf("run %d: %v\n%s", i, o.err, o.log)
		}
		if o.res.ConfigCreated {
			created++
		}
		for _, rep := range o.res.Add {
			for _, r := range rep.Results {
				if r.Cloned {
					cloned++
				}
			}
		}
		if strings.Contains(o.log, "waiting for it") {
			waited++
		}
	}
	if created != 1 || cloned != 1 {
		t.Errorf("created %d configs and cloned %d times across %d concurrent runs, want 1 and 1", created, cloned, n)
	}
	if waited == 0 {
		t.Log("no run had to wait (they happened not to overlap)")
	}
}

func TestApplySetupEnv(t *testing.T) {
	newFS := func() (*pflag.FlagSet, *string, *[]string, *bool) {
		fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
		var host string
		var projects []string
		var warm bool
		fs.StringVar(&host, "host", "", "")
		fs.StringArrayVar(&projects, "project", nil, "")
		fs.BoolVar(&warm, "warm", false, "")
		fs.String("token-env", "", "")
		return fs, &host, &projects, &warm
	}
	env := map[string]string{
		"ADE_SETUP_HOST": "from-env", "ADE_SETUP_PROJECT": "a.toml\n https://x/b.toml  c.toml",
		"ADE_SETUP_WARM": "true", "ADE_SETUP_TOKEN_ENV": "TOK",
	}
	get := func(k string) string { return env[k] }

	fs, host, projects, warm := newFS()
	if err := fs.Parse([]string{"--host", "from-flag"}); err != nil {
		t.Fatal(err)
	}
	if err := applySetupEnv(fs, get); err != nil {
		t.Fatal(err)
	}
	if *host != "from-flag" {
		t.Errorf("host = %q, a flag must win over the variable", *host)
	}
	if got := strings.Join(*projects, "|"); got != "a.toml|https://x/b.toml|c.toml" {
		t.Errorf("projects = %q", got)
	}
	if !*warm {
		t.Error("ADE_SETUP_WARM=true was ignored")
	}
	if v, _ := fs.GetString("token-env"); v != "TOK" {
		t.Errorf("token-env = %q", v)
	}

	env["ADE_SETUP_WARM"] = "maybe"
	fs, _, _, _ = newFS()
	if err := applySetupEnv(fs, get); err == nil || !strings.Contains(err.Error(), "ADE_SETUP_WARM") {
		t.Errorf("err = %v, want the bad variable named", err)
	}
}

// fakeZellij is a zellij stand-in on PATH that records its argv and knows one
// session once it has been asked to create it.
func fakeZellij(t *testing.T, name string) (logFile string) {
	t.Helper()
	dir := t.TempDir()
	logFile = filepath.Join(dir, "calls.log")
	state := filepath.Join(dir, "state")
	script := "#!/bin/sh\necho \"$@\" >> '" + logFile + "'\n" +
		"case \"$*\" in *list-sessions*) [ -f '" + state + "' ] && echo '" + name + " [Created 1s ago]'; exit 0;; esac\n" +
		"touch '" + state + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "zellij"), []byte(script), 0o755); err != nil { //nolint:gosec // test stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logFile
}

func TestSetupSessionStartsOnceAndNeverTouchesOthers(t *testing.T) {
	e := newInitEnv(t)
	e.setupEnvVars()
	const name = "ade-test-setup"
	logFile := fakeZellij(t, name)

	res, p, err := e.setup("--session", "--session-name", name)
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, p)
	}
	if res.Session == nil || res.Session.Status != "started" {
		t.Fatalf("session = %+v, want started", res.Session)
	}
	calls, _ := os.ReadFile(logFile)
	if !strings.Contains(string(calls), "attach --create-background "+name) || !strings.Contains(string(calls), "--layout") ||
		!strings.Contains(string(calls), "--config") {
		t.Errorf("zellij calls:\n%s", calls)
	}
	if strings.Contains(string(calls), "attach --create-background ade\n") {
		t.Error("touched the real ade session")
	}
	layout, err := os.ReadFile(filepath.Join(e.home, ".cache", "ade", "layout.kdl"))
	if err != nil || !strings.Contains(string(layout), "layout") {
		t.Errorf("layout file: %v", err)
	}

	res, p, err = e.setup("--session", "--session-name", name)
	if err != nil {
		t.Fatalf("second setup: %v\n%s", err, p)
	}
	if res.Session == nil || res.Session.Status != "running" {
		t.Errorf("second run session = %+v, want running", res.Session)
	}
	after, _ := os.ReadFile(logFile)
	if n := strings.Count(string(after), "attach"); n != 1 {
		t.Errorf("attach ran %d times over two setups, want once:\n%s", n, after)
	}
}

func TestSetupCAFileFromEnvReachesConfig(t *testing.T) {
	e := newInitEnv(t)
	e.setupEnvVars()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, selfSignedPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADE_SETUP_CA_FILE", ca)
	// The fake forge is plain http, so the CA is irrelevant to the calls; the
	// point is that the answer is written.
	if _, p, err := e.setup(); err != nil {
		t.Fatalf("setup: %v\n%s", err, p)
	}
	b, _ := os.ReadFile(e.configPath())
	if !strings.Contains(string(b), ca) {
		t.Errorf("ca_file missing from the config:\n%s", b)
	}
}

func selfSignedPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example internal ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
