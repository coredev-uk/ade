package config

import (
	"os"
	"strings"
	"testing"
)

func mustManifest(t *testing.T, src string) *Manifest {
	t.Helper()
	m, err := ParseManifest([]byte(src), "test.ade.toml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m
}

var testCfg = &Config{Forges: map[string]Forge{"work": {Kind: "gitlab"}}}

// want is "<severity> <path>: <message substring>".
func assertIssues(t *testing.T, got []Issue, want ...string) {
	t.Helper()
	var rendered []string
	for _, i := range got {
		rendered = append(rendered, i.String())
	}
	for _, w := range want {
		found := false
		for _, r := range rendered {
			if strings.Contains(r, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing issue containing %q; got:\n  %s", w, strings.Join(rendered, "\n  "))
		}
	}
	if len(want) == 0 && len(got) > 0 {
		t.Errorf("want no issues, got:\n  %s", strings.Join(rendered, "\n  "))
	}
}

func TestPlanExampleValidatesClean(t *testing.T) {
	data, err := os.ReadFile("testdata/acme.ade.toml")
	if err != nil {
		t.Fatal(err)
	}
	m := mustManifest(t, string(data))
	assertIssues(t, m.Validate(testCfg))
}

const header = "name = \"p\"\nforge = \"work\"\n"

func TestManifestValidate(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"minimal clean", header + "[repos.a]\npath = \"g/a\"\n", nil},
		{"dotfiles clean", header + "[dotfiles]\nrepo = \"git@gl.example.com:team/dotfiles.git\"\nbranch = \"main\"\ninstall = \"script/setup\"\nsymlink = false\n", nil},
		{"dotfiles without repo", header + "[dotfiles]\nbranch = \"main\"\n", []string{"error: dotfiles.repo: required"}},
		{"dotfiles repo as option", header + "[dotfiles]\nrepo = \"--upload-pack=x\"\n", []string{"error: dotfiles.repo:", "not an option"}},
		{"dotfiles install outside the repo", header + "[dotfiles]\nrepo = \"https://x/d.git\"\ninstall = \"../evil.sh\"\n", []string{"error: dotfiles.install:"}},
		{"dotfiles absolute install", header + "[dotfiles]\nrepo = \"https://x/d.git\"\ninstall = \"/tmp/evil.sh\"\n", []string{"error: dotfiles.install:"}},
		{"dotfiles unknown key warns", header + "[dotfiles]\nrepo = \"https://x/d.git\"\nrun = \"x\"\n", []string{"warn: dotfiles.run: unknown key"}},
		{"commit prefix", header + "[conventions]\ncommit_prefix = \"GL-{parent}\"\n", nil},
		{"commit prefix unknown placeholder", header + "[conventions]\ncommit_prefix = \"GL-{parnt}\"\n", []string{"error: conventions.commit_prefix: unknown placeholder {parnt}"}},
		{"commit prefix with whitespace", header + "[conventions]\ncommit_prefix = \"GL-{parent} \"\n", []string{"error: conventions.commit_prefix:", "whitespace"}},
		{"missing name", "forge = \"work\"\n", []string{"error: name: required"}},
		{"unsafe name", "name = \"../x\"\nforge = \"work\"\n", []string{"error: name:", "not path-safe"}},
		{"missing forge with forge-path repo", "name = \"p\"\n[repos.a]\npath = \"g/a\"\n", []string{"error: forge: required"}},
		{"missing forge with tracker", "name = \"p\"\n[tracker]\nproject = \"g/t\"\n", []string{"error: forge: required"}},
		{"no forge needed when every repo has url", "name = \"p\"\n[repos.a]\nurl = \"https://example.com/a.git\"\n", nil},
		{"unknown forge", "name = \"p\"\nforge = \"nope\"\n", []string{"error: forge: forge \"nope\" is not defined"}},
		{"repo without path or url", header + "[repos.a]\n", []string{"error: repos.a: needs path or url"}},
		{"repo with url only", header + "[repos.a]\nurl = \"https://example.com/a.git\"\n", nil},
		{"reserved repo name", header + "[repos.services]\npath = \"x\"\n", []string{"repos.services: \"services\" is reserved"}},
		{
			"unknown repo require", header + "[repos.a]\npath = \"x\"\nrequires = [\"b\"]\n",
			[]string{"error: repos.a.requires[0]: unknown repo \"b\""},
		},
		{
			"checkout cycle", header + "[repos.a]\npath = \"x\"\nrequires = [\"b\"]\n[repos.b]\npath = \"y\"\nrequires = [\"a\"]\n",
			[]string{"checkout dependency cycle: a → b → a"},
		},
		{
			"checkout self cycle", header + "[repos.a]\npath = \"x\"\nrequires = [\"a\"]\n",
			[]string{"checkout dependency cycle: a → a"},
		},
		{"qualified repo require alone is unchecked", header + "[repos.a]\npath = \"x\"\nrequires = [\"shared/auth\"]\n", nil},
		{
			"unknown task require", header + "[repos.a]\npath = \"x\"\n[repos.a.tasks.t]\ncmd = \"x\"\nrequires = [\"a.nope\"]\n",
			[]string{"error: repos.a.tasks.t.requires[0]: unknown reference \"a.nope\""},
		},
		{
			"malformed require", header + "[repos.a]\npath = \"x\"\n[repos.a.tasks.t]\ncmd = \"x\"\nrequires = [\"a\"]\n",
			[]string{"requires[0]: malformed reference"},
		},
		{"qualified require alone is unchecked", header + "[repos.a]\npath = \"x\"\n[repos.a.tasks.t]\ncmd = \"x\"\nrequires = [\"shared/auth.dev\"]\n", nil},
		{"run cycle with path", header + `
[repos.a]
path = "x"
[repos.a.tasks.one]
cmd = "x"
requires = ["a.two"]
[repos.a.tasks.two]
cmd = "x"
requires = ["services.s"]
[services.s]
cmd = "x"
requires = ["a.one"]
`, []string{"error: services.s.requires[0]: dependency cycle: a.one → a.two → services.s → a.one"}},
		{"bad kind", header + "[services.s]\ncmd = \"x\"\nkind = \"daemon\"\n", []string{"services.s.kind: unknown kind \"daemon\""}},
		{"missing cmd", header + "[services.s]\nport = \"auto\"\n", []string{"services.s.cmd: required"}},
		{
			"two ready probes", header + "[services.s]\ncmd = \"x\"\nready = { http = \"http://x\", tcp = \"x:1\" }\n",
			[]string{"services.s.ready: set at most one"},
		},
		{
			"oneshot with ready", header + "[services.s]\ncmd = \"x\"\nkind = \"oneshot\"\nready = { tcp = \"x:1\" }\n",
			[]string{"services.s.ready.tcp: oneshot tasks are ready when they exit 0"},
		},
		{"oneshot without ready", header + "[services.s]\ncmd = \"x\"\nkind = \"oneshot\"\n", nil},
		{"bad ready log regexp", header + "[services.s]\ncmd = \"x\"\nready = { log = \"(\" }\n", []string{"services.s.ready.log: invalid regular expression"}},
		{
			"fixed port clash", header + "[services.a]\ncmd = \"x\"\nport = 5432\n[services.b]\ncmd = \"x\"\nport = 5432\n",
			[]string{"error: services.b.port: fixed port 5432 is also used by services.a"},
		},
		{"auto ports never clash", header + "[services.a]\ncmd = \"x\"\nport = \"auto\"\n[services.b]\ncmd = \"x\"\nport = \"auto\"\n", nil},
		{"invalid task name", header + "[services.\"a.b\"]\ncmd = \"x\"\n", []string{"services.a.b: invalid name"}},
		{
			"group unknown member", header + "[services.s]\ncmd = \"x\"\n[groups]\ndev = [\"services.s\", \"s.t\"]\n",
			[]string{"error: groups.dev[1]: unknown reference \"s.t\""},
		},
		{
			"workspace unknown group", header + "[workspaces]\nd = { layout = \"code\", run = \"nope\" }\n",
			[]string{"error: workspaces.d.run: unknown group \"nope\""},
		},
		{"workspace services background", header + "[workspaces]\nd = { services = \"background\" }\n", nil},
		{"workspace services manual", header + "[workspaces]\nd = { services = \"manual\" }\n", nil},
		{"workspace services unknown", header + "[workspaces]\nd = { services = \"auto\" }\n", []string{"error: workspaces.d.services: unknown value \"auto\""}},
		{"install hooks", header + "[repos.web]\npath = \"a/web\"\n[repos.web.hooks]\ninstall = [\"npm ci\", \"npm run build\"]\n", nil},
		{"empty install command", header + "[repos.web]\npath = \"a/web\"\n[repos.web.hooks]\ninstall = [\"npm ci\", \"  \"]\n", []string{"error: repos.web.hooks.install[1]: empty command"}},
		{"workspace without run", header + "[workspaces]\nd = { layout = \"code\" }\n", nil},
		{"label pattern without value", header + "[labels.type]\npattern = \"type::\"\n", []string{"labels.type.pattern: pattern \"type::\" must contain {value}"}},
		{"workflow pattern without value", header + "[labels.workflow]\npattern = \"wf\"\n", []string{"labels.workflow.pattern:"}},
		{"bad workflow sync", header + "[labels.workflow]\nsync = \"sometimes\"\n", []string{"labels.workflow.sync:"}},
		{"bad iteration source", header + "[tracker]\niterations = { source = \"jira\" }\n", []string{"tracker.iterations.source:"}},
		{"good iteration source", header + "[tracker]\niterations = { source = \"cadence\" }\n", nil},
		{"unknown key warns", header + "colour = \"red\"\n", []string{"warn: colour: unknown key"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertIssues(t, mustManifest(t, tt.src).Validate(testCfg), tt.want...)
		})
	}
}

func TestNilConfigSkipsForgeLookup(t *testing.T) {
	m := mustManifest(t, "name = \"p\"\nforge = \"anything\"\n")
	assertIssues(t, m.Validate(nil))
}

const envBase = header + `
[repos.api]
path = "x"
[repos.api.tasks.dev]
cmd = "x"
port = "auto"
[repos.web]
path = "y"
[repos.lib]
path = "z"
[repos.web.tasks.dev]
cmd = "x"
port = "auto"
`

func TestEnvTemplates(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"own port dir state", envBase + "[repos.web.tasks.dev.env]\nA = \"{port}{dir}{state}\"\n", nil},
		{"shell syntax ignored", envBase + "[repos.web.tasks.dev.env]\nA = \"${HOME}/x\"\n", nil},
		{
			"port without port", envBase + "[repos.web.tasks.t]\ncmd = \"x\"\n[repos.web.tasks.t.env]\nA = \"{port}\"\n",
			[]string{"repos.web.tasks.t.env.A: {port} used but this node declares no port"},
		},
		{"ref in requires closure", strings.Replace(envBase, "[repos.web.tasks.dev]\n", "[repos.web.tasks.dev]\nrequires = [\"api.dev\"]\n", 1) +
			"[repos.web.tasks.dev.env]\nA = \"{api.dev.port}\"\n", nil},
		{
			"ref outside requires closure", envBase + "[repos.web.tasks.dev.env]\nA = \"{api.dev.port}\"\n",
			[]string{"error: repos.web.tasks.dev.env.A: {api.dev.port} references api.dev, which is outside this node's requires closure"},
		},
		{"transitive closure", header + `
[services.db]
cmd = "x"
port = "auto"
[repos.api]
path = "x"
[repos.api.tasks.m]
cmd = "x"
requires = ["services.db"]
[repos.api.tasks.dev]
cmd = "x"
requires = ["api.m"]
env = { D = "{services.db.port}" }
`, nil},
		{"ref to unknown node", envBase + "[repos.web.tasks.dev.env]\nA = \"{api.nope.port}\"\n", []string{"unknown reference \"api.nope\""}},
		{"ref to node without port", header + `
[repos.api]
path = "x"
[repos.api.tasks.m]
cmd = "x"
[repos.api.tasks.dev]
cmd = "x"
requires = ["api.m"]
env = { D = "{api.m.port}" }
`, []string{"api.m declares no port"}},
		{"unknown variable", envBase + "[repos.web.tasks.dev.env]\nA = \"{value}\"\n", []string{"unknown template variable {value}"}},
		{"repo dir own", envBase + "[repos.web.tasks.dev.env]\nA = \"{web.dir}\"\n", nil},
		{"repo dir via checkout requires", strings.Replace(envBase, "[repos.web]\npath = \"y\"\n", "[repos.web]\npath = \"y\"\nrequires = [\"lib\"]\n", 1) +
			"[repos.web.tasks.dev.env]\nA = \"{lib.dir}\"\n", nil},
		{"repo dir outside scope", envBase + "[repos.web.tasks.dev.env]\nA = \"{lib.dir}\"\n", []string{"{lib.dir} references a repo that is neither"}},
		{"repo dir unknown repo", envBase + "[repos.web.tasks.dev.env]\nA = \"{ghost.dir}\"\n", []string{"{ghost.dir}: unknown repo \"ghost\""}},
		{"qualified ref declared", strings.Replace(envBase, "[repos.web.tasks.dev]\n", "[repos.web.tasks.dev]\nrequires = [\"shared/auth.dev\"]\n", 1) +
			"[repos.web.tasks.dev.env]\nA = \"{shared/auth.dev.port}\"\n", nil},
		{"qualified ref undeclared", envBase + "[repos.web.tasks.dev.env]\nA = \"{shared/auth.dev.port}\"\n", []string{"outside this node's requires closure"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertIssues(t, mustManifest(t, tt.src).Validate(testCfg), tt.want...)
		})
	}
}

func TestValidateAllResolvesQualifiedRefs(t *testing.T) {
	shared := mustManifest(t, "name = \"shared\"\nforge = \"work\"\n[repos.auth]\npath = \"x\"\n[repos.auth.tasks.dev]\ncmd = \"x\"\n")
	shared.Source = "shared.ade.toml"
	user := mustManifest(t, header+`
[repos.web]
path = "w"
requires = ["shared/auth", "shared/ghost", "nowhere/x"]
[repos.web.tasks.dev]
cmd = "x"
requires = ["shared/auth.dev", "shared/auth.nope", "nowhere/x.y"]
[groups]
g = ["shared/auth.dev", "shared/auth.zzz"]
`)
	user.Source = "user.ade.toml"
	res := ValidateAll([]*Manifest{shared, user}, testCfg)
	assertIssues(t, res["shared.ade.toml"])
	got := res["user.ade.toml"]
	assertIssues(t, got,
		"repos.web.requires[1]: manifest \"shared\" has no repo \"ghost\"",
		"repos.web.requires[2]: unknown manifest \"nowhere\"",
		"repos.web.tasks.dev.requires[1]: manifest \"shared\" has no node \"auth.nope\"",
		"repos.web.tasks.dev.requires[2]: unknown manifest \"nowhere\"",
		"groups.g[1]: manifest \"shared\" has no node \"auth.zzz\"",
	)
	if len(got) != 5 {
		t.Errorf("want exactly 5 issues, got %d: %v", len(got), got)
	}
}

func TestValidateAllDuplicateNames(t *testing.T) {
	a := mustManifest(t, header)
	a.Source = "a.toml"
	b := mustManifest(t, header)
	b.Source = "b.toml"
	res := ValidateAll([]*Manifest{a, b}, testCfg)
	assertIssues(t, res["a.toml"])
	assertIssues(t, res["b.toml"], "error: name: project name \"p\" is already defined by a.toml")
}

func TestHasErrors(t *testing.T) {
	if HasErrors([]Issue{{Severity: SeverityWarn}}) {
		t.Error("warning counted as error")
	}
	if !HasErrors([]Issue{{Severity: SeverityWarn}, {Severity: SeverityError}}) {
		t.Error("error not detected")
	}
}
