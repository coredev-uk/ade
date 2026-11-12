// Package onboard holds the pure parts of `ade init`: turning answers into a
// user config file, replacing an existing one safely, and reconciling the forge
// name the project manifests ask for. The command itself (prompts, token
// verification, project add, first sync) lives in internal/cli.
package onboard

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/coredev-uk/ade/internal/config"
)

// Answers is everything the user config needs from onboarding.
type Answers struct {
	Handle    string
	ForgeName string // key under [forges]; must match the manifests' `forge`
	Kind      string // gitlab | github
	URL       string // forge base URL; empty means the kind's public host
	TokenEnv  string
	TokenCmd  string
	// ReadTokenEnv and ReadTokenCmd name an optional read-only token; with one,
	// the token above is only used for writes and may be absent.
	ReadTokenEnv string
	ReadTokenCmd string
	GitProtocol  string // ssh | https
	CAFile       string // PEM bundle of a self-hosted forge's CA (absolute path)
	SSHPort      int    // ssh port when not 22
	Root         string // paths.root
	// AI, when set, adds a gateway profile and makes it the active one.
	AI *AIGateway
}

// Forge is the config entry the answers describe.
func (a Answers) Forge() config.Forge {
	return config.Forge{
		Kind: a.Kind, URL: a.URL, TokenEnv: a.TokenEnv, TokenCmd: a.TokenCmd,
		ReadTokenEnv: a.ReadTokenEnv, ReadTokenCmd: a.ReadTokenCmd, GitProtocol: a.GitProtocol,
		CAFile: a.CAFile, SSHPort: a.SSHPort,
	}
}

// Validate reports the first problem that would produce a config ADE rejects.
func (a Answers) Validate() error {
	switch {
	case a.Kind != "gitlab" && a.Kind != "github":
		return fmt.Errorf("forge kind %q: want gitlab or github", a.Kind)
	case a.ForgeName == "":
		return errors.New("forge name is empty")
	case a.TokenEnv == "" && a.TokenCmd == "" && a.ReadTokenEnv == "" && a.ReadTokenCmd == "":
		return errors.New("no token source: give an environment variable name or a command")
	case a.ReadTokenEnv != "" && a.ReadTokenEnv == a.TokenEnv, a.ReadTokenCmd != "" && a.ReadTokenCmd == a.TokenCmd:
		return errors.New("the read token and the write token have the same source: give the read token its own variable or command")
	case a.GitProtocol != "ssh" && a.GitProtocol != "https":
		return fmt.Errorf("git protocol %q: want ssh or https", a.GitProtocol)
	case a.Root == "":
		return errors.New("projects root is empty")
	case a.SSHPort < 0 || a.SSHPort > 65535:
		return fmt.Errorf("ssh port %d is not a port", a.SSHPort)
	case a.CAFile != "" && !filepath.IsAbs(a.CAFile):
		return fmt.Errorf("ca file %q must be an absolute path", a.CAFile)
	}
	if a.AI != nil {
		return a.AI.Validate()
	}
	return nil
}

// NormalizeHost turns "gitlab.example.com", "https://gitlab.example.com/" or
// "" into the forge base URL. The kind's public host becomes "" so the config
// stays minimal.
func NormalizeHost(kind, host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", nil
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	u, err := url.Parse(host)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("host %q is not a hostname or URL", host)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("host %q: want an http(s) URL", host)
	}
	out := strings.TrimRight(u.String(), "/")
	if out == PublicURL(kind) {
		return "", nil
	}
	return out, nil
}

// PublicURL is the kind's default host.
func PublicURL(kind string) string {
	switch kind {
	case "gitlab":
		return "https://gitlab.com"
	case "github":
		return "https://github.com"
	}
	return ""
}

// DefaultTokenEnv is the conventional token variable for kind.
func DefaultTokenEnv(kind string) string {
	switch kind {
	case "gitlab":
		return "GITLAB_TOKEN"
	case "github":
		return "GITHUB_TOKEN"
	}
	return ""
}

// ParseTokenSource reads one answer to "where does the token come from?": a
// leading "!" marks a shell command, anything else is an environment variable
// name.
func ParseTokenSource(s string) (env, cmd string, err error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", "", errors.New("empty token source")
	case strings.HasPrefix(s, "!"):
		cmd = strings.TrimSpace(s[1:])
		if cmd == "" {
			return "", "", errors.New("empty token command after \"!\"")
		}
		return "", cmd, nil
	case strings.ContainsAny(s, " \t=$"):
		return "", "", fmt.Errorf("%q is not an environment variable name (prefix a command with \"!\")", s)
	}
	return s, "", nil
}

// ForgeName reconciles the forge key the manifests refer to. All manifests in
// one init must agree (one init configures one forge); with none, or with
// manifests that name no forge, fallback is used.
func ForgeName(named []string, fallback string) (string, error) {
	name := ""
	for _, n := range named {
		switch {
		case n == "":
		case name == "":
			name = n
		case name != n:
			return "", fmt.Errorf("the manifests use different forges (%q and %q): run `ade init` once per forge, or `ade project add` for the rest", name, n)
		}
	}
	if name == "" {
		return fallback, nil
	}
	return name, nil
}

// fileConfig is the written subset of config.Config, with empty fields
// omitted so the file stays short and readable.
type fileConfig struct {
	User   fileUser             `toml:"user"`
	Forges map[string]fileForge `toml:"forges"`
	Paths  filePaths            `toml:"paths"`
	AI     *fileAI              `toml:"ai,omitempty"`
}

type fileUser struct {
	Handle string `toml:"handle,omitempty"`
}

type fileForge struct {
	Kind        string `toml:"kind"`
	URL         string `toml:"url,omitempty"`
	TokenEnv    string `toml:"token_env,omitempty"`
	TokenCmd    string `toml:"token_cmd,omitempty"`
	GitProtocol string `toml:"git_protocol"`
	CAFile      string `toml:"ca_file,omitempty"`
	SSHPort     int    `toml:"ssh_port,omitempty"`

	ReadTokenEnv string `toml:"read_token_env,omitempty"`
	ReadTokenCmd string `toml:"read_token_cmd,omitempty"`
}

type filePaths struct {
	Root string `toml:"root"`
}

const header = `# ADE user config. Written by ` + "`ade init`" + `; edit freely.
# Secrets never live here: forges name an environment variable or a command that prints the token.
# Projects are added with ` + "`ade project add <manifest>`" + `; every other setting has a default (docs/PLAN.md §2).

`

// Render returns the config file for a. Projects are not part of it: they are
// registered by project add, which appends [[projects]] blocks.
func Render(a Answers) ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString(header)
	err := toml.NewEncoder(&buf).Encode(fileConfig{
		User: fileUser{Handle: a.Handle},
		Forges: map[string]fileForge{a.ForgeName: {
			Kind: a.Kind, URL: a.URL, TokenEnv: a.TokenEnv, TokenCmd: a.TokenCmd, GitProtocol: a.GitProtocol,
			CAFile: a.CAFile, SSHPort: a.SSHPort, ReadTokenEnv: a.ReadTokenEnv, ReadTokenCmd: a.ReadTokenCmd,
		}},
		Paths: filePaths{Root: a.Root},
		AI:    a.AI.file(),
	})
	return buf.Bytes(), err
}

// ErrExists means a config file is already there and replacing it was not asked for.
var ErrExists = errors.New("config file already exists")

// WriteConfig writes data to path with owner-only permissions. An existing file
// is only replaced with force, and then first copied to a timestamped .bak
// beside it (returned as backup). The write is atomic: a crash leaves either
// the old or the new file.
func WriteConfig(path string, data []byte, force bool, now time.Time) (backup string, err error) {
	old, err := os.ReadFile(path)
	switch {
	case err == nil:
		if !force {
			return "", fmt.Errorf("%w: %s", ErrExists, path)
		}
		backup, err = writeBackup(path, old, now)
		if err != nil {
			return "", fmt.Errorf("back up %s: %w", path, err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after a successful rename
	if _, err := io.Copy(tmp, bytes.NewReader(data)); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return "", err
	}
	return backup, os.Rename(tmp.Name(), path)
}

// writeBackup copies old beside path as <path>.bak-<timestamp>, never
// overwriting an earlier backup.
func writeBackup(path string, old []byte, now time.Time) (string, error) {
	base := fmt.Sprintf("%s.bak-%s", path, now.Format("20060102-150405"))
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(old)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return name, werr
	}
}
