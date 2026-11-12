package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/project"
)

// Target is where an agent works: a start-work worktree and what it belongs
// to. The CLI and TUI build it from startwork's discovery.
type Target struct {
	Forge   string    `json:"forge"`
	Project string    `json:"project"`
	Tracker forge.Ref `json:"tracker"`
	// Child is the code-repo issue start work created, when the tracker
	// issue lives elsewhere.
	Child    *forge.Ref `json:"child,omitempty"`
	Repo     string     `json:"repo"`
	Worktree string     `json:"worktree"`
	Branch   string     `json:"branch"`
	MR       *forge.Ref `json:"mr,omitempty"`
	AddDirs  []string   `json:"add_dirs,omitempty"`
	// CommitPrefix is the expanded commit prefix of the item ("" when the
	// project sets none). It is not stored with the run: the Manager fills it
	// in when it builds a prompt. PrefixKnown is false when the Manager could
	// not tell, and then the prompt says nothing about a prefix.
	CommitPrefix string `json:"commit_prefix,omitempty"`
	PrefixKnown  bool   `json:"-"`
}

// Thread is an unresolved merge request discussion.
type Thread struct {
	ID       string          `json:"id"`
	Position *forge.Position `json:"position,omitempty"`
	Notes    []forge.Note    `json:"notes"`
}

// Context is what a prompt is built from: the issue (and its child), their
// comments, the MR's unresolved threads, and the repo's AGENTS.md. It is kept
// in the run directory next to the prompt.
type Context struct {
	Issue    forge.Issue  `json:"issue"`
	Child    *forge.Issue `json:"child,omitempty"`
	Comments []forge.Note `json:"comments,omitempty"`
	Threads  []Thread     `json:"threads,omitempty"`
	Agents   string       `json:"agents,omitempty"` // AGENTS.md of the worktree
	// ProjectAgents is the project's AGENTS.md (the manifest's project.agents).
	ProjectAgents string `json:"project_agents,omitempty"`
	// Notes are the user's own notes on the item (records.ReadNotes), when
	// they are included in the prompt.
	Notes string `json:"notes,omitempty"`
}

// maxAgentsBytes bounds the AGENTS.md carried into a prompt.
const maxAgentsBytes = 16 << 10

// Gather reads the parent issue, the child issue, their human comments, the
// MR's unresolved threads and the worktree's AGENTS.md.
func Gather(ctx context.Context, f forge.Forge, t Target) (Context, error) {
	var c Context
	var err error
	if c.Issue, err = f.Issue(ctx, t.Tracker); err != nil {
		return c, fmt.Errorf("%s: %w", t.Tracker, err)
	}
	refs := []forge.Ref{t.Tracker}
	if t.Child != nil && *t.Child != t.Tracker {
		child, err := f.Issue(ctx, *t.Child)
		if err != nil {
			return c, fmt.Errorf("%s: %w", t.Child, err)
		}
		c.Child = &child
		refs = append(refs, *t.Child)
	}
	for _, r := range refs {
		ds, err := f.Discussions(ctx, r)
		if err != nil {
			return c, fmt.Errorf("comments of %s: %w", r, err)
		}
		for _, d := range ds {
			for _, n := range d.Notes {
				if !n.System {
					c.Comments = append(c.Comments, n)
				}
			}
		}
	}
	if t.MR != nil {
		ds, err := f.Discussions(ctx, *t.MR)
		if err != nil {
			return c, fmt.Errorf("threads of %s: %w", t.MR, err)
		}
		for _, d := range ds {
			if d.Resolvable && !d.Resolved && len(d.Notes) > 0 {
				c.Threads = append(c.Threads, Thread{ID: d.ID, Position: d.Position, Notes: d.Notes})
			}
		}
	}
	c.Agents = readAgents(t.Worktree)
	// The worktree is <root>/<project>/<repo>/<tree>.
	c.ProjectAgents = boundAgents(project.ReadAgents(filepath.Dir(filepath.Dir(t.Worktree))))
	return c, nil
}

func readAgents(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		return ""
	}
	return boundAgents(string(b))
}

// boundAgents cuts guidance text at maxAgentsBytes.
func boundAgents(s string) string {
	if len(s) > maxAgentsBytes {
		return s[:maxAgentsBytes] + "\n[truncated]"
	}
	return s
}

func (c Context) write(dir string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, "context.json"), append(b, '\n'))
}

func readContext(dir string) (Context, error) {
	var c Context
	b, err := os.ReadFile(filepath.Join(dir, "context.json"))
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

func (c Context) issueBlock(b *strings.Builder) {
	fmt.Fprintf(b, "## Issue %s: %s\n\n%s\n", c.Issue.Ref, c.Issue.Title, strings.TrimSpace(c.Issue.Description))
	if len(c.Issue.Labels) > 0 {
		fmt.Fprintf(b, "\nLabels: %s\n", strings.Join(c.Issue.Labels, ", "))
	}
	if c.Child != nil {
		fmt.Fprintf(b, "\n## Child issue %s: %s\n\n%s\n", c.Child.Ref, c.Child.Title, strings.TrimSpace(c.Child.Description))
	}
	if c.Notes != "" {
		fmt.Fprintf(b, "\n## Your notes on this item\n\nThe person you work for keeps these private notes. Treat them as guidance about intent and constraints; do not copy them into code, commits or comments.\n\n%s\n", c.Notes)
	}
	if len(c.Comments) > 0 {
		b.WriteString("\n## Comments\n")
		for _, n := range c.Comments {
			fmt.Fprintf(b, "\n%s (%s):\n%s\n", n.Author.Username, n.CreatedAt.Format("2006-01-02"), strings.TrimSpace(n.Body))
		}
	}
	if c.ProjectAgents != "" {
		fmt.Fprintf(b, "\n## Project guidance (AGENTS.md of the project)\n\n%s\n", strings.TrimSpace(c.ProjectAgents))
	}
	if c.Agents != "" {
		fmt.Fprintf(b, "\n## Repository guidance (AGENTS.md)\n\n%s\n", strings.TrimSpace(c.Agents))
	}
}

func targetBlock(b *strings.Builder, t Target) {
	fmt.Fprintf(b, "You are working in the git worktree %s (repo %s, branch %s).\n", t.Worktree, t.Repo, t.Branch)
	if len(t.AddDirs) > 0 {
		fmt.Fprintf(b, "These directories are required repos you may read but must not change: %s.\n", strings.Join(t.AddDirs, ", "))
	}
	b.WriteString("Never run git commit, git push or anything that changes remotes: the user reviews the diff, then ADE commits and pushes.\n")
	switch {
	case t.CommitPrefix != "":
		fmt.Fprintf(b, "ADE adds the prefix %q to the MR's first commit when it commits; do not add it yourself.\n\n", t.CommitPrefix)
	case t.PrefixKnown:
		b.WriteString("The project sets no commit prefix.\n\n")
	default:
		b.WriteString("\n")
	}
}

// PlanPrompt asks for a plan and forbids changes.
func PlanPrompt(t Target, c Context) string {
	var b strings.Builder
	b.WriteString("You are planning a code change. Do not modify any file: read the repository and answer with a plan only.\n")
	targetBlock(&b, t)
	c.issueBlock(&b)
	b.WriteString("\n## What to produce\n\nReply with a Markdown plan: a one-paragraph summary, the files to change with what changes in each, " +
		"how the change will be verified, and open questions. Keep it short and concrete. The reply is saved as plan.md and reviewed by a person before anything is implemented.\n")
	return b.String()
}

// RunPrompt asks to implement the approved plan.
func RunPrompt(t Target, c Context, plan string) string {
	var b strings.Builder
	b.WriteString("Implement the approved plan below in the worktree. Carry out every step of it, including small ones such as an extra file the plan names, and nothing beyond it. Run the repository's tests if it has them.\n")
	targetBlock(&b, t)
	c.issueBlock(&b)
	fmt.Fprintf(&b, "\n## Approved plan\n\n%s\n\n## When done\n\nReply with a short summary of what you changed and anything you could not do.\n", strings.TrimSpace(plan))
	return b.String()
}

// AddressPrompt seeds an address-comments run with the unresolved threads.
func AddressPrompt(t Target, c Context) string {
	var b strings.Builder
	b.WriteString("Address the unresolved review threads below: change the code where the reviewer is right, and draft a reply for each thread. Do not resolve threads.\n")
	targetBlock(&b, t)
	c.issueBlock(&b)
	b.WriteString("\n## Unresolved threads\n")
	for _, th := range c.Threads {
		loc := "overview"
		if th.Position != nil {
			loc = fmt.Sprintf("%s:%d", th.Position.Path, max(th.Position.NewLine, th.Position.OldLine))
		}
		fmt.Fprintf(&b, "\n### thread %s (%s)\n", th.ID, loc)
		for _, n := range th.Notes {
			fmt.Fprintf(&b, "\n%s:\n%s\n", n.Author.Username, strings.TrimSpace(n.Body))
		}
	}
	b.WriteString("\n## When done\n\nEnd your reply with one fenced json block, exactly this shape, one reply per thread:\n\n" +
		"```json\n{\"replies\": [{\"thread\": \"<thread id>\", \"body\": \"<reply text>\"}]}\n```\n")
	return b.String()
}

// Reply is one drafted answer to a thread.
type Reply struct {
	Thread string `json:"thread"`
	Body   string `json:"body"`
}

var fenceJSON = regexp.MustCompile("(?s)```json\\s*(.*?)```")

// ParseReplies reads the last fenced json block of an address run's final
// message. A message without one yields no replies and no error: the code
// changes still stand.
func ParseReplies(text string) ([]Reply, error) {
	ms := fenceJSON.FindAllStringSubmatch(text, -1)
	if len(ms) == 0 {
		return nil, nil
	}
	var out struct {
		Replies []Reply `json:"replies"`
	}
	if err := json.Unmarshal([]byte(ms[len(ms)-1][1]), &out); err != nil {
		return nil, fmt.Errorf("replies block is not valid JSON: %w", err)
	}
	var rs []Reply
	for _, r := range out.Replies {
		if strings.TrimSpace(r.Body) != "" && r.Thread != "" {
			rs = append(rs, r)
		}
	}
	return rs, nil
}
