package bootstrap

import (
	"maps"
	"path"
	"reflect"
	"slices"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
)

func TestSplitScoped(t *testing.T) {
	for _, c := range []struct {
		label, scope, sep, value string
		ok                       bool
	}{
		{"type::bug", "type", "::", "bug", true},
		{"workflow::in progress", "workflow", "::", "in progress", true},
		{"kind/feature", "kind", "/", "feature", true},
		{"area:web", "area", ":", "web", true},
		{"scope::a/b", "scope", "::", "a/b", true}, // the first separator wins
		{"a/b::c", "a", "/", "b::c", true},
		{"good first issue", "", "", "", false},
		{"type::", "", "", "", false},
		{"::bug", "", "", "", false},
	} {
		scope, sep, value, ok := splitScoped(c.label)
		if scope != c.scope || sep != c.sep || value != c.value || ok != c.ok {
			t.Errorf("splitScoped(%q) = %q %q %q %v, want %q %q %q %v", c.label, scope, sep, value, ok, c.scope, c.sep, c.value, c.ok)
		}
	}
}

func TestDetectFacets(t *testing.T) {
	for _, c := range []struct {
		name    string
		defined []string
		used    map[string]int
		want    map[string]string // facet -> pattern ("" = not found)
	}{
		{
			name:    "gitlab scoped labels",
			defined: []string{"type::story", "type::bug", "app::api", "app::web", "workflow::in progress", "workflow::done", "weight::1", "weight::5", "bug"},
			want:    map[string]string{"type": "type::{value}", "app": "app::{value}", "workflow": "workflow::{value}", "weight": "weight::{value}"},
		},
		{
			name:    "slash style synonyms",
			defined: []string{"kind/bug", "kind/feature", "area/api", "area/ui", "status/doing", "status/done", "points/3"},
			want:    map[string]string{"type": "kind/{value}", "app": "area/{value}", "workflow": "status/{value}", "weight": "points/{value}"},
		},
		{
			name:    "single colon, as on github",
			defined: []string{"type:bug", "app:api", "workflow:in review", "weight:2"},
			want:    map[string]string{"type": "type:{value}", "app": "app:{value}", "workflow": "workflow:{value}", "weight": "weight:{value}"},
		},
		{
			name:    "the spelling used on more issues wins over the one defined more often",
			defined: []string{"type::a", "type::b", "type::c", "kind/x"},
			used:    map[string]int{"kind/x": 9, "type::a": 1},
			want:    map[string]string{"type": "kind/{value}"},
		},
		{
			name:    "a tie falls to the facet with more values, then the canonical scope",
			defined: []string{"kind/a", "type/a"},
			want:    map[string]string{"type": "type/{value}"},
		},
		{
			name:    "weights must be numbers",
			defined: []string{"weight::small", "weight::large", "estimate::3"},
			want:    map[string]string{"weight": "estimate::{value}"},
		},
		{
			name:    "no scoped labels",
			defined: []string{"bug", "help wanted", "good first issue"},
			want:    map[string]string{},
		},
		{
			name:    "labels only seen on issues count",
			defined: nil,
			used:    map[string]int{"app::billing": 4},
			want:    map[string]string{"app": "app::{value}"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := DetectFacets(c.defined, c.used)
			got := map[string]string{}
			for name, fc := range map[string]*Facet{"type": f.Type, "app": f.App, "workflow": f.Workflow, "weight": f.Weight} {
				if fc != nil {
					got[name] = fc.Pattern
				}
			}
			if !maps.Equal(got, c.want) {
				t.Errorf("patterns = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDetectFacetsKeepsSpellingAndOrdersValuesByUse(t *testing.T) {
	f := DetectFacets([]string{"App::api", "App::web", "App::worker"}, map[string]int{"App::web": 5, "App::api": 2})
	if f.App == nil || f.App.Pattern != "App::{value}" {
		t.Fatalf("app = %+v: want the team's capitalisation kept", f.App)
	}
	if want := []string{"web", "api", "worker"}; !slices.Equal(f.App.Values, want) {
		t.Errorf("values = %v, want %v (busiest first, then alphabetical)", f.App.Values, want)
	}
}

func TestMapWorkflow(t *testing.T) {
	for _, c := range []struct {
		name    string
		values  []string
		used    map[string]int
		stage   map[string]string
		done    string
		blocked string
	}{
		{
			name:   "the sandbox's set",
			values: []string{"in progress", "in review", "blocked", "done"},
			stage:  map[string]string{"started": "in progress", "draft": "in progress", "review": "in review", "merged": "done"},
			done:   "done", blocked: "blocked",
		},
		{
			name:   "other spellings",
			values: []string{"Backlog", "Doing", "Code-Review", "Shipped", "On Hold"},
			stage:  map[string]string{"started": "Doing", "draft": "Doing", "review": "Code-Review", "merged": "Shipped"},
			done:   "Shipped", blocked: "On Hold",
		},
		{
			name:   "a value matching a taken stage goes to the busier one",
			values: []string{"review", "in review"},
			used:   map[string]int{"in review": 7, "review": 2},
			stage:  map[string]string{"review": "in review"},
		},
		{
			name:    "blocked in review is blocked, not review",
			values:  []string{"blocked in review", "in review"},
			stage:   map[string]string{"review": "in review"},
			blocked: "blocked in review",
		},
		{
			name:   "nothing recognisable maps nothing",
			values: []string{"todo", "ready", "later"},
			stage:  map[string]string{},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := MapWorkflow(c.values, c.used)
			if !maps.Equal(w.Stage, c.stage) || w.Done != c.done || w.Blocked != c.blocked {
				t.Errorf("stage=%v done=%q blocked=%q, want %v %q %q", w.Stage, w.Done, w.Blocked, c.stage, c.done, c.blocked)
			}
		})
	}
	w := MapWorkflow([]string{"todo", "in progress"}, nil)
	if !slices.Equal(w.Unmapped, []string{"todo"}) {
		t.Errorf("unmapped = %v, want [todo]", w.Unmapped)
	}
}

func TestMatchApps(t *testing.T) {
	for _, c := range []struct {
		name    string
		repos   []string
		values  []string
		want    map[string][]string
		matched map[string]bool
	}{
		{
			name: "exact", repos: []string{"api", "web"}, values: []string{"web", "api"},
			want: map[string][]string{"api": {"api"}, "web": {"web"}}, matched: map[string]bool{"api": true, "web": true},
		},
		{
			name: "exact beats contains: webhooks does not take web", repos: []string{"web", "webhooks"}, values: []string{"web"},
			want:    map[string][]string{"web": {"web"}, "webhooks": {"webhooks"}},
			matched: map[string]bool{"web": true},
		},
		{
			name: "a repo name containing a value", repos: []string{"billing-service"}, values: []string{"billing"},
			want: map[string][]string{"billing-service": {"billing"}}, matched: map[string]bool{"billing-service": true},
		},
		{
			name: "a value containing the repo name", repos: []string{"auth"}, values: []string{"authentication"},
			want: map[string][]string{"auth": {"authentication"}}, matched: map[string]bool{"auth": true},
		},
		{
			name: "no value matches: the repo name stands in, unmatched", repos: []string{"worker"}, values: []string{"api"},
			want: map[string][]string{"worker": {"worker"}}, matched: map[string]bool{},
		},
		{
			name: "no app facet at all", repos: []string{"api"}, values: nil,
			want: map[string][]string{"api": {"api"}}, matched: map[string]bool{},
		},
		{
			name: "two-letter names never match by containment", repos: []string{"ui"}, values: []string{"guide"},
			want: map[string][]string{"ui": {"ui"}}, matched: map[string]bool{},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, matched := MatchApps(c.repos, c.values)
			if !reflect.DeepEqual(got, c.want) || !maps.Equal(matched, c.matched) {
				t.Errorf("apps = %v matched = %v, want %v %v", got, matched, c.want, c.matched)
			}
		})
	}
}

func TestPickTracker(t *testing.T) {
	repo := func(name string, app int) Candidate {
		return Candidate{Repo: forge.Repo{Name: name, Path: "g/" + name}, AppIssues: app, NameHit: trackerName.MatchString(name)}
	}
	for _, c := range []struct {
		name  string
		cands []Candidate
		win   int
		ask   []int
	}{
		{"most app labels, named tracker", []Candidate{repo("api", 0), repo("tracker", 11), repo("web", 0)}, 1, nil},
		{"most app labels, unnamed", []Candidate{repo("api", 0), repo("roadmap", 9), repo("web", 2)}, 1, nil},
		{"no labels but one is named", []Candidate{repo("api", 0), repo("planning", 0), repo("web", 0)}, 1, nil},
		{"a tie broken by the name", []Candidate{repo("tracker", 5), repo("roadmap", 5)}, 0, nil},
		{"a tie nobody is named for", []Candidate{repo("a", 5), repo("b", 5)}, -1, []int{0, 1}},
		{"labels point away from the named repo", []Candidate{repo("tracker", 0), repo("roadmap", 9)}, -1, []int{0, 1}},
		{"nothing to go on", []Candidate{repo("a", 0), repo("b", 0)}, -1, nil},
		{"two named, no labels", []Candidate{repo("issues", 0), repo("planning", 0)}, -1, []int{0, 1}},
		{"names are whole words", []Candidate{repo("tracking-pixel", 0), repo("web", 0)}, -1, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			win, ask := PickTracker(c.cands)
			if win != c.win || !slices.Equal(ask, c.ask) {
				t.Errorf("= %d %v, want %d %v", win, ask, c.win, c.ask)
			}
		})
	}
}

func TestRepoKeys(t *testing.T) {
	mk := func(paths ...string) []forge.Repo {
		var out []forge.Repo
		for _, p := range paths {
			out = append(out, forge.Repo{Path: p, Name: path.Base(p)})
		}
		return out
	}
	keys, prefix := repoKeys(mk("g/ade-sandbox-tracker", "g/ade-sandbox-api", "g/ade-sandbox-web"))
	if prefix != "ade-sandbox-" || keys["g/ade-sandbox-api"] != "api" || keys["g/ade-sandbox-tracker"] != "tracker" {
		t.Errorf("shared prefix: keys=%v prefix=%q", keys, prefix)
	}
	if _, prefix := repoKeys(mk("g/tracker", "g/api")); prefix != "" {
		t.Errorf("prefix = %q, want none", prefix)
	}
	if _, prefix := repoKeys(mk("g/app-a", "g/app")); prefix != "" {
		t.Errorf("prefix = %q: a name that is only the prefix must keep its name", prefix)
	}
	keys, _ = repoKeys(mk("g/a/api", "g/b/api", "g/web.site"))
	if keys["g/a/api"] != "api" || keys["g/b/api"] != "b-api" || keys["g/web.site"] != "web-site" {
		t.Errorf("collisions and unsafe characters: %v", keys)
	}
}
