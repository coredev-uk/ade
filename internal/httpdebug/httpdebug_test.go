package httpdebug

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	Enable(&buf)
	t.Cleanup(Disable)
	return &buf
}

func TestLogsRequestAndResponseWithoutSecretsOrBodies(t *testing.T) {
	buf := capture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		io.WriteString(w, `{"message":"slow down","echo":"glpat-aaaaaaaaaaaaaaaaaaaa"}`)
	}))
	defer srv.Close()
	hc := &http.Client{Transport: Transport(nil)}

	req, _ := http.NewRequest("POST", srv.URL+"/api/v4/issues?state=opened&private_token=glpat-supersecretvalue123456&access_token=zzz", strings.NewReader(`{"title":"body-must-not-appear"}`))
	req.Header.Set("PRIVATE-TOKEN", "glpat-supersecretvalue123456")
	req.Header.Set("Authorization", "Bearer sk-aaaaaaaaaaaaaaaaaaaaaaaaaa")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "slow down") {
		t.Fatalf("the wrapper must pass the body through untouched, got %q", body)
	}

	got := buf.String()
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 2 {
		t.Fatalf("want a request and a response line, got:\n%s", got)
	}
	if !strings.HasPrefix(lines[0], "http → POST "+srv.URL+"/api/v4/issues?") || !strings.Contains(lines[0], "state=opened") {
		t.Errorf("request line %q", lines[0])
	}
	if !strings.Contains(lines[0], "Authorization") || !strings.Contains(lines[0], "Private-Token") || !strings.Contains(lines[0], "[redacted]") {
		t.Errorf("a credential header must be reported as present and redacted: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "http ← 429 POST ") || !strings.Contains(lines[1], "retry-after 7") {
		t.Errorf("response line %q", lines[1])
	}
	for _, secret := range []string{"supersecretvalue", "sk-aaaa", "zzz", "body-must-not-appear", "slow down", "glpat-aaaa"} {
		if strings.Contains(got, secret) {
			t.Errorf("log leaks %q:\n%s", secret, got)
		}
	}
}

func TestDisabledLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	Enable(&buf)
	Disable()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	if _, err := (&http.Client{Transport: Transport(nil)}).Get(srv.URL); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("logged while disabled: %q", buf.String())
	}
}

func TestTransportErrorsAreLoggedAndMasked(t *testing.T) {
	buf := capture(t)
	rt := Transport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(`Get "https://h.example/x?api_key=SECRETVALUE&page=2": dial tcp: refused`)
	}))
	req, _ := http.NewRequest("GET", "https://user:pw@h.example/x?api_key=SECRETVALUE&page=2", nil)
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("error must still be returned")
	}
	got := buf.String()
	if !strings.Contains(got, "http ✗ GET https://h.example/x") || !strings.Contains(got, "page=2") {
		t.Errorf("log:\n%s", got)
	}
	for _, secret := range []string{"SECRETVALUE", "user:pw", "pw@"} {
		if strings.Contains(got, secret) {
			t.Errorf("log leaks %q:\n%s", secret, got)
		}
	}
}

func TestMaskURL(t *testing.T) {
	u, _ := url.Parse("https://oauth2:tok@gitlab.example.com/api/v4/projects?search=a&token=x&PRIVATE_KEY=y&per_page=100")
	got := MaskURL(u)
	for _, want := range []string{"gitlab.example.com/api/v4/projects", "search=a", "per_page=100", "token=%5Bredacted%5D", "PRIVATE_KEY=%5Bredacted%5D"} {
		if !strings.Contains(got, want) {
			t.Errorf("MaskURL = %q, lacks %q", got, want)
		}
	}
	if strings.Contains(got, "oauth2") || strings.Contains(got, "tok@") {
		t.Errorf("userinfo leaked: %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
