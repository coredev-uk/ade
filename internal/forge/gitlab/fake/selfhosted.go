package fake

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
)

// selfHosted holds what makes the fake behave like a self-managed instance:
// its release, a TLS certificate from a private authority, a relative URL root
// and endpoints an older release does not have.
type selfHosted struct {
	version    string
	enterprise bool
	tls        bool
	prefix     string
	absent     []string
}

// WithVersion sets what GET /api/v4/version reports (for example "15.4.2"). A
// "-ee" suffix, or enterprise, marks the Enterprise Edition.
func WithVersion(version string, enterprise bool) Option {
	return func(o *options) { o.version, o.enterprise = version, enterprise }
}

// WithTLS serves HTTPS with httptest's self-signed certificate. Trust it with
// CertPEM; BaseURL is then the https:// address.
func WithTLS() Option { return func(o *options) { o.tls = true } }

// WithPathPrefix serves the instance under a relative URL root, like a GitLab
// installed at https://host/gitlab (WithPathPrefix("/gitlab")). Requests
// outside the prefix answer 404. BaseURL includes it; URL does not.
func WithPathPrefix(prefix string) Option {
	return func(o *options) { o.prefix = "/" + strings.Trim(prefix, "/") }
}

// WithMissingEndpoints makes every request whose path contains one of the
// fragments answer 404 Not Found, the way an older release lacks a route
// ("/reviewers", "/approvals", "/bulk_publish", "/api/graphql").
func WithMissingEndpoints(fragments ...string) Option {
	return func(o *options) { o.absent = append(o.absent, fragments...) }
}

// missing reports whether the request targets an endpoint this release lacks.
func (s selfHosted) missing(r *http.Request) bool {
	for _, f := range s.absent {
		if strings.Contains(r.URL.Path, f) {
			return true
		}
	}
	return false
}

// start builds the httptest server: plain or TLS, under the prefix.
func (s *Server) start() *httptest.Server {
	h := s.gitHTTP(s.routes())
	if p := s.opts.prefix; p != "" {
		h = http.StripPrefix(p, h)
	}
	if s.opts.tls {
		return httptest.NewTLSServer(h)
	}
	return httptest.NewServer(h)
}

// BaseURL is the address clients use: URL plus any WithPathPrefix.
func (s *Server) BaseURL() string { return s.URL + s.opts.prefix }

// CertPEM is the server certificate as a PEM bundle, for a ca_file; nil
// without WithTLS.
func (s *Server) CertPEM() []byte {
	if !s.opts.tls {
		return nil
	}
	return certPEM(s.Certificate())
}

func certPEM(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v4/version", func(w http.ResponseWriter, _ *http.Request) {
			version, ee := s.opts.version, s.opts.enterprise
			if version == "" {
				version, ee = "17.6.0-ee", true
			}
			writeJSON(w, http.StatusOK, map[string]any{"version": version, "revision": "fake", "enterprise": ee})
		})
	})
}
