package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/prompt"
)

func init() { register(newPromptCmd) }

func newPromptCmd() *cobra.Command {
	var format, path, scope string
	var zero bool
	cmd := &cobra.Command{
		Use:   "prompt",
		Short: "Shell-prompt segment: the worktree's ticket and how many items wait on you",
		Long: `Prints the ticket of the current worktree (its commit prefix, e.g. GL-412, else
#<tracker iid>) and how many items wait on you (items whose court is you, plus
open MRs asking for your review), for a shell prompt:

  PS1='$(ade prompt) \$ '

It reads the worktree's git files and the cache only: no network, no git
process, nothing written (not even the audit log). Outside an ADE tree it
prints nothing and exits 0, so it can stay in the prompt everywhere. Without
a cache (no sync yet) the attention count is left out.

--format takes {ticket} {attention} ("!3") {attention_n} (3) {items} {reviews}
{project} {repo} {branch} {worktree}. Empty values and the blanks around them
disappear; {attention} and {attention_n} are empty when nothing waits unless
--zero. With --json it prints the whole answer, also outside a tree.`,
		Args: cobra.NoArgs,
		// A prompt must start fast and survive a broken or missing config: it
		// loads only what it needs, and leaves no audit line per prompt.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := prompt.CheckFormat(format); err != nil {
				return withCode(2, err)
			}
			if scope != "all" && scope != "project" {
				return withCode(2, fmt.Errorf("--scope %q: want all or project", scope))
			}
			p, err := paths.Default()
			if err != nil {
				return err
			}
			cfg, err := config.Load(p.ConfigFile)
			if err != nil {
				return err
			}
			root, err := cfg.RootDir()
			if err != nil {
				return fmt.Errorf("paths.root: %w", err)
			}
			if path == "" {
				if path, err = os.Getwd(); err != nil {
					return err
				}
			}
			info := prompt.Read(prompt.Options{Root: root, CacheDB: filepath.Join(p.CacheDir, "cache.db"), Path: path, AllProjects: scope == "all"})
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), info)
			}
			if s := prompt.Render(format, info, zero); s != "" {
				fmt.Fprintln(cmd.OutOrStdout(), s)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", prompt.DefaultFormat, "output template: {ticket} {attention} {attention_n} {items} {reviews} {project} {repo} {branch} {worktree}")
	cmd.Flags().StringVar(&path, "path", "", "directory to describe (default: the working directory)")
	cmd.Flags().StringVar(&scope, "scope", "all", "count attention over all projects, or only the directory's project: all | project")
	cmd.Flags().BoolVar(&zero, "zero", false, "show the attention count when it is 0")
	return cmd
}
