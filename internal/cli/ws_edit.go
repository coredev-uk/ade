package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/editor"
	"github.com/coredev-uk/ade/internal/workspace"
)

// editSettings are the values of `ade ws edit --editor`: those of host.editor.
var editSettings = []string{editor.SettingAuto, editor.SettingCode, editor.SettingNvim, editor.SettingADENvim, editor.SettingEnv}

func newWsEditCmd() *cobra.Command {
	var setting string
	cmd := &cobra.Command{
		Use:   "edit [worktree]",
		Short: "Open a worktree in your editor (host.editor, or --editor)",
		Long: `Opens the worktree in the editor host.editor resolves to, or in the one
--editor names (code, nvim, ade-nvim, '$EDITOR' or auto, as host.editor takes):
code opens the worktree in a VS Code window (code-server's browser window
when that is where VS Code runs) and returns; a terminal editor runs in this
terminal until you quit it.

Run it in the workspace tab's shell: that shell runs inside the worktree's dev
shell, so a terminal editor gets the repo's language servers and linters. An
--editor whose program is missing is an error rather than a fallback. The
worktree defaults to the current directory.`,
		Example: "  ade ws edit\n  ade ws edit --editor code\n  ade ws edit ~/ade/demo/web/418-paginate --editor nvim",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if setting != "" && !slices.Contains(editSettings, setting) {
				return fmt.Errorf("--editor %q: want code, nvim, ade-nvim, $EDITOR or auto", setting)
			}
			a := appFrom(cmd)
			loc, err := workspace.Locate(a.Root, worktreeArg(args))
			if err != nil {
				return err
			}
			env := editor.FromOS()
			want := setting
			if want == "" {
				want = a.Config.Host.Editor
			}
			ed := editor.Resolve(want, env)
			if setting != "" && !resolvesTo(setting, ed) {
				if setting == editor.SettingCode {
					return errors.New(env.NoCodeHint(loc.Tree))
				}
				return fmt.Errorf("--editor %s: not available here (ade doctor checks editors)", setting)
			}
			argv := ed.ArgvDir(loc.Tree)
			c := exec.Command(argv[0], argv[1:]...)
			c.Dir, c.Env = loc.Tree, ed.Environ()
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
			if err := c.Run(); err != nil {
				return fmt.Errorf("%s: %w", ed.Name, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&setting, "editor", "", "code | nvim | ade-nvim | $EDITOR | auto (default: host.editor)")
	return cmd
}

// resolvesTo reports whether ed is the editor setting asked for, not the
// fallback Resolve picks when that editor is missing.
func resolvesTo(setting string, ed editor.Editor) bool {
	switch setting {
	case editor.SettingAuto:
		return true
	case editor.SettingCode:
		return ed.Kind == editor.Code
	}
	return ed.Name == setting
}
