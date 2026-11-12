package project

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
)

// The URL matrix of a self-hosted GitLab: path prefix, custom ssh port,
// protocol. The ssh path never carries the web prefix: it is the project path.
func TestCloneURLSelfHosted(t *testing.T) {
	tests := []struct {
		name  string
		forge config.Forge
		repo  config.Repo
		want  string
	}{
		{
			"https with a path prefix",
			config.Forge{Kind: "gitlab", URL: "https://git.example.internal/gitlab", GitProtocol: "https"},
			config.Repo{Path: "grp/api"},
			"https://git.example.internal/gitlab/grp/api.git",
		},
		{
			"https with prefix, port and trailing slash",
			config.Forge{Kind: "gitlab", URL: "https://git.example.internal:8443/gitlab/", GitProtocol: "https"},
			config.Repo{Path: "grp/sub/api.git"},
			"https://git.example.internal:8443/gitlab/grp/sub/api.git",
		},
		{
			"scp form without a port",
			config.Forge{Kind: "gitlab", URL: "https://git.example.internal/gitlab"},
			config.Repo{Path: "grp/api"},
			"git@git.example.internal:grp/api.git",
		},
		{
			"custom ssh port",
			config.Forge{Kind: "gitlab", URL: "https://git.example.internal", SSHPort: 2222},
			config.Repo{Path: "grp/api"},
			"ssh://git@git.example.internal:2222/grp/api.git",
		},
		{
			"custom ssh port, prefixed web url",
			config.Forge{Kind: "gitlab", URL: "https://git.example.internal:8443/gitlab", SSHPort: 2222},
			config.Repo{Path: "/grp/api"},
			"ssh://git@git.example.internal:2222/grp/api.git",
		},
		{
			"port 22 is the scp form",
			config.Forge{Kind: "gitlab", URL: "https://git.example.internal", SSHPort: 22},
			config.Repo{Path: "grp/api"},
			"git@git.example.internal:grp/api.git",
		},
		{
			"ssh port is ignored over https",
			config.Forge{Kind: "gitlab", URL: "https://git.example.internal/gitlab", GitProtocol: "https", SSHPort: 2222},
			config.Repo{Path: "grp/api"},
			"https://git.example.internal/gitlab/grp/api.git",
		},
		{
			"ip host with a port",
			config.Forge{Kind: "gitlab", URL: "https://127.0.0.1:9443/gitlab", SSHPort: 2200},
			config.Repo{Path: "grp/api"},
			"ssh://git@127.0.0.1:2200/grp/api.git",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cloneURL(tt.forge, tt.repo)
			if err != nil || got != tt.want {
				t.Errorf("cloneURL = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func writeServerCA(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Git over https needs the forge's CA through git's own configuration, scoped
// to the forge host; ssh remotes and other hosts are untouched.
func TestGitClientCarriesForgeCA(t *testing.T) {
	t.Setenv("SSL_CERT_FILE", "")
	t.Setenv("ADE_TEST_TOKEN", "secret")
	e := newEnv(t)
	e.app.Config.Forges = map[string]config.Forge{"work": {
		Kind: "gitlab", URL: "https://git.example.internal:8443/gitlab", TokenEnv: "ADE_TEST_TOKEN", CAFile: "/etc/ade/ca.pem",
	}}
	c, err := e.svc.client(context.Background(), "work", "https://git.example.internal:8443/gitlab/grp/api.git")
	if err != nil {
		t.Fatal(err)
	}
	want := "http.https://git.example.internal:8443/.sslCAInfo"
	var keys, vals []string
	for _, kv := range c.Env {
		switch {
		case strings.HasPrefix(kv, "GIT_CONFIG_KEY_"):
			keys = append(keys, kv[strings.Index(kv, "=")+1:])
		case strings.HasPrefix(kv, "GIT_CONFIG_VALUE_"):
			vals = append(vals, kv[strings.Index(kv, "=")+1:])
		}
	}
	i := slices.Index(keys, want)
	if i < 0 || vals[i] != "/etc/ade/ca.pem" {
		t.Fatalf("git env %v lacks %s=/etc/ade/ca.pem", c.Env, want)
	}
	if !slices.Contains(c.Env, "GIT_CONFIG_COUNT=3") {
		t.Errorf("env = %v: want extraHeader, insteadOf and sslCAInfo", c.Env)
	}

	ssh, err := e.svc.client(context.Background(), "work", "ssh://git@git.example.internal:2222/grp/api.git")
	if err != nil || len(ssh.Env) != 0 {
		t.Errorf("ssh remote got %v, %v: want a bare client", ssh.Env, err)
	}
}

// A manifest served over https from a GitLab with a private CA, under a path
// prefix: ca_file makes it readable, and without it the fetch fails.
func TestManifestFromHTTPSWithCA(t *testing.T) {
	e := newEnv(t)
	api := newRemote(t, "api")
	src := e.manifest("web", map[string]*remote{"api": api}, nil)
	data, _ := os.ReadFile(src)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "secret" || r.URL.Path != "/gitlab/m.toml" {
			http.NotFound(w, r)
			return
		}
		w.Write(data) //nolint:errcheck
	}))
	defer srv.Close()
	t.Setenv("ADE_TEST_TOKEN", "secret")
	forge := config.Forge{Kind: "gitlab", URL: srv.URL + "/gitlab", TokenEnv: "ADE_TEST_TOKEN"}
	e.app.Config.Forges = map[string]config.Forge{"f": forge}

	if _, err := e.svc.fetchRaw(context.Background(), srv.URL+"/gitlab/m.toml"); err == nil {
		t.Fatal("a private CA must not be trusted without ca_file")
	}
	forge.CAFile = writeServerCA(t, srv)
	e.app.Config.Forges["f"] = forge
	got, err := e.svc.fetchRaw(context.Background(), srv.URL+"/gitlab/m.toml")
	if err != nil {
		t.Fatalf("with ca_file: %v", err)
	}
	if string(got) != string(data) {
		t.Error("manifest content differs")
	}
}
