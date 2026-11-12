// Package tlsca builds the trust configuration for a self-hosted forge that
// uses an internal certificate authority: `[forges.x] ca_file` is a PEM bundle
// added to the system pool for ADE's own API calls, and handed to git as
// http.<host>.sslCAInfo for clones and fetches.
//
// Go already honours SSL_CERT_FILE and SSL_CERT_DIR when it loads the system
// pool (Linux), so exporting SSL_CERT_FILE works for the API client without
// any setting; GitEnv forwards it to git, which does not read it.
package tlsca

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Pool returns the system pool (which honours SSL_CERT_FILE) extended with the
// PEM certificates in caFile. An empty caFile returns the system pool alone.
// A file without a single certificate is an error: silently trusting nothing
// extra would surface later as an opaque "unknown authority".
func Pool(caFile string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if caFile == "" {
		return pool, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("ca_file: %w", err)
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("ca_file %s: no PEM certificates found", caFile)
	}
	return pool, nil
}

// Transport returns an HTTP transport that trusts caFile on top of the system
// pool, or nil (use the default transport) when caFile is empty.
func Transport(caFile string) (http.RoundTripper, error) {
	if caFile == "" {
		return nil, nil
	}
	pool, err := Pool(caFile)
	if err != nil {
		return nil, err
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("tlsca: http.DefaultTransport is not an *http.Transport")
	}
	tr := base.Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return tr, nil
}

// GitSettings returns the git config pairs (key, value) that make git trust
// caFile for remoteURL's host, or SSL_CERT_FILE when caFile is empty. The
// setting is scoped to the host (http.<scheme://host[:port]/>.sslCAInfo) so a
// private CA bundle never replaces trust for other hosts. Nothing is returned
// for non-https URLs or when no bundle applies.
func GitSettings(remoteURL, caFile string) [][2]string {
	if caFile == "" {
		caFile = strings.TrimSpace(os.Getenv("SSL_CERT_FILE"))
	}
	if caFile == "" {
		return nil
	}
	u, err := url.Parse(remoteURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil
	}
	return [][2]string{{"http." + u.Scheme + "://" + u.Host + "/.sslCAInfo", caFile}}
}
