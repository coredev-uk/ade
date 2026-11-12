package doctor

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/codews"
)

// VSCodeSettings is the recommended user settings for running ADE in VS Code's
// integrated terminal; the definition lives in codews, which cannot import
// this package (it would cycle through internal/project).
var VSCodeSettings = codews.UserSettings

// VSCodeSnippet renders VSCodeSettings as the JSON to merge into settings.json.
func VSCodeSnippet() string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	_ = enc.Encode(VSCodeSettings) // a map of strings, bools and string lists cannot fail
	return b.String()
}

// VSCodeOptions locate the user's VS Code and the directory being checked.
type VSCodeOptions struct {
	Cwd string // directory to look for .vscode/tasks.json in
	// Home overrides the home directory settings.json is looked up under (tests).
	Home string
}

const vscodeGroup = "vscode"

// VSCode checks what ADE needs from VS Code: the `code` launcher, the host ADE
// would pick here, the generated tasks.json, and the terminal key settings.
func VSCode(o Options, v VSCodeOptions) []Check {
	o.defaults()
	checks := []Check{terminalCheck(o), codeCheck(), vscodeHostCheck(o)}
	if c, ok := tasksCheck(o, v.Cwd); ok {
		checks = append(checks, c)
	}
	return append(checks, settingsCheck(o, v.Home))
}

func terminalCheck(o Options) Check {
	c := Check{Group: vscodeGroup, Name: "terminal", Status: OK}
	if o.Getenv("TERM_PROGRAM") == "vscode" {
		c.Detail = "running in the VS Code integrated terminal"
	} else {
		c.Detail = "not in a VS Code terminal; the checks below apply to using ADE from VS Code"
	}
	return c
}

func codeCheck() Check {
	c := Check{Group: vscodeGroup, Name: "code launcher"}
	if p, err := exec.LookPath("code"); err == nil {
		c.Status, c.Detail = OK, p
		return c
	}
	c.Status, c.Detail = Warn, "`code` is not on PATH: ADE cannot open a worktree in a new window"
	c.Fix = "in VS Code run: Shell Command: Install 'code' command in PATH"
	return c
}

func vscodeHostCheck(o Options) Check {
	c := Check{Group: vscodeGroup, Name: "host here", Status: OK}
	mode := ""
	if o.Config != nil {
		mode = o.Config.Host.Mode
	}
	inVSCode := o.Getenv("TERM_PROGRAM") == "vscode"
	switch {
	case mode == "vscode":
		c.Detail = "host.mode = vscode: workspaces open as `code <worktree>` with generated tasks"
	case mode == "zellij" || mode == "plain":
		c.Detail = "host.mode = " + mode + " is forced; VS Code is not used as the host"
	case inVSCode && o.Getenv("ZELLIJ") == "":
		c.Detail = "VS Code terminal without zellij: workspaces open as `code <worktree>` with generated tasks"
	case inVSCode:
		c.Detail = "zellij inside the VS Code terminal: workspaces open as zellij tabs; use ade vscode tasks for VS Code tasks"
	default:
		c.Detail = "VS Code is not the host here (set host.mode = \"vscode\" to force it)"
	}
	return c
}

// tasksCheck inspects cwd's .vscode/tasks.json. ok is false when there is
// nothing to say: no file and cwd is not a worktree of a registered project.
func tasksCheck(o Options, cwd string) (Check, bool) {
	c := Check{Group: vscodeGroup, Name: "tasks.json"}
	if cwd == "" {
		return c, false
	}
	path := filepath.Join(cwd, ".vscode", "tasks.json")
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if o.Root == "" || !within(o.Root, cwd) {
			return c, false
		}
		c.Status, c.Detail = Warn, "no .vscode/tasks.json in this worktree"
		c.Fix = "ade vscode tasks"
		return c, true
	case err != nil:
		c.Status, c.Detail = Warn, err.Error()
		return c, true
	}
	var doc struct {
		Tasks []struct {
			Label string `json:"label"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		c.Status = Warn
		c.Detail = path + " is not plain JSON (comments?): `ade vscode tasks` cannot merge into it"
		c.Fix = "remove the comments, or move your tasks into a workspace file"
		return c, true
	}
	n := 0
	for _, t := range doc.Tasks {
		if strings.HasPrefix(t.Label, "ade: ") {
			n++
		}
	}
	c.Status = OK
	if n == 0 {
		c.Status, c.Detail, c.Fix = Warn, "tasks.json has no generated ADE tasks", "ade vscode tasks"
	} else {
		c.Detail = strconv.Itoa(n) + " generated ADE tasks"
	}
	return c, true
}

func within(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

// settingsCheck reads the user's settings.json as text (it is JSONC, so it is
// not parsed) and looks for the recommended removals.
func settingsCheck(o Options, home string) Check {
	c := Check{Group: vscodeGroup, Name: "terminal key settings"}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	var found []string
	for _, rel := range []string{
		".config/Code/User/settings.json",
		"Library/Application Support/Code/User/settings.json",
		".vscode-server/data/User/settings.json",
	} {
		p := filepath.Join(home, rel)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		found = append(found, p)
		text := string(data)
		if strings.Contains(text, "commandsToSkipShell") && strings.Contains(text, "-workbench.action.quickOpen") {
			c.Status, c.Detail = OK, "commandsToSkipShell frees Ctrl+P for the terminal in "+p
			return c
		}
	}
	c.Status = Warn
	if len(found) == 0 {
		c.Detail = "no VS Code settings.json found to verify (remote and portable installs keep it elsewhere)"
	} else {
		c.Detail = "commandsToSkipShell does not release Ctrl+P, Ctrl+B and Ctrl+J to the terminal in " + found[0]
	}
	c.Fix = "merge the snippet printed by `ade doctor --vscode` into settings.json"
	return c
}
