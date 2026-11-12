package onboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// This file holds the pure and filesystem parts of `ade setup`: the machine
// tool table, the cross-process lock, the config template fetch. The command
// itself lives in internal/cli.

// Tool is one program a fully set-up machine has.
type Tool struct {
	Name     string `json:"name"`
	Why      string `json:"why"`
	Nix      string `json:"-"` // nixpkgs attribute; empty when Hint is the only advice
	Hint     string `json:"-"` // how to get it when there is no nixpkgs attribute to name
	Required bool   `json:"required"`
}

// SetupTools lists what `ade setup` checks for.
func SetupTools() []Tool {
	return []Tool{
		{Name: "git", Why: "bare clones and worktrees", Nix: "git", Required: true},
		{Name: "zellij", Why: "the work-tab host", Nix: "zellij"},
		{Name: "lazygit", Why: "the git pane", Nix: "lazygit"},
		{Name: "direnv", Why: "per-repo dev shells (.envrc)", Nix: "direnv"},
		{Name: "nix", Why: "per-repo dev shells (flake.nix)", Hint: "install Nix: https://nixos.org/download"},
		{Name: "omp", Why: "the agent driver", Hint: "install omp (oh-my-pi) and put it on PATH"},
		{Name: "code", Why: "VS Code remote CLI (open files and diffs from the terminal)", Hint: "open a VS Code remote terminal once: it puts the remote `code` CLI on PATH"},
	}
}

// ToolStatus is the result of looking for one Tool.
type ToolStatus struct {
	Tool
	Path    string `json:"path,omitempty"`
	Missing bool   `json:"missing"`
}

// CheckTools looks every tool up with look (exec.LookPath in production).
// The VS Code remote CLI also counts when it sits in the remote server's
// directory under home, where a terminal that VS Code did not start cannot
// see it on PATH.
func CheckTools(look func(string) (string, error), home string) []ToolStatus {
	var out []ToolStatus
	for _, t := range SetupTools() {
		p, err := look(t.Name)
		if err != nil && t.Name == "code" && home != "" {
			if m, _ := filepath.Glob(filepath.Join(home, ".vscode-server", "bin", "*", "bin", "remote-cli", "code")); len(m) > 0 {
				p, err = m[len(m)-1], nil
			}
		}
		out = append(out, ToolStatus{Tool: t, Path: p, Missing: err != nil})
	}
	return out
}

// MissingAdvice returns one line per missing tool saying how to get it, and
// the single `nix profile install` command covering those with an attribute.
func MissingAdvice(sts []ToolStatus) (lines []string, nixCmd string) {
	var attrs []string
	for _, s := range sts {
		if !s.Missing {
			continue
		}
		switch {
		case s.Nix != "":
			attrs = append(attrs, "nixpkgs#"+s.Nix)
			lines = append(lines, fmt.Sprintf("%s missing (%s)", s.Name, s.Why))
		default:
			lines = append(lines, fmt.Sprintf("%s missing (%s): %s", s.Name, s.Why, s.Hint))
		}
	}
	if len(attrs) > 0 {
		nixCmd = "nix profile install " + strings.Join(attrs, " ")
	}
	return lines, nixCmd
}

// SplitList splits an environment variable holding a list: entries are
// separated by newlines, spaces or commas.
func SplitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ' ' || r == ',' || r == '\t' || r == '\r' })
}

// Lock takes an exclusive flock on path, creating it (and its directory), and
// waits for a competing holder to finish. onWait is called once when the lock
// is not free at first.
func Lock(ctx context.Context, path string, onWait func()) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	waited := false
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
				f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if !waited && onWait != nil {
			waited = true
			onWait()
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

const maxTemplate = 1 << 20

// FetchTemplate reads a team config template from a local path or an
// http(s) URL.
func FetchTemplate(ctx context.Context, src string) ([]byte, error) {
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if err != nil {
			return nil, err
		}
		cl := &http.Client{Timeout: 30 * time.Second}
		resp, err := cl.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: %s", src, resp.Status)
		}
		return readCapped(resp.Body)
	}
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readCapped(f)
}

func readCapped(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxTemplate+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxTemplate {
		return nil, errors.New("config template is larger than 1 MiB")
	}
	return b, nil
}
