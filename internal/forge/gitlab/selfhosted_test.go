package gitlab_test

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		raw        string
		enterprise bool
		want       gitlab.Version
		wantErr    bool
	}{
		{"16.9.2-ee", false, gitlab.Version{Raw: "16.9.2-ee", Major: 16, Minor: 9, Patch: 2, Enterprise: true}, false},
		{"16.9.2", true, gitlab.Version{Raw: "16.9.2", Major: 16, Minor: 9, Patch: 2, Enterprise: true}, false},
		{"17.0.0-pre", false, gitlab.Version{Raw: "17.0.0-pre", Major: 17}, false},
		{" 15.1 ", false, gitlab.Version{Raw: "15.1", Major: 15, Minor: 1}, false},
		{"nightly", false, gitlab.Version{}, true},
		{"", false, gitlab.Version{}, true},
	}
	for _, tc := range tests {
		got, err := gitlab.ParseVersion(tc.raw, tc.enterprise)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseVersion(%q) = %+v, %v; want %+v (err %v)", tc.raw, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestVersionGating(t *testing.T) {
	names := func(v gitlab.Version) []gitlab.Feature {
		var out []gitlab.Feature
		for _, r := range v.Unsupported() {
			out = append(out, r.Feature)
		}
		return out
	}
	tests := []struct {
		raw  string
		want []gitlab.Feature
	}{
		{"17.6.0", nil},
		{"16.0.0", []gitlab.Feature{}}, // exactly at the work-items floor
		{"15.9.0", []gitlab.Feature{gitlab.FeatureWorkItems}},
		{"15.8.9", []gitlab.Feature{gitlab.FeatureBulkPublish, gitlab.FeatureWorkItems}},
		{"14.10.5", []gitlab.Feature{gitlab.FeatureBulkPublish, gitlab.FeatureCadences, gitlab.FeatureWorkItems}},
		{"13.12.0", []gitlab.Feature{gitlab.FeatureBulkPublish, gitlab.FeatureReviewers, gitlab.FeatureWorkItems, gitlab.FeatureCadences}},
	}
	for _, tc := range tests {
		v, err := gitlab.ParseVersion(tc.raw, false)
		if err != nil {
			t.Fatal(err)
		}
		got := names(v)
		slices.Sort(got)
		want := slices.Clone(tc.want)
		slices.Sort(want)
		if !slices.Equal(got, want) && (len(got) != 0 || len(want) != 0) {
			t.Errorf("Unsupported(%s) = %v, want %v", tc.raw, got, want)
		}
	}
	if (gitlab.Version{}).AtLeast(1, 0) || len((gitlab.Version{}).Unsupported()) != 0 {
		t.Error("an unknown version must neither satisfy nor fail anything")
	}
	v15, _ := gitlab.ParseVersion("15.4.2", false)
	if v15.Supports(gitlab.FeatureBulkPublish) || !v15.Supports(gitlab.FeatureCadences) || !v15.Supports("no-such-feature") {
		t.Error("Supports must follow the floors and treat unknown features as supported")
	}
}

func TestVersionFromServer(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithVersion("15.4.2", false))
	t.Cleanup(srv.Close)
	v, err := gitlab.New(srv.URL, fake.Token).Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.Major != 15 || v.Minor != 4 || v.Patch != 2 || v.Edition() != "CE" {
		t.Errorf("version = %+v", v)
	}
	if !strings.Contains(v.String(), "15.4.2 CE") {
		t.Errorf("String = %q", v.String())
	}

	ee := fake.New(fake.Premium, fake.WithVersion("16.9.0-ee", true))
	t.Cleanup(ee.Close)
	v, err = gitlab.New(ee.URL, fake.Token).Version(context.Background())
	if err != nil || !v.Enterprise {
		t.Errorf("EE version = %+v, %v", v, err)
	}
	if _, err := gitlab.New(ee.URL, "wrong").Version(context.Background()); !errors.Is(err, forge.ErrAuth) {
		t.Errorf("a bad token must be ErrAuth, got %v", err)
	}
}

// forgeFor is the config entry of a fake served under a prefix with TLS.
func forgeFor(t *testing.T, srv *fake.Server, caFile string) config.Forge {
	t.Helper()
	t.Setenv("ADE_TEST_TOKEN", fake.Token)
	return config.Forge{Kind: "gitlab", URL: srv.BaseURL(), TokenEnv: "ADE_TEST_TOKEN", CAFile: caFile}
}

func writeCA(t *testing.T, srv *fake.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, srv.CertPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A self-managed GitLab under https://host/prefix with a private CA: the CA
// file makes the API reachable, its absence is the x509 error doctor explains,
// and every request carries the prefix.
func TestOpenTrustsCAFileUnderPathPrefix(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithTLS(), fake.WithPathPrefix("/gitlab"))
	t.Cleanup(srv.Close)
	if !strings.HasSuffix(srv.BaseURL(), "/gitlab") || !strings.HasPrefix(srv.BaseURL(), "https://") {
		t.Fatalf("BaseURL = %q", srv.BaseURL())
	}
	ctx := context.Background()

	c, err := gitlab.Open(ctx, "work", forgeFor(t, srv, writeCA(t, srv)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Me(ctx); err != nil {
		t.Fatalf("Me with the CA file: %v", err)
	}
	if _, err := c.Capabilities(ctx, fake.GroupPath, ""); err != nil {
		t.Fatalf("GraphQL and REST both live under the prefix: %v", err)
	}
	for _, r := range srv.Requests() {
		if !strings.HasPrefix(r.Path, "/api/") {
			t.Errorf("the fake saw %q: the prefix is stripped before logging, so only /api/ paths may remain", r.Path)
		}
	}

	bare, err := gitlab.Open(ctx, "work", forgeFor(t, srv, ""))
	if err != nil {
		t.Fatal(err)
	}
	_, err = bare.Me(ctx)
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Fatalf("without the CA the answer must be an unknown-authority error, got %v", err)
	}
}

func TestOpenRejectsUnusableCAFile(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	bad := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := gitlab.Open(context.Background(), "work", forgeFor(t, srv, bad))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Me(context.Background()); err == nil || !strings.Contains(err.Error(), "no PEM certificates") {
		t.Fatalf("a CA file with no certificate must fail loudly on use, got %v", err)
	}
}

func TestOpenMaxConcurrency(t *testing.T) {
	t.Setenv("ADE_TEST_TOKEN", fake.Token)
	for _, tc := range []struct{ cfg, want int }{{0, 4}, {2, 2}, {1, 1}} {
		srv := fake.New(fake.Free, fake.WithLatency(30*time.Millisecond))
		c, err := gitlab.Open(context.Background(), "work", config.Forge{
			Kind: "gitlab", URL: srv.URL, TokenEnv: "ADE_TEST_TOKEN", MaxConcurrency: tc.cfg,
		})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := c.Me(context.Background()); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		got := srv.MaxInFlight()
		srv.Close()
		if got > tc.want {
			t.Errorf("max_concurrency %d: the server saw %d requests at once, want at most %d", tc.cfg, got, tc.want)
		}
		if tc.want > 1 && got < tc.want {
			t.Errorf("max_concurrency %d: only %d requests overlapped, the cap should be reachable", tc.cfg, got)
		}
	}
}

// A client that forgets the relative URL root reaches nothing; with it, all.
func TestPathPrefixIsRequired(t *testing.T) {
	srv := fake.New(fake.Free, fake.WithPathPrefix("gitlab/"))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	if _, err := gitlab.New(srv.URL, fake.Token).Me(ctx); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("without the prefix err = %v, want ErrNotFound", err)
	}
	if _, err := gitlab.New(srv.BaseURL()+"/", fake.Token).Me(ctx); err != nil {
		t.Fatalf("with the prefix (and a trailing slash): %v", err)
	}
}

// A self-managed instance without RateLimit headers still paces by
// Retry-After alone, and the answers carry no RateLimit-* headers to read.
func TestPacingWithRetryAfterOnly(t *testing.T) {
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	srv.Throttle(1, "2")
	var slept []time.Duration
	c := gitlab.New(srv.URL, fake.Token, gitlab.WithSleep(func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}))
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 1 || slept[0] != 3*time.Second {
		t.Fatalf("slept %v, want one Retry-After wait (2s + 1s slack)", slept)
	}
}

// An old instance: the routes of newer features 404, and nothing crashes. The
// driver reports each missing feature once through Unavailable.
func TestOldInstanceDegrades(t *testing.T) {
	ctx := context.Background()

	t.Run("MR reads without approvals and reviewers", func(t *testing.T) {
		srv := fake.New(fake.Free, fake.WithMissingEndpoints("/approvals", "/reviewers"))
		t.Cleanup(srv.Close)
		c := gitlab.New(srv.URL, fake.Token)
		for range 3 { // the same MR again and again: still one note per feature
			mr, err := c.MR(ctx, mrRef(apiProj, 1))
			if err != nil {
				t.Fatalf("MR must not fail when approvals and reviewers 404: %v", err)
			}
			if mr.Approvals.Required != -1 {
				t.Errorf("approvals = %+v, want the unknown marker", mr.Approvals)
			}
		}
		want := []string{"merge request approvals", "reviewer verdicts on merge requests (changes requested)"}
		if got := c.Unavailable(); !slices.Equal(got, want) {
			t.Errorf("Unavailable = %q, want %q", got, want)
		}
	})

	t.Run("a missing MR is still an error", func(t *testing.T) {
		srv := fake.New(fake.Free)
		t.Cleanup(srv.Close)
		c := gitlab.New(srv.URL, fake.Token)
		if _, err := c.MR(ctx, mrRef(apiProj, 9999)); !errors.Is(err, forge.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if len(c.Unavailable()) != 0 {
			t.Errorf("a missing MR is not a missing feature: %v", c.Unavailable())
		}
	})

	t.Run("capabilities without GraphQL", func(t *testing.T) {
		srv := fake.New(fake.Premium, fake.WithMissingEndpoints("/api/graphql"))
		t.Cleanup(srv.Close)
		c := gitlab.New(srv.URL, fake.Token)
		caps, err := c.Capabilities(ctx, fake.GroupPath, "")
		if err != nil {
			t.Fatalf("Capabilities must survive an instance with no GraphQL: %v", err)
		}
		if caps.Cadences || caps.Epics {
			t.Errorf("caps = %+v: GraphQL features cannot be on", caps)
		}
		if !caps.Iterations {
			t.Errorf("the REST side is unaffected, iterations must still probe on a Premium fake: %+v", caps)
		}
		if got := c.Unavailable(); len(got) != 2 {
			t.Errorf("Unavailable = %q, want cadences and work items", got)
		}
		if _, err := c.Cadences(ctx, fake.GroupPath); !errors.Is(err, forge.ErrUnavailable) {
			t.Errorf("Cadences err = %v, want ErrUnavailable", err)
		}
	})

	t.Run("a GraphQL schema without the fields", func(t *testing.T) {
		// An older release answers 200 with validation errors and no data.
		srv := fake.New(fake.Premium)
		t.Cleanup(srv.Close)
		inner := srv.Config.Handler
		old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/graphql" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"errors":[{"message":"Field 'workItemTypes' doesn't exist on type 'Group'"}]}`))
				return
			}
			inner.ServeHTTP(w, r)
		}))
		t.Cleanup(old.Close)
		c := gitlab.New(old.URL, fake.Token)
		caps, err := c.Capabilities(ctx, fake.GroupPath, "")
		if err != nil || caps.Epics || caps.Cadences {
			t.Fatalf("caps = %+v, err = %v: want GraphQL features off and no error", caps, err)
		}
	})

	t.Run("bulk_publish on an old release", func(t *testing.T) {
		srv := fake.New(fake.Free, fake.WithMissingEndpoints("/bulk_publish"))
		t.Cleanup(srv.Close)
		c := gitlab.New(srv.URL, fake.Token)
		err := c.PublishDraftNotes(ctx, mrRef(apiProj, 1))
		if !errors.Is(err, forge.ErrUnavailable) || !strings.Contains(err.Error(), "bulk_publish") {
			t.Fatalf("err = %v, want ErrUnavailable naming bulk_publish", err)
		}
		if got := c.Unavailable(); len(got) != 1 || !strings.Contains(got[0], "bulk_publish") {
			t.Errorf("Unavailable = %q", got)
		}
	})
}
