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
	"slices"
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
	AI       AI               `toml:"ai"`
	Budget   Budget           `toml:"budget"`
	Dotfiles DotfilesOverride `toml:"dotfiles"`

	// File is the path the config was loaded from; Missing is true when no file
	// existed and defaults are in effect.
	File    string `toml:"-"`
	Missing bool   `toml:"-"`
	// Overlay is the local override file merged over File ("" when there is none).
	Overlay string `toml:"-"`
	// Undecoded lists keys present in the file that ADE does not know.
	Undecoded []string `toml:"-"`
}

type User struct {
	Handle   string   `toml:"handle"`
	Timezone string   `toml:"timezone"`
	Workdays []string `toml:"workdays"`
	// Holidays are non-working dates ("YYYY-MM-DD"); standup skips them.
	Holidays []string `toml:"holidays"`
}

// Forge is one GitLab or GitHub host. Credentials come from TokenCmd or
// TokenEnv (the write token, which also reads when no read token is set) and
// ReadTokenCmd or ReadTokenEnv (the read token: syncs, dashboards, AI context).
type Forge struct {
	Kind     string `toml:"kind"` // gitlab | github
	URL      string `toml:"url"`  // web/API base; defaults per kind
	TokenCmd string `toml:"token_cmd"`
	TokenEnv string `toml:"token_env"`
	// ReadTokenCmd and ReadTokenEnv name a second, read-only token. With one
	// set, every read (GET, GraphQL queries, clone and fetch) uses it, the
	// write token is resolved only when ADE writes, and agents ADE launches
	// do not see TokenEnv.
	ReadTokenCmd string `toml:"read_token_cmd"`
	ReadTokenEnv string `toml:"read_token_env"`
	GitProtocol  string `toml:"git_protocol"` // ssh | https (default ssh)
	// CAFile is a PEM bundle of the certificate authority a self-hosted
	// forge's certificate chains to. It is added to the system pool for API
	// calls and handed to git (http.<host>.sslCAInfo) for https clones.
	CAFile string `toml:"ca_file"`
	// SSHPort is the forge's SSH port when it is not 22; clone URLs then use
	// the ssh://git@host:port/path form.
	SSHPort int `toml:"ssh_port"`
	// MaxConcurrency caps in-flight API requests to this forge (default 4).
	MaxConcurrency int `toml:"max_concurrency"`
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
	// Toast lists event-kind globs shown as pop-ups in the dashboard. Every
	// event is recorded in the inbox regardless.
	Toast []string `toml:"toast"`
}

type Records struct {
	Dir              string `toml:"dir"`
	AuditRetain      string `toml:"audit_retain"`
	SnapshotComments int    `toml:"snapshot_comments"`
}

// AI selects the active profile and holds every profile of this machine.
type AI struct {
	Mode     string               `toml:"mode"`    // off | ask | auto
	Profile  string               `toml:"profile"` // key into Profiles
	Profiles map[string]AIProfile `toml:"profiles"`
}

// AIProfile is one place AI calls can go: a driver plus the models its roles
// use and where its spend is read from.
type AIProfile struct {
	Driver  string `toml:"driver"` // omp | anthropic | openai-compat
	BaseURL string `toml:"base_url"`
	KeyCmd  string `toml:"key_cmd"`
	KeyEnv  string `toml:"key_env"`
	// Command is the omp binary (driver "omp"); default "omp".
	Command string `toml:"command"`
	// Agent is the CLI that runs agent work (plan, run, address comments) for
	// this profile: omp (default), claude or codex. AgentCommand is its
	// binary when not on PATH under that name.
	Agent        string `toml:"agent"`
	AgentCommand string `toml:"agent_command"`
	// Models maps a role (explain, classify, draft, plan, implement, review)
	// to a model id. A missing classify model falls back to explain; a missing
	// review model to draft, then explain.
	Models map[string]string `toml:"models"`
	// ExtraHeaders are sent on every request of an HTTP driver, e.g. a
	// gateway's team or tag headers. Authorization is the key's job.
	ExtraHeaders map[string]string `toml:"extra_headers"`
	// Timeout caps one request of an HTTP driver (default 5m).
	Timeout Duration `toml:"timeout"`
	// Prices maps a model id to its list prices, for pre-flight estimates and
	// for drivers whose responses carry no cost.
	Prices  map[string]Price `toml:"prices"`
	Billing AIBilling        `toml:"billing"`
}

// Price is USD per million tokens.
type Price struct {
	Input      float64 `toml:"input"`
	Output     float64 `toml:"output"`
	CacheRead  float64 `toml:"cache_read"`
	CacheWrite float64 `toml:"cache_write"`
}

// AIBilling says where a profile's authoritative spend comes from (see
// docs/billing). AdminKey* are for sources that need an organisation key;
// BaseURL overrides the source's default endpoint host.
type AIBilling struct {
	Source        string  `toml:"source"` // litellm | omp | anthropic | openai | moonshot | openrouter | none
	WeeklySoftUSD float64 `toml:"weekly_soft_usd"`
	BaseURL       string  `toml:"base_url"`
	AdminKeyCmd   string  `toml:"admin_key_cmd"`
	AdminKeyEnv   string  `toml:"admin_key_env"`
}

// Budget holds the local spend caps. Zero means no cap.
type Budget struct {
	DailyUSD       float64 `toml:"daily_usd"`
	WeeklyUSD      float64 `toml:"weekly_usd"`
	SprintUSD      float64 `toml:"sprint_usd"`
	CallUSD        float64 `toml:"call_usd"`         // hard cap per call
	ConfirmOverUSD float64 `toml:"confirm_over_usd"` // pre-flight confirmation threshold per action
	AgentRunUSD    float64 `toml:"agent_run_usd"`    // passed as --max-cost to omp runs
}

// DotfilesOverride is your say over the project dotfiles every manifest asks
// for: use another repo or branch, or turn them off on this machine. It never
// adds dotfiles to a project whose manifest has none.
type DotfilesOverride struct {
	Enabled *bool  `toml:"enabled"` // unset: true
	Repo    string `toml:"repo"`
	Branch  string `toml:"branch"`
}

// On reports whether project dotfiles are applied on this machine.
func (d DotfilesOverride) On() bool { return d.Enabled == nil || *d.Enabled }

type Host struct {
	Mode   string `toml:"mode"`   // auto | zellij | vscode | plain
	Editor string `toml:"editor"` // auto | nvim | ade-nvim | code | $EDITOR
	Glyphs string `toml:"glyphs"` // unicode | nerd | ascii
	// Layout is your default workspace tab layout: code (editor main pane) or agent
	// (omp with the ADE implement skill) or manual (a shell). Empty: the manifest's
	// workspace layout, else code.
	Layout string `toml:"layout"`
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
			Toast: []string{"*"},
		},
		Records: Records{AuditRetain: "forever", SnapshotComments: 20},
		Host:    Host{Mode: "auto", Editor: "auto", Glyphs: "unicode"},
		AI:      AI{Mode: "ask", Profiles: map[string]AIProfile{}},
		Budget:  Budget{DailyUSD: 2, WeeklyUSD: 8, ConfirmOverUSD: 0.25, AgentRunUSD: 1.5},
	}
}

// Load reads the config at path, layering it over Defaults and then the local
// override beside it (config.local.toml, see LocalPath and MergeTOML). A
// missing file is not an error: Missing is set and defaults apply.
func Load(path string) (*Config, error) {
	cfg := Defaults()
	cfg.File = path
	data, err := os.ReadFile(path)
	missing := errors.Is(err, fs.ErrNotExist)
	if err != nil && !missing {
		return nil, err
	}
	ov, err := readOverlay(path)
	if err != nil {
		return nil, err
	}
	if missing && ov == nil {
		cfg.Missing = true
		return &cfg, nil
	}
	if ov != nil {
		if data, err = MergeTOML(data, path, ov, LocalPath(path)); err != nil {
			return nil, err
		}
		cfg.Overlay = LocalPath(path)
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
		if f.MaxConcurrency == 0 {
			f.MaxConcurrency = DefaultMaxConcurrency
		}
		if f.CAFile != "" {
			if p, err := cfg.ResolvePath(f.CAFile); err == nil {
				f.CAFile = p
			}
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

// Token resolves the forge's write credential from TokenEnv or TokenCmd; it
// is also the read credential when HasReadToken is false. The value is never
// logged or persisted by ADE.
func (f Forge) Token(ctx context.Context) (string, error) {
	return resolveToken(ctx, "token", f.TokenEnv, f.TokenCmd)
}

// HasReadToken reports whether the forge names a separate read token.
func (f Forge) HasReadToken() bool { return f.ReadTokenEnv != "" || f.ReadTokenCmd != "" }

// HasWriteToken reports whether the forge names a write token (token_env or
// token_cmd). A forge with only a read token is read-only.
func (f Forge) HasWriteToken() bool { return f.TokenEnv != "" || f.TokenCmd != "" }

// ReadToken resolves the credential for reads: the read token when one is
// configured, else the write token (Token).
func (f Forge) ReadToken(ctx context.Context) (string, error) {
	if !f.HasReadToken() {
		return f.Token(ctx)
	}
	return resolveToken(ctx, "read_token", f.ReadTokenEnv, f.ReadTokenCmd)
}

// resolveToken reads the variable env, falling back to running cmd. key names
// the pair in messages ("token" for token_env/token_cmd).
func resolveToken(ctx context.Context, key, env, command string) (string, error) {
	if env != "" {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, nil
		}
		if command == "" {
			return "", fmt.Errorf("%s_env %s is empty", key, env)
		}
	}
	if command == "" {
		return "", fmt.Errorf("no %s_cmd or %s_env configured", key, key)
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s_cmd failed: %w: %s", key, err, strings.TrimSpace(stderr.String()))
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("%s_cmd produced no output", key)
	}
	return tok, nil
}

// AgentHiddenEnv names the environment variables agents launched by ADE must
// not inherit: the token_env of every forge that has a read token, so an agent
// reaches the forge with the read token at most. Forges without a read token
// are left alone (the agent sees what the user's shell sees). A token_cmd
// cannot be hidden this way: the agent runs as the same user.
func (c *Config) AgentHiddenEnv() []string {
	if c == nil {
		return nil
	}
	var names []string
	for _, f := range c.Forges {
		if f.HasReadToken() && f.TokenEnv != "" && f.TokenEnv != f.ReadTokenEnv && !slices.Contains(names, f.TokenEnv) {
			names = append(names, f.TokenEnv)
		}
	}
	slices.Sort(names)
	return names
}

// WithoutEnv returns environ (KEY=value entries) minus the variables names.
func WithoutEnv(environ, names []string) []string {
	if len(names) == 0 {
		return environ
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(names, k) {
			out = append(out, kv)
		}
	}
	return out
}
