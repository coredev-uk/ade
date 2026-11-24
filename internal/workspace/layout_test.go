package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var fullTools = Tools{Ade: "/opt/ade/bin/ade", Lazygit: "/bin/lazygit", Delta: "/bin/delta", Omp: "/bin/omp"}

func baseSpec(layout string) LayoutSpec {
	return LayoutSpec{
		Layout: layout, Tab: "web/418-paginate", Dir: "/projects/acme/web/418-paginate", BaseRef: "origin/main", Tools: fullTools,
		Up:       Pane{Name: "services", Args: []string{"ws", "up", "/projects/acme/web/418-paginate", "--no-zellij"}},
		Services: []Pane{{Name: "api.dev", Args: []string{"task", "run", "api.dev", "--worktree", "/projects/acme/web/418-paginate"}}},
		Tasks:    []Pane{{Name: "web.dev", Args: []string{"task", "run", "web.dev", "--worktree", "/projects/acme/web/418-paginate"}}},
	}
}

func TestBuildLayoutCode(t *testing.T) {
	got, err := BuildLayout(baseSpec(LayoutCode))
	if err != nil {
		t.Fatal(err)
	}
	want := `layout {
    default_tab_template {
        pane size=1 borderless=true {
            plugin location="zellij:tab-bar"
        }
        children
        pane size=2 borderless=true {
            plugin location="zellij:status-bar"
        }
    }
    tab name="web/418-paginate" cwd="/projects/acme/web/418-paginate" focus=true {
        pane size="70%" stacked=true {
            pane name="shell" expanded=true focus=true
            pane name="lazygit" command="/bin/lazygit"
        }
        pane size="30%" split_direction="vertical" {
            pane name="services" start_suspended=true command="/opt/ade/bin/ade" {
                args "ws" "up" "/projects/acme/web/418-paginate" "--no-zellij"
            }
            pane name="api.dev" start_suspended=true command="/opt/ade/bin/ade" {
                args "task" "run" "api.dev" "--worktree" "/projects/acme/web/418-paginate"
            }
            pane name="web.dev" start_suspended=true command="/opt/ade/bin/ade" {
                args "task" "run" "web.dev" "--worktree" "/projects/acme/web/418-paginate"
            }
        }
    }
}
`
	if got != want {
		t.Fatalf("layout mismatch\n--- got\n%s--- want\n%s", got, want)
	}
}

// The tab is a shell stacked with lazygit: no editor and no overview, and
// manual is the same tab as code.
func TestBuildLayoutCodeAndManualAreTheShellStack(t *testing.T) {
	code, err := BuildLayout(baseSpec(LayoutCode))
	if err != nil {
		t.Fatal(err)
	}
	manual, err := BuildLayout(baseSpec(LayoutManual))
	if err != nil || manual != code {
		t.Fatalf("manual must lay out as code: %v\n%s", err, manual)
	}
	for _, l := range []string{LayoutCode, LayoutAgent, LayoutReview} {
		got, _ := BuildLayout(baseSpec(l))
		if strings.Contains(got, "overview") || strings.Contains(got, `"--watch"`) || strings.Contains(got, `name="editor"`) {
			t.Errorf("%s: no overview or editor pane in the tab:\n%s", l, got)
		}
	}
	// Without lazygit the shell is the whole stack.
	s := baseSpec(LayoutCode)
	s.Tools.Lazygit = ""
	got, _ := BuildLayout(s)
	if !strings.Contains(got, "pane size=\"70%\" stacked=true {\n            pane name=\"shell\" expanded=true focus=true\n        }") {
		t.Fatalf("shell alone:\n%s", got)
	}
}

// The shell pane runs the user's shell itself, not whatever zellij's
// environment would start.
func TestBuildLayoutShellPaneRunsTheUserShell(t *testing.T) {
	for _, l := range []string{LayoutCode, LayoutAgent} {
		s := baseSpec(l)
		s.Tools.Shell = "/usr/bin/zsh"
		got, err := BuildLayout(s)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, `name="shell"`) || !strings.Contains(got, `command="/usr/bin/zsh"`) {
			t.Errorf("%s: shell pane:\n%s", l, got)
		}
	}
}

func TestUserShell(t *testing.T) {
	passwd := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(passwd, []byte("root:x:0:0:root:/root:/bin/sh\ndev:x:1000:100::/home/dev:/run/current-system/sw/bin/zsh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	devBash := "/nix/store/abc-bash-interactive-5.2/bin/bash"
	for _, tc := range []struct {
		env  string
		uid  int
		want string
	}{
		{"/bin/fish", 1000, "/bin/fish"},                                           // $SHELL wins
		{"/nix/store/abc-zsh-5.9/bin/zsh", 1000, "/nix/store/abc-zsh-5.9/bin/zsh"}, // a Nix-installed shell of the user's own
		{devBash, 1000, "/run/current-system/sw/bin/zsh"},                          // nix develop's bash: the login shell
		{"", 1000, "/run/current-system/sw/bin/zsh"},
		{devBash, 4242, devBash}, // no passwd entry: keep what there is
		{"", 4242, ""},
	} {
		if got := userShell(tc.env, passwd, tc.uid); got != tc.want {
			t.Errorf("userShell(%q, uid %d) = %q, want %q", tc.env, tc.uid, got, tc.want)
		}
	}
}

func TestBuildLayoutAgentPaneStartsOmpWithTheSkillOverlay(t *testing.T) {
	s := baseSpec(LayoutAgent)
	s.Agent = []string{"--config", "/cache/ws/agent/x/omp.yml", `Read skill://ade-implement and implement "it"`}
	got, err := BuildLayout(s)
	if err != nil {
		t.Fatal(err)
	}
	want := `pane name="agent" size="60%" focus=true command="env" {
                args "ADE_AGENT_RUN=1" "/bin/omp" "--config" "/cache/ws/agent/x/omp.yml" "Read skill://ade-implement and implement \"it\""
            }`
	if !strings.Contains(got, want) {
		t.Fatalf("agent pane:\n%s", got)
	}
	// Without omp installed the pane is a plain shell, with no dangling args.
	s.Tools.Omp = ""
	got, _ = BuildLayout(s)
	if strings.Contains(got, "omp.yml") || strings.Contains(got, "ADE_AGENT_RUN") || !strings.Contains(got, `pane name="agent"`) {
		t.Fatalf("fallback:\n%s", got)
	}
}

func TestBuildLayoutAgentPaneUnsetsHiddenVariables(t *testing.T) {
	s := baseSpec(LayoutAgent)
	s.Agent = []string{"--config", "/cache/omp.yml"}
	s.AgentUnset = []string{"GITLAB_TOKEN", "GH_TOKEN"}
	got, err := BuildLayout(s)
	if err != nil {
		t.Fatal(err)
	}
	want := `pane name="agent" size="60%" focus=true command="env" {
                args "-u" "GITLAB_TOKEN" "-u" "GH_TOKEN" "ADE_AGENT_RUN=1" "/bin/omp" "--config" "/cache/omp.yml"
            }`
	if !strings.Contains(got, want) {
		t.Fatalf("agent pane:\n%s", got)
	}
	// Only the agent pane loses the variables.
	if strings.Count(got, `"-u"`) != 2 || strings.Count(got, `command="env"`) != 1 {
		t.Fatalf("other panes changed:\n%s", got)
	}
	// Without omp the pane is a plain shell: nothing to wrap.
	s.Tools.Omp = ""
	if got, _ = BuildLayout(s); strings.Contains(got, `"env"`) {
		t.Fatalf("fallback:\n%s", got)
	}
	s = baseSpec(LayoutAgent)
	if got, _ = BuildLayout(s); strings.Contains(got, `"-u"`) || !strings.Contains(got, `args "ADE_AGENT_RUN=1" "/bin/omp"`) {
		t.Fatalf("empty AgentUnset must only carry the run marker:\n%s", got)
	}
}

func TestBuildLayoutVariants(t *testing.T) {
	tests := []struct {
		layout string
		want   []string
		not    []string
	}{
		{LayoutAgent, []string{`name="agent" size="60%" focus=true`, `"ADE_AGENT_RUN=1" "/bin/omp"`, `pane name="lazygit" expanded=true`, `name="api.dev"`, `name="web.dev"`}, []string{`name="editor"`}},
		{LayoutReview, []string{`name="diff"`, `command="sh"`, `args "-c" "git diff 'origin/main'...HEAD | '/bin/delta'"`}, []string{`name="editor"`, `"/bin/omp"`}},
		{LayoutManual, []string{`name="shell" expanded=true focus=true`, `name="lazygit"`, `name="web.dev"`}, []string{`"/bin/omp"`, `ADE_AGENT_RUN`, `name="editor"`}},
	}
	for _, tc := range tests {
		got, err := BuildLayout(baseSpec(tc.layout))
		if err != nil {
			t.Fatalf("%s: %v", tc.layout, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s layout lacks %s\n%s", tc.layout, w, got)
			}
		}
		for _, n := range tc.not {
			if strings.Contains(got, n) {
				t.Errorf("%s layout must not contain %s\n%s", tc.layout, n, got)
			}
		}
		// Services and tasks form one strip in every layout, so all of them are visible at once.
		if tc.layout != LayoutReview && !strings.Contains(got, `size="30%" split_direction="vertical"`) {
			t.Errorf("%s: no services strip\n%s", tc.layout, got)
		}
	}
}

func TestBuildLayoutWithoutToolsFallsBackToShells(t *testing.T) {
	s := baseSpec(LayoutCode)
	s.Tools = Tools{} // nothing installed
	got, err := BuildLayout(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "command=") {
		t.Fatalf("no tool, no command pane:\n%s", got)
	}
	if !strings.Contains(got, `pane name="shell" expanded=true focus=true`) || strings.Contains(got, "lazygit") {
		t.Fatalf("expected a lone shell:\n%s", got)
	}
}

func TestBuildLayoutEscapesPaths(t *testing.T) {
	s := baseSpec(LayoutCode)
	s.Dir = `/p/we "ird"/a\b`
	s.Tab = `web/"x"`
	got, err := BuildLayout(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `tab name="web/\"x\"" cwd="/p/we \"ird\"/a\\b"`) {
		t.Fatalf("unescaped:\n%s", got)
	}
}

func TestBuildLayoutWithoutTasksHasNoStrip(t *testing.T) {
	s := baseSpec(LayoutCode)
	s.Up, s.Services, s.Tasks = Pane{}, nil, nil
	got, _ := BuildLayout(s)
	if strings.Contains(got, `30%`) || strings.Contains(got, `70%`) || strings.Contains(got, "start_suspended") {
		t.Fatalf("a workspace without tasks is just the top row:\n%s", got)
	}
}

func TestBuildLayoutServicePanesWaitForEnter(t *testing.T) {
	got, err := BuildLayout(baseSpec(LayoutCode))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, `"/opt/ade/bin/ade"`) && !strings.Contains(line, "start_suspended=true") {
			if strings.Contains(line, `name="overview"`) {
				continue // the overview is not a service
			}
			t.Errorf("a service pane would start on open: %s", line)
		}
	}
	if strings.Count(got, "start_suspended=true") != 3 {
		t.Fatalf("want the services pane and both service panes suspended:\n%s", got)
	}
}

func TestBuildLayoutEveryTabHasTheStandardChrome(t *testing.T) {
	for _, l := range []string{LayoutCode, LayoutAgent, LayoutReview, LayoutManual} {
		got, err := BuildLayout(baseSpec(l))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, `plugin location="zellij:tab-bar"`) || !strings.Contains(got, `plugin location="zellij:status-bar"`) {
			t.Errorf("%s: the new tab has no tab bar or status bar:\n%s", l, got)
		}
	}
}

func TestBuildLayoutRejectsBadInput(t *testing.T) {
	if _, err := BuildLayout(LayoutSpec{Layout: "grid", Tab: "t", Dir: "/d"}); err == nil || !strings.Contains(err.Error(), `unknown layout "grid"`) {
		t.Fatalf("got %v", err)
	}
	if _, err := BuildLayout(LayoutSpec{Layout: LayoutCode}); err == nil {
		t.Fatal("empty tab/dir accepted")
	}
}

type fakeZellij struct {
	sessions string
	tabs     string
	panes    string // list-panes --json
	tabList  string // list-tabs --json
	failOn   string // an action whose arguments contain this fails
	calls    [][]string
}

func (f *fakeZellij) run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if f.failOn != "" && strings.Contains(strings.Join(args, " "), f.failOn) {
		return "", errors.New("zellij " + f.failOn + ": failed")
	}
	switch {
	case len(args) > 3 && args[3] == "list-panes":
		return f.panes, nil
	case args[0] == "list-sessions":
		return f.sessions, nil
	case len(args) > 3 && args[3] == "query-tab-names":
		return f.tabs, nil
	case len(args) > 3 && args[3] == "list-tabs":
		return f.tabList, nil
	}
	return "", nil
}

func TestOpenTabCreatesThenFocuses(t *testing.T) {
	z := &fakeZellij{sessions: "ade [Created 5s ago]\nade-old [Created 2d ago] (EXITED - attach to resurrect)\n", tabs: "home\n"}
	created, _, err := openTab(context.Background(), z.run, "ade", "web/418", "/tmp/x.kdl", tabLayout{})
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	last := z.calls[len(z.calls)-1]
	if strings.Join(last, " ") != "--session ade action new-tab --layout /tmp/x.kdl --name web/418" {
		t.Fatalf("call: %v", last)
	}

	z = &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "home\nweb/418\n"}
	created, _, err = openTab(context.Background(), z.run, "ade", "web/418", "/tmp/x.kdl", tabLayout{})
	if err != nil || created {
		t.Fatalf("existing tab: created=%v err=%v", created, err)
	}
	if last := strings.Join(z.calls[len(z.calls)-1], " "); last != "--session ade action go-to-tab-name web/418" {
		t.Fatalf("must focus, not create: %s", last)
	}
}

func TestOpenTabNeverTouchesOtherSessions(t *testing.T) {
	// Only a resurrectable "ade" and a live neighbour exist: neither is usable.
	z := &fakeZellij{sessions: "ade [Created 5d ago] (EXITED - attach to resurrect)\nadept [Created 1s ago]\n"}
	_, _, err := openTab(context.Background(), z.run, "ade", "t", "/tmp/x.kdl", tabLayout{})
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("got %v", err)
	}
	for _, c := range z.calls {
		if c[0] != "list-sessions" {
			t.Fatalf("acted on a session that is not running: %v", c)
		}
	}
}

func TestBuildTasksMirrorsGraph(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	plan, err := f.mgr.Plan(context.Background(), f.loc("web/main"), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tasks := BuildTasks(plan, "/opt/ade", []string{"--with", "api=feat"})
	by := map[string]VSTask{}
	for _, tk := range tasks {
		by[tk.Label] = tk
	}
	web, api, build, up := by["ade: web.dev"], by["ade: api.dev"], by["ade: api.build"], by["ade: up"]
	if strings.Join(web.DependsOn, ",") != "ade: api.dev" || strings.Join(api.DependsOn, ",") != "ade: api.build" || len(build.DependsOn) != 0 {
		t.Fatalf("dependsOn: web=%v api=%v build=%v", web.DependsOn, api.DependsOn, build.DependsOn)
	}
	if strings.Join(up.DependsOn, ",") != "ade: web.dev" {
		t.Fatalf("compound task: %v", up.DependsOn)
	}
	if !web.IsBackground || !api.IsBackground || build.IsBackground {
		t.Fatal("services are background tasks, oneshots are not")
	}
	// log readiness: the node's own regexp ends the task; http: the marker.
	wm := web.ProblemMatcher.(vsMatcher)
	if wm.Background.EndsPattern != `web ready api=\S+` {
		t.Fatalf("web endsPattern %q", wm.Background.EndsPattern)
	}
	am := api.ProblemMatcher.(vsMatcher)
	if am.Background.EndsPattern != `^ade: ready api\.dev` || am.Background.BeginsPattern != `^ade: starting api\.dev` {
		t.Fatalf("api matcher %+v", am.Background)
	}
	if got := strings.Join(web.Args, " "); got != "task run web.dev --worktree "+plan.Loc.Tree+" --with api=feat" {
		t.Fatalf("args: %s", got)
	}
	if !strings.Contains(strings.Join(build.Args, " "), "--no-follow") {
		t.Fatal("a oneshot task must not follow")
	}
}

func TestMergeTasksKeepsUserTasksAndReplacesOurs(t *testing.T) {
	existing := `{"version":"2.0.0","inputs":[{"id":"x"}],"tasks":[
		{"label":"lint","type":"shell","command":"make lint"},
		{"label":"ade: api.dev","type":"process","command":"stale"}]}`
	out, err := MergeTasks([]byte(existing), []VSTask{{Label: "ade: api.dev", Type: "process", Command: "fresh", ProblemMatcher: []any{}}})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Version string
		Inputs  []map[string]any
		Tasks   []VSTask
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if got.Version != "2.0.0" || len(got.Inputs) != 1 {
		t.Fatalf("other settings lost: %s", out)
	}
	if len(got.Tasks) != 2 || got.Tasks[0].Label != "lint" || got.Tasks[1].Command != "fresh" {
		t.Fatalf("tasks: %+v", got.Tasks)
	}
}

func TestMergeTasksRefusesCommentedFile(t *testing.T) {
	_, err := MergeTasks([]byte("// my tasks\n{\"tasks\": []}"), nil)
	if err == nil || !strings.Contains(err.Error(), "not plain JSON") {
		t.Fatalf("got %v", err)
	}
}

func TestMergeTasksFromEmpty(t *testing.T) {
	out, err := MergeTasks(nil, []VSTask{{Label: "ade: x", Type: "process", ProblemMatcher: []any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"version": "2.0.0"`) {
		t.Fatalf("version missing: %s", out)
	}
}

func TestSlugIsASingleSafePathElement(t *testing.T) {
	for _, id := range []string{"web.dev@/p/web/main", "/p/web/main#web/main", `a "b"/../c@x`, strings.Repeat("n", 200) + "@t"} {
		s := slugOf(id)
		if strings.ContainsAny(s, `/\ "`) || s == "" || len(s) > 60 {
			t.Errorf("slugOf(%q) = %q", id, s)
		}
	}
	if slugOf("web.dev@/a") == slugOf("web.dev@/b") {
		t.Error("different trees must not share a slug")
	}
}

func TestOpenTabCreatesTheHomeTabFirstWhenItIsMissing(t *testing.T) {
	home := tabLayout{name: "home", file: "/tmp/home.kdl"}
	services := tabLayout{name: "web/418 · services", file: "/tmp/s.kdl"}
	z := &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "api/9\n"}
	if created, errs, err := openTab(context.Background(), z.run, "ade", "web/418", "/tmp/x.kdl", home, services); err != nil || errs != nil || !created {
		t.Fatalf("created=%v errs=%v err=%v", created, errs, err)
	}
	want := "new-tab --layout /tmp/home.kdl --name home|new-tab --layout /tmp/s.kdl --name web/418 · services|new-tab --layout /tmp/x.kdl --name web/418"
	if got := strings.Join(actions(z), "|"); got != want {
		t.Fatalf("actions %q, want %q: the dashboard first, the workspace tab last so it ends focused", got, want)
	}
	z = &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "home\n"}
	if _, _, err := openTab(context.Background(), z.run, "ade", "web/418", "/tmp/x.kdl", home); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(actions(z), "|"); got != "new-tab --layout /tmp/x.kdl --name web/418" {
		t.Fatalf("a running dashboard is left alone: %q", got)
	}
}

func TestRecordTabKeepsEveryTabOfATreeOnce(t *testing.T) {
	dir := t.TempDir()
	for _, tab := range []string{"web/418-paginate", "web/feature/418", "web/418-paginate"} {
		if err := recordTab(dir, "/p/web/418", "ade", tab); err != nil {
			t.Fatal(err)
		}
	}
	got := readTabs(dir, "/p/web/418")
	if len(got) != 2 || got[0].Tab != "web/418-paginate" || got[1].Tab != "web/feature/418" || got[0].Session != "ade" {
		t.Fatalf("records: %+v", got)
	}
	if readTabs(dir, "/p/web/other") != nil {
		t.Fatal("another tree has no tabs")
	}
}

// The caller's own tab closes last: closing it ends the caller.
func TestFindTabsPutsTheCallersTabLast(t *testing.T) {
	z := &fakeZellij{
		sessions: "ade [Created 5s ago]\ngone [Created 2d ago] (EXITED - attach to resurrect)\n",
		tabList:  `[{"name":"home","tab_id":0},{"name":"web/418","tab_id":3},{"name":"web/418 · services","tab_id":2},{"name":"api/9","tab_id":5}]`,
		panes:    `[{"id":7,"is_plugin":true,"tab_id":2},{"id":7,"is_plugin":false,"tab_id":3}]`,
	}
	recs := []tabRecord{{"ade", "web/418"}, {"ade", "web/main"}, {"gone", "web/418"}}
	got, warn := findTabs(context.Background(), z.run, recs, pane{session: "ade", id: "7"})
	if len(warn) != 0 || len(got) != 1 {
		t.Fatalf("got %+v warn %v: only the live session", got, warn)
	}
	if st := got[0]; st.session != "ade" || fmt.Sprint(st.ids) != "[2 3]" || strings.Join(st.names, "|") != "web/418 · services|web/418" {
		t.Fatalf("tabs %+v: the services tab, then the caller's own; web/main was closed by hand", st)
	}
	for _, c := range z.calls {
		if c[0] == "--session" && c[1] == "gone" {
			t.Fatalf("acted on a session that is not running: %v", c)
		}
	}
}

func TestCloseTabsShowsTheHomeTabFirst(t *testing.T) {
	targets := []sessionTabs{{session: "ade", ids: []int{2, 3}, names: []string{"web/418 · services", "web/418"}}}
	z := &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "home\nweb/418\n"}
	if err := closeTabs(context.Background(), z.run, targets, "/tmp/home.kdl"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(actions(z), "|"); got != "go-to-tab-name home|close-tab-by-id 2|close-tab-by-id 3" {
		t.Fatalf("actions %q", got)
	}
	// No dashboard: it is created, so the session keeps a tab.
	z = &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "web/418\n"}
	if err := closeTabs(context.Background(), z.run, targets, "/tmp/home.kdl"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(actions(z), "|"); got != "new-tab --layout /tmp/home.kdl --name home|close-tab-by-id 2|close-tab-by-id 3" {
		t.Fatalf("actions %q", got)
	}
	// The home tab cannot be shown: nothing closes.
	z = &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "web/418\n", failOn: "new-tab"}
	if err := closeTabs(context.Background(), z.run, targets, "/tmp/home.kdl"); err == nil || slices.Contains(actions(z), "close-tab-by-id 3") {
		t.Fatalf("err %v, actions %q", err, actions(z))
	}
}
