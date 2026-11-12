package doctor

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/httpdebug"
	"github.com/coredev-uk/ade/internal/tlsca"
)

const expiryWarning = 14 * 24 * time.Hour

func forgeChecks(o Options) []func(context.Context) []Check {
	if o.Config == nil {
		return nil
	}
	var fns []func(context.Context) []Check
	for _, name := range slices.Sorted(maps.Keys(o.Config.Forges)) {
		f := o.Config.Forges[name]
		fns = append(fns, func(ctx context.Context) []Check { return checkForge(ctx, o, name, f) })
	}
	return fns
}

func checkForge(ctx context.Context, o Options, name string, f config.Forge) []Check {
	tokenCheck := Check{Group: "forge", Name: name + " token"}
	tctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	tok, err := f.Token(tctx)
	if err != nil {
		tokenCheck.Status, tokenCheck.Detail = Error, err.Error()
		tokenCheck.Fix = "set token_env or token_cmd under [forges." + name + "] so it yields a token"
		return []Check{tokenCheck}
	}
	tokenCheck.Status, tokenCheck.Detail = OK, "resolved via "+tokenSource(f)
	checks := []Check{tokenCheck}

	hc := o.HTTP
	if f.CAFile != "" {
		ca := Check{Group: "forge", Name: name + " ca_file"}
		tr, err := tlsca.Transport(f.CAFile)
		if err != nil {
			ca.Status, ca.Detail = Error, err.Error()
			ca.Fix = "point ca_file under [forges." + name + "] at a readable PEM bundle with the CA certificate(s)"
			return append(checks, ca)
		}
		ca.Status, ca.Detail = OK, f.CAFile+" is trusted on top of the system certificates (API and git over https)"
		checks = append(checks, ca)
		hc = &http.Client{Timeout: o.Timeout, Transport: httpdebug.Transport(tr)}
	}

	auth := Check{Group: "forge", Name: name + " auth"}
	actx, acancel := context.WithTimeout(ctx, o.Timeout)
	defer acancel()
	switch f.Kind {
	case "gitlab":
		auth = gitlabAuth(actx, hc, auth, f, tok)
	case "github":
		auth = githubAuth(actx, hc, auth, f, tok)
	default:
		auth.Status, auth.Detail, auth.Fix = Error, fmt.Sprintf("unknown forge kind %q", f.Kind), "set kind to gitlab or github"
	}
	checks = append(checks, auth)
	if f.Kind == "gitlab" && auth.Status != Error {
		vctx, vcancel := context.WithTimeout(ctx, o.Timeout)
		defer vcancel()
		checks = append(checks, gitlabVersion(vctx, hc, name, f, tok))
	}
	return checks
}

// gitlabVersion reports the instance's release and edition (GET
// /api/v4/version) and warns, per feature, where it is older than the code
// paths ADE has for it. The older instance still works: those features report
// "not available on this GitLab" and sync carries on.
func gitlabVersion(ctx context.Context, hc *http.Client, name string, f config.Forge, tok string) Check {
	c := Check{Group: "forge", Name: name + " version"}
	base := strings.TrimRight(f.URL, "/")
	if base == "" {
		base = gitlab.DefaultURL
	}
	r, err := get(ctx, hc, base+"/api/v4/version", map[string]string{"PRIVATE-TOKEN": tok})
	switch {
	case err != nil:
		return failed(c, base, err)
	case r.status != http.StatusOK:
		c.Status = Warn
		c.Detail = fmt.Sprintf("%s/api/v4/version answered HTTP %d: the release is unknown, so feature gating is off", base, r.status)
		return c
	}
	var w struct {
		Version    string `json:"version"`
		Enterprise bool   `json:"enterprise"`
	}
	if err := json.Unmarshal(r.body, &w); err != nil {
		c.Status, c.Detail = Warn, "version answer is not JSON: "+err.Error()
		return c
	}
	v, err := gitlab.ParseVersion(w.Version, w.Enterprise)
	if err != nil {
		c.Status, c.Detail = Warn, err.Error()
		return c
	}
	c.Status, c.Detail = OK, v.String()
	if missing := v.Unsupported(); len(missing) > 0 {
		parts := make([]string, len(missing))
		for i, m := range missing {
			parts[i] = fmt.Sprintf("%s needs %d.%d+", m.Name, m.Major, m.Minor)
		}
		c.Status = Warn
		c.Detail += ": older than ADE expects; " + strings.Join(parts, "; ")
		c.Fix = "upgrade GitLab for those features; ADE reports each as not available on this GitLab and carries on"
	}
	return c
}

func tokenSource(f config.Forge) string {
	if f.TokenEnv != "" {
		return "$" + f.TokenEnv
	}
	return "token_cmd"
}

type response struct {
	status int
	body   []byte
	header http.Header
}

// get never includes the token in errors: net/http only reports the URL.
func get(ctx context.Context, c *http.Client, url string, headers map[string]string) (response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return response{}, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", "ade-doctor")
	resp, err := c.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return response{resp.StatusCode, body, resp.Header}, err
}

func gitlabAuth(ctx context.Context, hc *http.Client, c Check, f config.Forge, tok string) Check {
	base := strings.TrimRight(f.URL, "/")
	if base == "" {
		base = "https://gitlab.com"
	}
	hdr := map[string]string{"PRIVATE-TOKEN": tok}
	r, err := get(ctx, hc, base+"/api/v4/personal_access_tokens/self", hdr)
	if err != nil {
		return failed(c, base, err)
	}
	switch r.status {
	case http.StatusOK:
		return gitlabPAT(c, r.body)
	case http.StatusUnauthorized, http.StatusNotFound:
		// Project/group/OAuth tokens have no "self" record: fall back to /user.
		return gitlabUser(ctx, hc, c, base, hdr)
	}
	return unexpected(c, base, r.status)
}

func gitlabPAT(c Check, body []byte) Check {
	var pat struct {
		Scopes    []string `json:"scopes"`
		Active    bool     `json:"active"`
		Revoked   bool     `json:"revoked"`
		ExpiresAt string   `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &pat); err != nil {
		c.Status, c.Detail = Error, "unreadable response: "+err.Error()
		return c
	}
	c.Status, c.Detail = gitlabScopeStatus(pat.Scopes, &c.Fix)
	c.Detail = "scopes: " + strings.Join(pat.Scopes, ", ") + c.Detail
	if pat.Revoked || !pat.Active {
		c.Status, c.Fix = Error, "the token is revoked or inactive: create a new personal access token"
		return c
	}
	if exp, err := time.Parse("2006-01-02", pat.ExpiresAt); err == nil {
		left := time.Until(exp)
		switch {
		case left < 0:
			c.Status, c.Fix = Error, "the token expired on "+pat.ExpiresAt+": create a new one"
		case left < expiryWarning && c.Status == OK:
			c.Status, c.Fix = Warn, "the token expires on "+pat.ExpiresAt+": rotate it soon"
		}
	}
	return c
}

// gitlabScopeStatus judges PAT scopes: api allows writes; read_api plus
// read_user is the minimum for read-only use. fix is set when not OK.
func gitlabScopeStatus(scopes []string, fix *string) (Status, string) {
	has := func(s string) bool { return slices.Contains(scopes, s) }
	switch {
	case has("api"):
		return OK, ""
	case has("read_api") && has("read_user"):
		*fix = "read-only token: ADE cannot create issues, branches or merge requests; grant the api scope for writes"
		return Warn, " (read-only)"
	}
	var missing []string
	for _, s := range []string{"read_api", "read_user"} {
		if !has(s) {
			missing = append(missing, s)
		}
	}
	*fix = "grant the api scope (or at least " + strings.Join(missing, " and ") + ")"
	return Error, " (missing " + strings.Join(missing, ", ") + ")"
}

func gitlabUser(ctx context.Context, hc *http.Client, c Check, base string, hdr map[string]string) Check {
	r, err := get(ctx, hc, base+"/api/v4/user", hdr)
	if err != nil {
		return failed(c, base, err)
	}
	switch r.status {
	case http.StatusOK:
		var u struct {
			Username string `json:"username"`
		}
		_ = json.Unmarshal(r.body, &u)
		c.Status = OK
		c.Detail = "authenticated as " + u.Username + " (not a personal access token: scopes cannot be inspected)"
		return c
	case http.StatusUnauthorized:
		c.Status, c.Detail = Error, "token rejected by "+base+" (HTTP 401)"
		c.Fix = "create a new token with the api scope and update the forge's token source"
		return c
	}
	return unexpected(c, base, r.status)
}

func githubAuth(ctx context.Context, hc *http.Client, c Check, f config.Forge, tok string) Check {
	base := strings.TrimRight(f.URL, "/")
	endpoint := "https://api.github.com/user"
	if base != "" && base != "https://github.com" {
		endpoint = base + "/api/v3/user" // GitHub Enterprise Server
	}
	r, err := get(ctx, hc, endpoint, map[string]string{
		"Authorization":        "Bearer " + tok,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	})
	if err != nil {
		return failed(c, endpoint, err)
	}
	if r.status == http.StatusUnauthorized {
		c.Status, c.Detail = Error, "token rejected by GitHub (HTTP 401)"
		c.Fix = "refresh the token (e.g. `gh auth login`) or create one with the repo scope"
		return c
	}
	if r.status != http.StatusOK {
		return unexpected(c, endpoint, r.status)
	}
	var u struct {
		Login string `json:"login"`
	}
	_ = json.Unmarshal(r.body, &u)
	c.Status = OK
	c.Detail = "authenticated as " + u.Login
	// Fine-grained and app tokens send no X-OAuth-Scopes header at all.
	raw, classic := r.header["X-Oauth-Scopes"]
	if !classic {
		c.Detail += "; scopes not reported (fine-grained token)"
		return c
	}
	scopes := splitScopes(strings.Join(raw, ","))
	c.Detail += "; scopes: " + joinOrNone(scopes)
	if !slices.Contains(scopes, "repo") {
		c.Status = Warn
		c.Fix = "without the repo scope ADE can only read public data; add repo for writes"
	}
	return c
}

func splitScopes(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func joinOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

func failed(c Check, target string, err error) Check {
	c.Status, c.Detail = Error, fmt.Sprintf("cannot reach %s: %v", target, err)
	c.Fix = "check network access and the forge url"
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var host x509.HostnameError
	if errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &host) {
		c.Fix = "the forge's certificate is not trusted: set ca_file under its [forges.<name>] to your CA's PEM bundle, or export SSL_CERT_FILE"
	}
	return c
}

func unexpected(c Check, target string, status int) Check {
	c.Status, c.Detail = Error, fmt.Sprintf("%s answered HTTP %d", target, status)
	c.Fix = "check the forge url and that the token is valid"
	return c
}
