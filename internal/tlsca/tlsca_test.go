package tlsca

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tlsServer serves HTTPS with a private certificate and returns its PEM file.
func tlsServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return srv, path
}

func get(rt http.RoundTripper, url string) error {
	c := &http.Client{Transport: rt}
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func TestTransportTrustsCAFileOnTopOfSystemPool(t *testing.T) {
	srv, ca := tlsServer(t)

	if err := get(http.DefaultTransport, srv.URL); err == nil {
		t.Fatal("the system pool must not trust the private CA")
	}
	var unknown x509.UnknownAuthorityError
	if err := get(http.DefaultTransport, srv.URL); !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want an unknown-authority error", err)
	}

	tr, err := Transport(ca)
	if err != nil {
		t.Fatal(err)
	}
	if err := get(tr, srv.URL); err != nil {
		t.Fatalf("with ca_file: %v", err)
	}

	if tr, err := Transport(""); tr != nil || err != nil {
		t.Errorf("no ca_file means the default transport, got %v, %v", tr, err)
	}
}

func TestPoolErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Pool(filepath.Join(dir, "missing.pem")); err == nil || !strings.Contains(err.Error(), "ca_file") {
		t.Errorf("a missing file must name ca_file, got %v", err)
	}
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("-----BEGIN NOTHING-----"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Pool(junk); err == nil || !strings.Contains(err.Error(), "no PEM certificates") {
		t.Errorf("a file without certificates must be refused, got %v", err)
	}
	if p, err := Pool(""); err != nil || p == nil {
		t.Errorf("Pool(\"\") = %v, %v", p, err)
	}
}

func TestGitSettings(t *testing.T) {
	t.Setenv("SSL_CERT_FILE", "")
	tests := []struct {
		name, url, ca, env string
		want               [][2]string
	}{
		{
			"scoped to the forge host", "https://git.example.internal/acme/api.git", "/etc/ca.pem", "",
			[][2]string{{"http.https://git.example.internal/.sslCAInfo", "/etc/ca.pem"}},
		},
		{
			"keeps the port and drops the path prefix", "https://127.0.0.1:8443/gitlab/acme/api.git", "/etc/ca.pem", "",
			[][2]string{{"http.https://127.0.0.1:8443/.sslCAInfo", "/etc/ca.pem"}},
		},
		{
			"ca_file wins over SSL_CERT_FILE", "https://h/x.git", "/a.pem", "/b.pem",
			[][2]string{{"http.https://h/.sslCAInfo", "/a.pem"}},
		},
		{"SSL_CERT_FILE alone", "https://h/x.git", "", "/b.pem", [][2]string{{"http.https://h/.sslCAInfo", "/b.pem"}}},
		{"nothing to trust", "https://h/x.git", "", "", nil},
		{"ssh remotes have no TLS", "ssh://git@h:2222/x.git", "/a.pem", "", nil},
		{"scp remotes have no TLS", "git@h:x.git", "/a.pem", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SSL_CERT_FILE", tc.env)
			got := GitSettings(tc.url, tc.ca)
			if !slices.Equal(got, tc.want) {
				t.Errorf("GitSettings = %v, want %v", got, tc.want)
			}
		})
	}
}

// Go reads SSL_CERT_FILE once, when the system pool is first built, so the
// check runs in a fresh process: the test binary re-executes itself.
func TestSSLCertFileIsHonoured(t *testing.T) {
	if url := os.Getenv("ADE_TLSCA_HELPER_URL"); url != "" {
		if err := get(http.DefaultTransport, url); err != nil {
			t.Fatalf("SSL_CERT_FILE not honoured: %v", err)
		}
		return
	}
	srv, ca := tlsServer(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestSSLCertFileIsHonoured$")
	cmd.Env = append(os.Environ(), "ADE_TLSCA_HELPER_URL="+srv.URL, "SSL_CERT_FILE="+ca)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper failed: %v\n%s", err, out)
	}
}
