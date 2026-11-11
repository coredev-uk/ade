package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

func skillItem() ItemRef {
	w := 5
	return ItemRef{Project: "demo", Forge: "gl", Item: domain.WorkItem{
		Project: "demo",
		Ref:     forge.Ref{Project: "acme/tracker", IID: 12, Kind: forge.KindIssue},
		Issue: forge.Issue{
			Ref: forge.Ref{Project: "acme/tracker", IID: 12, Kind: forge.KindIssue}, Title: "Rate limit exports",
			WebURL:      "https://gl.example/acme/tracker/-/issues/12",
			Description: "Exports hammer the db.\n\n## Acceptance criteria\n\n- [ ] 429 after 10 requests a minute\n- [ ] Retry-After is set\n",
			Labels:      []string{"type::story", "app::api"},
		},
		Children: []forge.Issue{{
			Ref: forge.Ref{Project: "acme/api", IID: 31, Kind: forge.KindIssue}, Title: "Rate limit exports",
			Description: "Parent: acme/tracker#12\n\nImplementation notes for the api.", WebURL: "https://gl.example/acme/api/-/issues/31",
		}},
		MRs: []forge.MR{{
			Ref: forge.Ref{Project: "acme/api", IID: 7, Kind: forge.KindMR}, Draft: true, State: "opened",
			SourceBranch: "31-rate-limit-exports", TargetBranch: "main", WebURL: "https://gl.example/acme/api/-/merge_requests/7",
		}},
		Type: "story", Weight: &w,
	}}
}

func skillInput() SkillInput {
	m := &config.Manifest{Name: "demo", Repos: map[string]config.Repo{"api": {Path: "acme/api"}, "web": {Path: "acme/web"}}}
	m.Conventions.Branch = "{child_iid}-{slug}"
	return SkillInput{
		Item: skillItem(), Manifest: m, Repo: "api", Branch: "31-rate-limit-exports", Worktree: "/p/demo/api/31-rate-limit-exports",
		OtherTrees: map[string]string{"web": "/p/demo/web/31-rate-limit-exports"},
		Notes:      "Use the token bucket in pkg/limit, not a new one.",
		AgentsMD:   "# Rules\nRun `make check` before committing.\n", AgentsPath: "AGENTS.md",
		CommitPrefix: "GL-12",
	}
}

func TestRenderSkillCarriesTheTicket(t *testing.T) {
	got := RenderSkill(skillInput())
	for _, want := range []string{
		"name: ade-implement",
		"# Implement acme/tracker#12: Rate limit exports",
		"acme/tracker#12 <https://gl.example/acme/tracker/-/issues/12>",
		"Child issue in this repo: acme/api#31",
		"acme/api!7 (draft) `31-rate-limit-exports` into `main` <https://gl.example/acme/api/-/merge_requests/7>",
		"`/p/demo/web/31-rate-limit-exports`",
		"weight 5",
		"Exports hammer the db.",
		"### Acceptance criteria",
		"- [ ] 429 after 10 requests a minute",
		"Implementation notes for the api.",
		"Use the token bucket in pkg/limit",
		"<repository-rules file=\"AGENTS.md\">",
		"Run `make check` before committing.",
		"Every commit message starts with `GL-12` and a space",
		"**Stop for review.**",
		"Do not push",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill lacks %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "Parent: acme/tracker#12\n") && strings.Count(got, "Implementation notes") != 1 {
		t.Error("child description repeated")
	}
}

func TestRenderSkillWithoutPrefixNotesOrAgentsMD(t *testing.T) {
	in := skillInput()
	in.CommitPrefix, in.Notes, in.AgentsMD, in.AgentsPath = "", "  \n", "", ""
	in.Item.Item.MRs, in.Item.Item.Children = nil, nil
	got := RenderSkill(in)
	if !strings.Contains(got, "The project sets no commit prefix") || strings.Contains(got, "developer's notes") ||
		!strings.Contains(got, "no AGENTS.md") || strings.Contains(got, "Merge request:") {
		t.Fatalf("\n%s", got)
	}
}

func TestRenderSkillFrontmatterIsOneYAMLScalarPerKey(t *testing.T) {
	in := skillInput()
	in.Item.Item.Issue.Title = "Fix: the \"quoted\"\nnewline"
	got := RenderSkill(in)
	head, _, _ := strings.Cut(got, "\n---\n\n")
	lines := strings.Split(head, "\n")
	if len(lines) != 3 || lines[0] != "---" || !strings.HasPrefix(lines[2], `description: "`) {
		t.Fatalf("frontmatter must be exactly name and description:\n%s", head)
	}
}

func TestClipCutsOnALineAndSaysSo(t *testing.T) {
	long := strings.Repeat("line of text\n", 1000)
	got := clip(long, 500)
	if len(got) > 600 || !strings.Contains(got, "[… cut:") || !strings.Contains(got, "line of text\n\n[") {
		t.Fatalf("%q", got[len(got)-80:])
	}
	if clip("short", 500) != "short" {
		t.Fatal("short text must pass untouched")
	}
}

func TestWriteAgentFilesLeavesTheUsersOmpConfigAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ws", "agent", "x")
	overlay, err := WriteAgentFiles(dir, skillInput())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatal(err)
	}
	if want := "skills:\n  customDirectories:\n    - " + `"` + filepath.Join(dir, "skills") + `"` + "\n"; string(cfg) != want {
		t.Fatalf("overlay:\n%s", cfg)
	}
	md, err := os.ReadFile(filepath.Join(dir, "skills", SkillName, "SKILL.md"))
	if err != nil || !strings.Contains(string(md), "Rate limit exports") {
		t.Fatalf("skill: %v\n%s", err, md)
	}
	// Regenerated on the next open: a changed ticket shows up.
	in := skillInput()
	in.Item.Item.Issue.Title = "Rate limit everything"
	if _, err := WriteAgentFiles(dir, in); err != nil {
		t.Fatal(err)
	}
	md, _ = os.ReadFile(filepath.Join(dir, "skills", SkillName, "SKILL.md"))
	if !strings.Contains(string(md), "Rate limit everything") {
		t.Fatalf("not regenerated:\n%s", md)
	}
}

func TestAgentArgsNameTheSkillAndForbidPushing(t *testing.T) {
	args := AgentArgs("/c/omp.yml", skillItem())
	if len(args) != 3 || args[0] != "--config" || args[1] != "/c/omp.yml" {
		t.Fatalf("%v", args)
	}
	for _, w := range []string{"skill://ade-implement", "acme/tracker#12", "do not push"} {
		if !strings.Contains(args[2], w) {
			t.Errorf("prompt lacks %q: %s", w, args[2])
		}
	}
}

func TestAgentsDocPrefersAgentsMD(t *testing.T) {
	dir := t.TempDir()
	if text, _ := agentsDoc(dir); text != "" {
		t.Fatal("no file, no text")
	}
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude rules"), 0o644)
	if text, name := agentsDoc(dir); text != "claude rules" || name != "CLAUDE.md" {
		t.Fatalf("%q %q", text, name)
	}
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("agents rules"), 0o644)
	if text, name := agentsDoc(dir); text != "agents rules" || name != "AGENTS.md" {
		t.Fatalf("%q %q", text, name)
	}
}

func TestRenderLayoutAgentLayoutOfAnItemStartsOmpWithTheSkill(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main")
	a := newApp(t, f, "plain")
	tree := filepath.Join(f.root, "demo", "web", "main")
	it := skillItem()
	kdl, err := RenderLayout(context.Background(), a, "demo", "web", tree, WithLayout(LayoutAgent), WithItem(it))
	if err != nil {
		t.Fatal(err)
	}
	// No omp on the empty PATH: the pane is a shell, but the overview is there.
	if !strings.Contains(kdl, `args "item" "acme/tracker#12" "--watch"`) {
		t.Fatalf("overview:\n%s", kdl)
	}
	p, err := prepare(context.Background(), a, "demo", "web", tree, []OpenOption{WithLayout(LayoutAgent), WithItem(it)})
	if err != nil || p.agent == nil {
		t.Fatalf("agent files missing: %v", err)
	}
	if want := filepath.Join(a.Paths.CacheDir, "ws", "agent"); !strings.HasPrefix(p.agent.dir, want) {
		t.Fatalf("skill must live in the cache, outside every git tree: %s", p.agent.dir)
	}
	spec := layoutSpec(context.Background(), p)
	if len(spec.Agent) != 3 || spec.Agent[0] != "--config" || spec.Agent[1] != p.agent.overlay() {
		t.Fatalf("agent args: %v", spec.Agent)
	}
	// Other layouts write no skill.
	if p, _ = prepare(context.Background(), a, "demo", "web", tree, []OpenOption{WithLayout(LayoutCode), WithItem(it)}); p.agent != nil {
		t.Fatal("only the agent layout carries the skill")
	}
}

func TestLayoutPrecedenceExplicitThenUserThenManifestThenCode(t *testing.T) {
	f := newFixture(t, appManifest, "api/main", "web/main") // the manifest says code
	a := newApp(t, f, "plain")
	tree := filepath.Join(f.root, "demo", "web", "main")
	layout := func(opts ...OpenOption) string {
		p, err := prepare(context.Background(), a, "demo", "web", tree, opts)
		if err != nil {
			t.Fatal(err)
		}
		return p.res.Layout
	}
	if got := layout(); got != LayoutCode {
		t.Fatalf("manifest default: %s", got)
	}
	a.Config.Host.Layout = LayoutAgent
	if got := layout(); got != LayoutAgent {
		t.Fatalf("host.layout beats the manifest: %s", got)
	}
	if got := layout(WithLayout(LayoutManual)); got != LayoutManual {
		t.Fatalf("an explicit layout beats host.layout: %s", got)
	}
	if DefaultLayout("", "") != LayoutCode || DefaultLayout("", "manual") != LayoutManual || DefaultLayout("agent", "manual") != LayoutAgent {
		t.Fatal("DefaultLayout")
	}
}
