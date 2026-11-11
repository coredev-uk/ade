// Package config loads ADE's user config (one per machine) and project manifests (shared).
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/coredev-uk/ade/internal/paths"
)

// Config is the user-local configuration (~/.config/ade/config.toml). It never
// leaves the machine and never holds secrets directly, only commands/env names
// that produce them.
type Config struct {
	User     User             `toml:"user"`
	Forges   map[string]Forge `toml:"forges"`
	Paths    PathsCfg         `toml:"paths"`
	Projects []ProjectRef     `toml:"projects"`
	Update   Update           `toml:"update"`
	Notify   Notify           `toml:"notify"`
	Records  Records          `toml:"records"`
	Host     Host             `toml:"host"`

	// File is the path the config was loaded from; Missing is true when no file
	// existed and defaults are in effect.
	File    string `toml:"-"`
	Missing bool   `toml:"-"`
	// Undecoded lists keys present in the file that ADE does not know.
	Undecoded []string `toml:"-"`
}

type User struct {
	Handle   string   `toml:"handle"`
	Timezone string   `toml:"timezone"`
	Workdays []string `toml:"workdays"`
}

// Forge is one GitLab or GitHub host. Credentials come from TokenCmd or TokenEnv.
type Forge struct {
	Kind        string `toml:"kind"` // gitlab | github
	URL         string `toml:"url"`  // web/API base; defaults per kind
	TokenCmd    string `toml:"token_cmd"`
	TokenEnv    string `toml:"token_env"`
	GitProtocol string `toml:"git_protocol"` // ssh | https (default ssh)
}

type PathsCfg struct {
	Root string `toml:"root"` // default ~/ade
}

// ProjectRef registers a project manifest on this machine.
type ProjectRef struct {
	Source string `toml:"source"` // local path, https URL, or git+<url>#<path>
}

type Update struct {
	OnStart         *bool    `toml:"on_start"`
	Interval        Duration `toml:"interval"`
	FeatureBranches string   `toml:"feature_branches"` // ff-if-clean | notify-only
	Concurrency     int      `toml:"concurrency"`
}

type Notify struct {
	Toast    []string `toml:"toast"`
	Terminal []string `toml:"terminal"`
	Desktop  []string `toml:"desktop"`
	Quiet    string   `toml:"quiet"` // "HH:MM-HH:MM", local time
}

type Records struct {
	Dir              string `toml:"dir"`
	AuditRetain      string `toml:"audit_retain"`
	SnapshotComments int    `toml:"snapshot_comments"`
}

type Host struct {
	Mode   string `toml:"mode"`   // auto | zellij | vscode | plain
	Editor string `toml:"editor"` // auto | nvim | code | $EDITOR
	Glyphs string `toml:"glyphs"` // unicode | nerd | ascii
}

// Duration decodes TOML strings like "10m".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Defaults returns the configuration used when keys are absent.
func Defaults() Config {
	on := true
	return Config{
		User:   User{Workdays: []string{"mon", "tue", "wed", "thu", "fri"}},
		Forges: map[string]Forge{},
		Paths:  PathsCfg{Root: "~/ade"},
		Update: Update{OnStart: &on, Interval: Duration{10 * time.Minute}, FeatureBranches: "ff-if-clean", Concurrency: 4},
		Notify: Notify{
			Toast:    []string{"*"},
			Terminal: []string{"ci.failed", "task.failed", "agent.run.end", "git.*.failed", "budget.stop"},
		},
		Records: Records{AuditRetain: "forever", SnapshotComments: 20},
		Host:    Host{Mode: "auto", Editor: "auto", Glyphs: "unicode"},
	}
}

// Load reads the config at path, layering it over Defaults. A missing file is
// not an error: Missing is set and defaults apply.
func Load(path string) (*Config, error) {
	cfg := Defaults()
	cfg.File = path
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		cfg.Missing = true
		return &cfg, nil
	}
	if err != nil {
		return nil, err
	}
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, k := range md.Undecoded() {
		cfg.Undecoded = append(cfg.Undecoded, k.String())
	}
	sort.Strings(cfg.Undecoded)
	for name, f := range cfg.Forges {
		if f.URL == "" {
			switch f.Kind {
			case "gitlab":
				f.URL = "https://gitlab.com"
			case "github":
				f.URL = "https://github.com"
			}
		}
		if f.GitProtocol == "" {
			f.GitProtocol = "ssh"
		}
		f.URL = strings.TrimRight(f.URL, "/")
		cfg.Forges[name] = f
	}
	return &cfg, nil
}

// ResolvePath expands "~/" and makes relative paths relative to the directory
// of the config file, so a config is portable with its neighbouring files.
func (c *Config) ResolvePath(p string) (string, error) {
	p, err := paths.Expand(p)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(p) || c.File == "" {
		return filepath.Clean(p), nil
	}
	return filepath.Join(filepath.Dir(c.File), p), nil
}

// RootDir returns the resolved projects root (paths.root).
func (c *Config) RootDir() (string, error) { return c.ResolvePath(c.Paths.Root) }

// RecordsDir returns the resolved records directory, defaulting to dataDir.
func (c *Config) RecordsDir(dataDir string) (string, error) {
	if c.Records.Dir == "" {
		return dataDir, nil
	}
	return c.ResolvePath(c.Records.Dir)
}

// UpdateOnStart reports whether `ade` should run an update at startup.
func (c *Config) UpdateOnStart() bool { return c.Update.OnStart == nil || *c.Update.OnStart }

// Token resolves the forge credential from TokenEnv or TokenCmd. The value is
// never logged or persisted by ADE.
func (f Forge) Token(ctx context.Context) (string, error) {
	if f.TokenEnv != "" {
		if v := strings.TrimSpace(os.Getenv(f.TokenEnv)); v != "" {
			return v, nil
		}
		if f.TokenCmd == "" {
			return "", fmt.Errorf("token_env %s is empty", f.TokenEnv)
		}
	}
	if f.TokenCmd == "" {
		return "", errors.New("no token_cmd or token_env configured")
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", f.TokenCmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("token_cmd failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", errors.New("token_cmd produced no output")
	}
	return tok, nil
}
