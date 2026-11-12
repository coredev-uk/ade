// Package codews builds the content of ADE's generated VS Code multi-root
// workspace files (*.code-workspace): the folders, the settings ADE sets and
// the merge that keeps whatever else the user put in a regenerated file. The
// per-item workspace (internal/workspace) and the per-group workspace files
// of a project (internal/project) share it.
package codews

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
)

// Ext is the extension of a VS Code workspace file.
const Ext = ".code-workspace"

// folderNameMax bounds the tree name shown in a folder's label.
const folderNameMax = 24

// Folder is one root of a workspace.
type Folder struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// FolderName is the label of a repo's folder: "<repo> (<tree directory>)".
func FolderName(repo, tree string) string {
	return repo + " (" + truncateName(filepath.Base(tree), folderNameMax) + ")"
}

// truncateName cuts s to at most n runes, ending in … when it was cut.
func truncateName(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// UserSettings is the recommended user settings for running ADE (and zellij
// inside it) in VS Code's integrated terminal. Entries starting with "-" remove
// a command from VS Code's default skip-the-shell list, so its key reaches the
// program in the terminal instead: Ctrl+P and Ctrl+B belong to zellij and
// Neovim there, Ctrl+J to the shell. Chords (Ctrl+K ...) stay with the terminal
// program, and Option acts as Meta so Alt bindings work on macOS.
var UserSettings = map[string]any{
	"terminal.integrated.commandsToSkipShell": []string{
		"-workbench.action.quickOpen",
		"-workbench.action.toggleSidebarVisibility",
		"-workbench.action.togglePanel",
	},
	"terminal.integrated.allowChords":     false,
	"terminal.integrated.macOptionIsMeta": true,
}

// Settings are the workspace settings ADE sets: bare repos and their
// worktree metadata stay out of the explorer, search and file watcher; git
// only looks for repositories in the folders themselves (the project's repo
// directories hold a bare clone, not a checkout, so parents never apply); and
// the terminal hands Ctrl+P/B/J to zellij and the shell, as `ade doctor
// --vscode` recommends.
func Settings() map[string]any {
	return map[string]any{
		"files.exclude":                           map[string]bool{"**/.bare": true},
		"search.exclude":                          map[string]bool{"**/.bare": true},
		"files.watcherExclude":                    map[string]bool{"**/.bare/**": true},
		"git.autoRepositoryDetection":             "subFolders",
		"git.openRepositoryInParentFolders":       "never",
		"terminal.integrated.commandsToSkipShell": UserSettings["terminal.integrated.commandsToSkipShell"],
	}
}

// Merge returns the .code-workspace content for folders on top of an existing
// file (nil for none): folders and ADE's settings keys are regenerated, every
// other top-level key and setting survives. extra, when set, may change the
// top-level keys before they are written (the item workspace merges its tasks
// there). A file that is not plain JSON (VS Code allows comments) is an error
// rather than being overwritten.
func Merge(existing []byte, folders []Folder, extra func(rest map[string]json.RawMessage) error) ([]byte, error) {
	rest := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &rest); err != nil {
			return nil, fmt.Errorf("existing workspace file is not plain JSON (comments?): %w", err)
		}
	}
	if folders == nil {
		folders = []Folder{}
	}
	if err := SetRaw(rest, "folders", folders); err != nil {
		return nil, err
	}

	settings := map[string]json.RawMessage{}
	if raw, ok := rest["settings"]; ok {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return nil, fmt.Errorf("existing workspace file: settings: %w", err)
		}
	}
	for k, v := range Settings() {
		if err := SetRaw(settings, k, v); err != nil {
			return nil, err
		}
	}
	if err := SetRaw(rest, "settings", settings); err != nil {
		return nil, err
	}

	if extra != nil {
		if err := extra(rest); err != nil {
			return nil, err
		}
	}

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rest); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// SetRaw sets m[key] to the JSON encoding of v.
func SetRaw(m map[string]json.RawMessage, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m[key] = b
	return nil
}
