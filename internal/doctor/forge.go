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

// tokenRole says which of a forge's credentials a check judges: the one that
// writes (token_env/token_cmd) or the one that only reads (read_token_*).
type tokenRole int

const (
	writeRole tokenRole = iota
	readRole
)

func checkForge(ctx context.Context, o Options, name string, f config.Forge) []Check {
	var checks []Check
	var writeTok string
	tokenCheck := Check{Group: "forge", Name: name + " token"}
	if f.HasReadToken() && !f.HasWriteToken() {
		tokenCheck.Status, tokenCheck.Detail = Warn, "no write token: ADE is read-only on this forge"
		tokenCheck.Fix = "set token_env or token_cmd under [forges." + name + "] to let ADE create issues and merge requests and push"
		checks = append(checks, tokenCheck)
	} else {
		tctx, cancel := context.WithTimeout(ctx, o.Timeout)
		defer cancel()
		tok, err := f.Token(tctx)
		if err != nil {
			tokenCheck.Status, tokenCheck.Detail = Error, err.Error()
			tokenCheck.Fix = "set token_env or token_cmd under [forges." + name + "] so it yields a token"
			return []Check{tokenCheck}
		}
		writeTok = tok
		tokenCheck.Status, tokenCheck.Detail = OK, "resolved via "+tokenSource(f)
		checks = append(checks, tokenCheck)
	}

	readTok := writeTok
	if f.HasReadToken() {
		readCheck := Check{Group: "forge", Name: name + " read token"}
		rctx, cancel := context.WithTimeout(ctx, o.Timeout)
		defer cancel()
		tok, err := f.ReadToken(rctx)
		if err != nil {
			readCheck.Status, readCheck.Detail = Error, err.Error()
			readCheck.Fix = "set read_token_env or read_token_cmd under [forges." + name + "] so it yields a token"
			return append(checks, readCheck)
		}
		readTok = tok
		readCheck.Status, readCheck.Detail = OK, "resolved via "+readTokenSource(f)
		checks = append(checks, readCheck)
	}

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

	var writeAuth, readAuth Check
	if f.HasWriteToken() {
		writeAuth = forgeAuth(ctx, o, hc, Check{Group: "forge", Name: name + " auth"}, f, writeTok, writeRole)
		checks = append(checks, writeAuth)
	}
	usedAuth := writeAuth
	if f.HasReadToken() {
		readAuth = forgeAuth(ctx, o, hc, Check{Group: "forge", Name: name + " read auth"}, f, readTok, readRole)
		checks = append(checks, readAuth)
		usedAuth = readAuth
	}
	if f.Kind == "gitlab" && usedAuth.Status != Error {
		vctx, vcancel := context.WithTimeout(ctx, o.Timeout)
		defer vcancel()
		checks = append(checks, gitlabVersion(vctx, hc, name, f, readTok))
	}
	if f.HasReadToken() {
		if f.HasWriteToken() && writeAuth.Status != Error && readAuth.Status != Error {
			uctx, ucancel := context.WithTimeout(ctx, o.Timeout)
			defer ucancel()
			if mismatch := differentUsers(uctx, hc, f, readTok, writeTok); mismatch != "" {
				for i := range checks {
					if checks[i].Name == readAuth.Name {
						checks[i].Status, checks[i].Detail = Error, mismatch
						checks[i].Fix = "create the read and write tokens for the same account: ADE takes \"me\", assignments and your own actions from the token"
					}
				}
			}
		}
		if agents, ok := agentsCheck(name, f); ok {
			checks = append(checks, agents)
		}
	}
	return checks
}

// forgeAuth asks the forge what tok may do, within its own timeout.
func forgeAuth(ctx context.Context, o Options, hc *http.Client, c Check, f config.Forge, tok string, role tokenRole) Check {
	actx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	switch f.Kind {
	case "gitlab":
		return gitlabAuth(actx, hc, c, f, tok, role)
	case "github":
		return githubAuth(actx, hc, c, f, tok, role)
	}
	c.Status, c.Detail, c.Fix = Error, fmt.Sprintf("unknown forge kind %q", f.Kind), "set kind to gitlab or github"
	return c
}

// differentUsers returns the check detail when the read and write tokens
// authenticate different accounts, and "" when they match or either account
// cannot be told (the auth checks report why).
func differentUsers(ctx context.Context, hc *http.Client, f config.Forge, readTok, writeTok string) string {
	reader, writer := forgeUser(ctx, hc, f, readTok), forgeUser(ctx, hc, f, writeTok)
	if reader == "" || writer == "" || strings.EqualFold(reader, writer) {
		return ""
	}
	return fmt.Sprintf("read token is %s, write token is %s", reader, writer)
}

// forgeUser is the account tok authenticates as, or "" when the forge does
// not say.
func forgeUser(ctx context.Context, hc *http.Client, f config.Forge, tok string) string {
	var url, field string
	var hdr map[string]string
	switch f.Kind {
	case "gitlab":
		url, field, hdr = gitlabBase(f)+"/api/v4/user", "username", map[string]string{"PRIVATE-TOKEN": tok}
	case "github":
		url, field, hdr = githubUserEndpoint(f), "login", githubHeaders(tok)
	default:
		return ""
	}
	r, err := get(ctx, hc, url, hdr)
	if err != nil || r.status != http.StatusOK {
		return ""
	}
	var u map[string]any
	if json.Unmarshal(r.body, &u) != nil {
		return ""
	}
	name, _ := u[field].(string)
	return name
}

// agentsCheck reports whether the agents ADE launches can reach the write
// token. ok is false when the forge has no write token to hide.
func agentsCheck(name string, f config.Forge) (Check, bool) {
	c := Check{Group: "forge", Name: name + " agents", Status: OK}
	switch {
	case !f.HasWriteToken():
		return c, false
	case f.TokenEnv != "" && f.TokenEnv == f.ReadTokenEnv:
		c.Status, c.Detail = Warn, "token_env and read_token_env name the same variable $"+f.TokenEnv+": agents inherit it"
		c.Fix = "give the write token its own variable under [forges." + name + "]"
	case f.TokenCmd != "":
		c.Detail = "agents run as you and can run token_cmd themselves; keep the write token in a source agents cannot reach if that matters"
	default:
		c.Detail = "agents ADE launches do not inherit $" + f.TokenEnv
	}
	return c, true
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

func readTokenSource(f config.Forge) string {
	if f.ReadTokenEnv != "" {
		return "$" + f.ReadTokenEnv
	}
	return "read_token_cmd"
}

func gitlabBase(f config.Forge) string {
	if base := strings.TrimRight(f.URL, "/"); base != "" {
		return base
	}
	return "https://gitlab.com"
}

// githubUserEndpoint is the /user URL: api.github.com, or /api/v3 on GitHub
// Enterprise Server.
func githubUserEndpoint(f config.Forge) string {
	if base := strings.TrimRight(f.URL, "/"); base != "" && base != "https://github.com" {
		return base + "/api/v3/user"
	}
	return "https://api.github.com/user"
}

func githubHeaders(tok string) map[string]string {
	return map[string]string{
		"Authorization":        "Bearer " + tok,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
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

func gitlabAuth(ctx context.Context, hc *http.Client, c Check, f config.Forge, tok string, role tokenRole) Check {
	base := gitlabBase(f)
	hdr := map[string]string{"PRIVATE-TOKEN": tok}
	r, err := get(ctx, hc, base+"/api/v4/personal_access_tokens/self", hdr)
	if err != nil {
		return failed(c, base, err)
	}
	switch r.status {
	case http.StatusOK:
		return gitlabPAT(c, r.body, f, role)
	case http.StatusUnauthorized, http.StatusNotFound:
		// Project/group/OAuth tokens have no "self" record: fall back to /user.
		return gitlabUser(ctx, hc, c, base, hdr, role)
	}
	return unexpected(c, base, r.status)
}

func gitlabPAT(c Check, body []byte, f config.Forge, role tokenRole) Check {
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
	c.Status, c.Detail = gitlabScopeStatus(pat.Scopes, role, f.GitProtocol == "https", &c.Fix)
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

// gitlabScopeStatus judges PAT scopes. A write token needs api (read_api plus
// read_user is the minimum for read-only use); a read token should have no api
// and, for git over https, read_repository. fix is set when not OK.
func gitlabScopeStatus(scopes []string, role tokenRole, httpsGit bool, fix *string) (Status, string) {
	has := func(s string) bool { return slices.Contains(scopes, s) }
	switch {
	case role == writeRole && has("api"):
		return OK, ""
	case role == writeRole && has("read_api") && has("read_user"):
		*fix = "read-only token: ADE cannot create issues, branches or merge requests; grant the api scope for writes"
		return Warn, " (read-only)"
	case role == readRole && has("api"):
		*fix = "the read token can write: create one with read_api, read_user (and read_repository for git over https) so reads and agents cannot write"
		return Warn, " (can write)"
	case role == readRole && has("read_api") && has("read_user"):
		if httpsGit && !has("read_repository") {
			*fix = "git_protocol is https: add the read_repository scope so clones and fetches work with the read token"
			return Warn, " (read-only; read_repository missing)"
		}
		return OK, " (read-only)"
	}
	var missing []string
	for _, s := range []string{"read_api", "read_user"} {
		if !has(s) {
			missing = append(missing, s)
		}
	}
	if role == readRole {
		*fix = "create the read token with " + strings.Join(missing, " and ") + " (plus read_repository for git over https)"
	} else {
		*fix = "grant the api scope (or at least " + strings.Join(missing, " and ") + ")"
	}
	return Error, " (missing " + strings.Join(missing, ", ") + ")"
}

func gitlabUser(ctx context.Context, hc *http.Client, c Check, base string, hdr map[string]string, role tokenRole) Check {
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
		if role == readRole {
			c.Fix = "create a new token with read_api and read_user and update the forge's read token source"
		}
		return c
	}
	return unexpected(c, base, r.status)
}

func githubAuth(ctx context.Context, hc *http.Client, c Check, f config.Forge, tok string, role tokenRole) Check {
	endpoint := githubUserEndpoint(f)
	r, err := get(ctx, hc, endpoint, githubHeaders(tok))
	if err != nil {
		return failed(c, endpoint, err)
	}
	if r.status == http.StatusUnauthorized {
		c.Status, c.Detail = Error, "token rejected by GitHub (HTTP 401)"
		c.Fix = "refresh the token (e.g. `gh auth login`) or create one with the repo scope"
		if role == readRole {
			c.Fix = "create a fine-grained token with read-only Contents, Issues, Pull requests and Metadata and update the forge's read token source"
		}
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
		if role == readRole {
			c.Detail += "; permissions not reported; give it read-only access"
		} else {
			c.Detail += "; scopes not reported (fine-grained token)"
		}
		return c
	}
	scopes := splitScopes(strings.Join(raw, ","))
	c.Detail += "; scopes: " + joinOrNone(scopes)
	if role == readRole {
		if w := githubWriteScopes(scopes); len(w) > 0 {
			c.Status = Warn
			c.Detail += " (can write: " + strings.Join(w, ", ") + ")"
			c.Fix = "classic scopes such as repo can write and cannot be read-only for private repos: use a fine-grained token with read-only Contents, Issues, Pull requests and Metadata"
		}
		return c
	}
	if !slices.Contains(scopes, "repo") {
		c.Status = Warn
		c.Fix = "without the repo scope ADE can only read public data; add repo for writes"
	}
	return c
}

// githubWriteScopes are the classic scopes in scopes that grant writes.
func githubWriteScopes(scopes []string) []string {
	var w []string
	for _, s := range scopes {
		if s == "repo" || s == "public_repo" || s == "workflow" || strings.HasPrefix(s, "write:") || strings.HasPrefix(s, "admin:") {
			w = append(w, s)
		}
	}
	return w
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
