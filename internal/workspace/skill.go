package workspace

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/itemdoc"
)

// SkillName is the omp skill ADE generates for the agent layout's main pane.
const SkillName = "ade-implement"

// Limits keep the skill a briefing, not a dump: omp reads the whole file.
const (
	maxDescription = 8000
	maxAgentsMD    = 8000
	maxNotes       = 4000
)

// SkillInput is what the implement skill tells the agent about the ticket.
type SkillInput struct {
	Item     ItemRef
	Manifest *config.Manifest
	Repo     string // the repo of the worktree the agent works in
	Branch   string
	Worktree string
	// OtherTrees are the item's worktrees in the other repos (repo → path).
	OtherTrees map[string]string
	// Notes are the user's own notes from the item record.
	Notes string
	// ProjectGuidance is the project's AGENTS.md (the manifest's project.agents), "" when it has none.
	ProjectGuidance string
	// AgentsMD is the repo's AGENTS.md (or CLAUDE.md) text and AgentsPath its file name.
	AgentsMD   string
	AgentsPath string
	// CommitPrefix is the expanded commit prefix ("" when the project has none).
	CommitPrefix string
}

// RenderSkill renders the implement skill's SKILL.md: the ticket, where the
// work lives, the repo's conventions and the commit rule, then what to do and
// where to stop.
func RenderSkill(in SkillInput) string {
	it := in.Item.Item
	parent := it.Issue
	child, mr := in.workChild(), in.workMR()
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	line("---")
	line("name: %s", SkillName)
	line("description: %s", yamlString(fmt.Sprintf("Implement %s %s in this worktree (ADE ticket briefing); commit locally, then stop for review.", parent.Ref, parent.Title)))
	line("---")
	line("")
	line("# Implement %s: %s", parent.Ref, parent.Title)
	line("")
	line("You were started by ADE inside the worktree `%s` (repo `%s`, branch `%s`). This file is the", in.Worktree, in.Repo, in.Branch)
	line("briefing for the ticket; ADE regenerates it every time the workspace opens, so it is current.")
	line("")

	line("## Where the work lives")
	line("")
	line("- Tracker issue: %s%s", parent.Ref, link(parent.WebURL))
	if child != nil {
		line("- Child issue in this repo: %s%s", child.Ref, link(child.WebURL))
	}
	if mr != nil {
		state := mr.State
		if mr.Draft {
			state = "draft"
		}
		line("- Merge request: %s (%s) `%s` into `%s`%s", mr.Ref, state, mr.SourceBranch, mr.TargetBranch, link(mr.WebURL))
	}
	line("- Worktree: `%s`", in.Worktree)
	for _, repo := range slices.Sorted(maps.Keys(in.OtherTrees)) {
		line("- Worktree of `%s` for this item: `%s`", repo, in.OtherTrees[repo])
	}
	line("")

	line("## The ticket")
	line("")
	line("**%s** (%s)", parent.Title, parent.Ref)
	var facts []string
	if it.Type != "" {
		facts = append(facts, "type "+it.Type)
	}
	if it.Weight != nil {
		facts = append(facts, fmt.Sprintf("weight %d", *it.Weight))
	}
	if it.Sprint != nil {
		facts = append(facts, "sprint "+it.Sprint.Title)
	}
	if len(parent.Labels) > 0 {
		facts = append(facts, "labels "+strings.Join(parent.Labels, ", "))
	}
	if len(facts) > 0 {
		line("")
		line("%s", strings.Join(facts, " · "))
	}
	doc := itemdoc.Parse(parent.Description)
	if doc.Body != "" {
		line("")
		line("%s", clip(doc.Body, maxDescription))
	}
	if doc.Acceptance != "" {
		line("")
		line("### Acceptance criteria")
		line("")
		line("%s", clip(doc.Acceptance, maxDescription))
	} else if len(doc.Tasks) > 0 {
		line("")
		line("### Task checklist (the ticket's own)")
		line("")
		for _, t := range doc.Tasks {
			line("- [%s] %s", map[bool]string{true: "x", false: " "}[t.Done], t.Text)
		}
	}
	if child != nil && strings.TrimSpace(child.Description) != "" && child.Description != parent.Description {
		line("")
		line("### Child issue %s", child.Ref)
		line("")
		line("%s", clip(strings.TrimSpace(child.Description), maxDescription))
	}
	line("")

	if n := strings.TrimSpace(in.Notes); n != "" {
		line("## The developer's notes")
		line("")
		line("Written by the person you work for; they outrank the guesses you would otherwise make.")
		line("")
		line("%s", clip(n, maxNotes))
		line("")
	}

	line("## Conventions")
	line("")
	if in.Manifest != nil {
		c := in.Manifest.Conventions
		if c.Branch != "" {
			line("- Branch naming: `%s` (this branch already exists; stay on it).", c.Branch)
		}
		if c.MR.Closes != "" || c.MR.Mention != "" {
			line("- The merge request closes the child issue and mentions the parent; ADE manages them, do not edit the MR or the issues.")
		}
	}
	if in.ProjectGuidance != "" {
		line("- The project's guidance (it applies to every repo of the project) comes first, then the repository's own rules.")
	}
	if in.AgentsMD != "" {
		line("- The repository's own rules (`%s`) follow; they apply fully.", in.AgentsPath)
	} else {
		line("- The repository has no AGENTS.md: follow what the existing code does.")
	}
	line("")
	if in.ProjectGuidance != "" {
		line("<project-guidance file=%q>", "AGENTS.md")
		line("%s", clip(strings.TrimSpace(in.ProjectGuidance), maxAgentsMD))
		line("</project-guidance>")
		line("")
	}
	if in.AgentsMD != "" {
		line("<repository-rules file=%q>", in.AgentsPath)
		line("%s", clip(strings.TrimSpace(in.AgentsMD), maxAgentsMD))
		line("</repository-rules>")
		line("")
	}

	line("## Commits")
	line("")
	if in.CommitPrefix != "" {
		line("Only the first commit of the merge request starts with `%s` and a space, e.g. `%s Add the rate limit`;", in.CommitPrefix, in.CommitPrefix)
		line("later commits are plain, imperative messages. The worktree's `git config ade.commitPrefix` holds it and a")
		line("commit-msg hook may add it for you; never write it twice.")
	} else {
		line("The project sets no commit prefix; write plain, imperative messages like the existing history.")
	}
	line("Make small, logical commits. Never amend or rewrite commits that are already on the remote.")
	line("")

	line("## What to do")
	line("")
	line("1. Read the ticket above and the repository rules. If the ticket is ambiguous, ask the developer before building.")
	line("2. Plan briefly, then implement in this worktree only (`%s`).", in.Worktree)
	line("3. Run the project's tests and checks that cover what you changed; fix what you broke.")
	line("4. Commit locally following the commit rule.")
	line("5. **Stop for review.** Summarise what changed, how you verified it, and anything open. Do not push, do not")
	line("   open, edit or merge merge requests, do not change issues or labels, do not start another ticket.")
	line("   The developer reviews the diff in the lazygit pane and pushes it themselves.")
	return b.String()
}

// workChild is the child issue in this repo, nil when the item has none there.
func (in SkillInput) workChild() *forge.Issue {
	if in.Manifest == nil {
		return nil
	}
	path := in.Manifest.Repos[in.Repo].Path
	for i := range in.Item.Item.Children {
		if strings.EqualFold(in.Item.Item.Children[i].Ref.Project, path) {
			return &in.Item.Item.Children[i]
		}
	}
	return nil
}

// workMR is the item's MR whose source branch is the worktree's.
func (in SkillInput) workMR() *forge.MR {
	for i := range in.Item.Item.MRs {
		if in.Item.Item.MRs[i].SourceBranch == in.Branch && in.Branch != "" {
			return &in.Item.Item.MRs[i]
		}
	}
	return nil
}

func link(url string) string {
	if url == "" {
		return ""
	}
	return " <" + url + ">"
}

// clip cuts s at max bytes on a line boundary and says so.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndex(cut, "\n"); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, "\n") + "\n\n[… cut: the ticket is longer; read the rest on the forge]"
}

// yamlString quotes s as a YAML scalar (JSON strings are valid YAML).
func yamlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// agentsDoc reads the repo's own agent rules from a worktree: AGENTS.md, else CLAUDE.md.
func agentsDoc(tree string) (text, name string) {
	for _, n := range []string{"AGENTS.md", "CLAUDE.md"} {
		if b, err := os.ReadFile(filepath.Join(tree, n)); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			return string(b), n
		}
	}
	return "", ""
}

// WriteAgentFiles writes the skill and the omp config overlay that loads it
// under dir, outside every git tree, and returns the overlay's path. The
// overlay only adds the skill directory for one omp run (`omp --config`); the
// user's own omp configuration is never touched.
func WriteAgentFiles(dir string, in SkillInput) (overlay string, err error) {
	skillDir := filepath.Join(dir, "skills", SkillName)
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		return "", err
	}
	if err := writeFileAtomic(filepath.Join(skillDir, "SKILL.md"), []byte(RenderSkill(in))); err != nil {
		return "", err
	}
	overlay = filepath.Join(dir, "omp.yml")
	cfg := "skills:\n  customDirectories:\n    - " + yamlString(filepath.Join(dir, "skills")) + "\n"
	return overlay, writeFileAtomic(overlay, []byte(cfg))
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AgentArgs are the arguments of the interactive omp session: the overlay that
// loads the skill, and the opening message that starts it.
func AgentArgs(overlay string, it ItemRef) []string {
	return []string{
		"--config", overlay,
		fmt.Sprintf("Read skill://%s and implement %s in this worktree as it says. Commit locally and stop for my review; do not push.", SkillName, it.Item.Ref),
	}
}
