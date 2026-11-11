// Package httpdebug implements `ade --debug-http` (PLAN 4.3): a transport
// wrapper that writes one request line and one response line per HTTP call to
// stderr. It logs the method, the URL with credentials masked, the status and
// the timing; it never logs bodies, and it never prints a credential header,
// only that one was sent. Every HTTP client ADE builds for a forge or an AI
// driver uses Transport, and the wrapper costs one atomic load while the
// flag is off.
package httpdebug

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
)

var (
	enabled atomic.Bool
	mu      sync.Mutex // serialises lines from concurrent requests
	out     io.Writer  = os.Stderr
)

// Enable turns request logging on, writing to w (stderr when nil).
func Enable(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	if w == nil {
		w = os.Stderr
	}
	out = w
	enabled.Store(true)
}

// Disable turns request logging off.
func Disable() { enabled.Store(false) }

// Enabled reports whether requests are being logged.
func Enabled() bool { return enabled.Load() }

// Transport wraps base (http.DefaultTransport when nil) so that requests are
// logged while debugging is enabled.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &transport{base: base}
}

type transport struct{ base http.RoundTripper }

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !enabled.Load() {
		return t.base.RoundTrip(req)
	}
	target := MaskURL(req.URL)
	line := fmt.Sprintf("http → %s %s", req.Method, target)
	if h := credentialHeaders(req.Header); len(h) > 0 {
		line += "  (" + strings.Join(h, ", ") + ": [redacted])"
	}
	write(line)
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	took := time.Since(start).Round(time.Millisecond)
	if err != nil {
		write(fmt.Sprintf("http ✗ %s %s  %s  %s", req.Method, target, took, audit.Redact(MaskText(err.Error()))))
		return resp, err
	}
	line = fmt.Sprintf("http ← %d %s %s  %s", resp.StatusCode, req.Method, target, took)
	if resp.ContentLength >= 0 {
		line += fmt.Sprintf("  %d bytes", resp.ContentLength)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		line += "  retry-after " + ra
	}
	write(line)
	return resp, nil
}

func write(line string) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprintln(out, line)
}

// credentialHeaders names the credential headers a request carries.
func credentialHeaders(h http.Header) []string {
	var names []string
	for _, k := range []string{"Authorization", "Proxy-Authorization", "Private-Token", "Job-Token", "X-Api-Key", "X-Goog-Api-Key", "Cookie"} {
		if h.Get(k) != "" {
			names = append(names, k)
		}
	}
	return names
}

var secretKey = regexp.MustCompile(`(?i)token|key|secret|passw|auth|signature|credential|^sig$|^code$`)

// MaskURL renders u without userinfo and with the value of every
// credential-looking query parameter masked. The path is kept (it is what you
// debug), minus any token-shaped segment.
func MaskURL(u *url.URL) string {
	c := *u
	c.User = nil
	if c.RawQuery != "" {
		q := c.Query()
		for k := range q {
			if secretKey.MatchString(k) {
				q.Set(k, "[redacted]")
			}
		}
		c.RawQuery = q.Encode()
	}
	return audit.Redact(c.String())
}

// MaskText masks credential-looking query values inside free text such as the
// URL Go quotes in a transport error (`Get "https://h/p?token=x": dial ...`).
func MaskText(s string) string {
	return queryValue.ReplaceAllStringFunc(s, func(m string) string {
		k, _, _ := strings.Cut(m, "=")
		if secretKey.MatchString(strings.TrimLeft(k, "?&")) {
			return k + "=[redacted]"
		}
		return m
	})
}

var queryValue = regexp.MustCompile(`[?&][A-Za-z0-9_.-]+=[^&"\s]*`)
