package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var fullTools = Tools{Ade: "/opt/ade/bin/ade", Editor: "nvim -u NONE", Lazygit: "/bin/lazygit", Delta: "/bin/delta", Omp: "/bin/omp"}

func baseSpec(layout string) LayoutSpec {
	return LayoutSpec{
		Layout: layout, Tab: "web/418-paginate", Dir: "/projects/acme/web/418-paginate", BaseRef: "origin/main", Tools: fullTools,
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
    tab name="web/418-paginate" cwd="/projects/acme/web/418-paginate" focus=true {
        pane size="70%" split_direction="vertical" {
            pane name="editor" size="60%" focus=true command="nvim" {
                args "-u" "NONE"
            }
            pane size="40%" stacked=true {
                pane name="lazygit" expanded=true command="/bin/lazygit"
                pane name="shell"
            }
        }
        pane size="30%" split_direction="vertical" {
            pane name="services" stacked=true {
                pane name="api.dev" expanded=true command="/opt/ade/bin/ade" {
                    args "task" "run" "api.dev" "--worktree" "/projects/acme/web/418-paginate"
                }
            }
            pane name="web.dev" command="/opt/ade/bin/ade" {
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

func TestBuildLayoutVariants(t *testing.T) {
	tests := []struct {
		layout string
		want   []string
		not    []string
	}{
		{LayoutAgent, []string{`name="agent"`, `command="/bin/omp"`, `name="lazygit"`, `name="api.dev"`, `name="web.dev"`}, []string{`name="editor"`, `size="30%"`}},
		{LayoutReview, []string{`name="diff"`, `command="sh"`, `args "-c" "git diff 'origin/main'...HEAD | '/bin/delta'"`}, []string{`name="editor"`, `command="/bin/omp"`}},
		{LayoutManual, []string{`name="shell" size="60%"`, `name="lazygit"`, `name="web.dev"`}, []string{`command="/bin/omp"`, `name="editor"`}},
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
	if !strings.Contains(got, `pane name="editor"`) || !strings.Contains(got, `pane name="shell" expanded=true`) {
		t.Fatalf("expected shell fallbacks:\n%s", got)
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
	s.Services, s.Tasks = nil, nil
	got, _ := BuildLayout(s)
	if strings.Contains(got, `30%`) || strings.Contains(got, `70%`) {
		t.Fatalf("a workspace without tasks is just the top row:\n%s", got)
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
	calls    [][]string
}

func (f *fakeZellij) run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	switch {
	case args[0] == "list-sessions":
		return f.sessions, nil
	case len(args) > 3 && args[3] == "query-tab-names":
		return f.tabs, nil
	}
	return "", nil
}

func TestOpenTabCreatesThenFocuses(t *testing.T) {
	z := &fakeZellij{sessions: "ade [Created 5s ago]\nade-old [Created 2d ago] (EXITED - attach to resurrect)\n", tabs: "home\n"}
	created, err := openTab(context.Background(), z.run, "ade", "web/418", "/tmp/x.kdl")
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	last := z.calls[len(z.calls)-1]
	if strings.Join(last, " ") != "--session ade action new-tab --layout /tmp/x.kdl --name web/418" {
		t.Fatalf("call: %v", last)
	}

	z = &fakeZellij{sessions: "ade [Created 5s ago]\n", tabs: "home\nweb/418\n"}
	created, err = openTab(context.Background(), z.run, "ade", "web/418", "/tmp/x.kdl")
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
	_, err := openTab(context.Background(), z.run, "ade", "t", "/tmp/x.kdl")
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
