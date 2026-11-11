package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
)

// The shape probe calls the read-only GitLab endpoints ADE depends on and
// reports only their *shape*: HTTP status, whether the fields ADE reads are
// present, and each field's JSON type. It never reports a value and never
// writes. Run against a licensed instance it is how the Premium paths, which
// ADE's tests can only model, are confirmed without moving data off the machine.

// ProbeTarget is one registered project's tracker, resolved by the caller from
// its manifest. A manifest that could not be fetched carries Err instead.
type ProbeTarget struct {
	Manifest string   `json:"manifest"` // manifest name, or its source when unreadable
	Forge    string   `json:"forge"`    // key into Config.Forges
	Project  string   `json:"project"`  // tracker project path
	Repos    []string `json:"repos,omitempty"`
	Err      string   `json:"error,omitempty"`
}

// Field is one expected field and its shape: absent, null, string, number,
// bool, object or array.
type Field struct {
	Name  string `json:"name"`
	Shape string `json:"shape"`
}

// Endpoint is the shape of one endpoint's answer. Path is a template (":id"),
// never a concrete path.
type Endpoint struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	// Fields are those of the first returned object; empty when the answer was
	// not 200 or the list was empty (see Notes).
	Fields []Field `json:"fields,omitempty"`
	// Headers lists which pagination and rate-limit headers were present.
	Headers []string `json:"headers,omitempty"`
	Notes   []string `json:"notes,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// ProbeReport is the probe of one target.
type ProbeReport struct {
	ProbeTarget
	Skipped   string      `json:"skipped,omitempty"`
	Endpoints []Endpoint  `json:"endpoints,omitempty"`
	Caps      *forge.Caps `json:"caps,omitempty"`
	CapsError string      `json:"caps_error,omitempty"`
}

// Failed reports whether the probe itself broke: a manifest or token that
// could not be used, an unreachable host or a rejected credential. A gated
// endpoint answering 403 is a finding, not a failure.
func (r ProbeReport) Failed() bool {
	if r.Err != "" || r.CapsError != "" {
		return true
	}
	for _, e := range r.Endpoints {
		if e.Error != "" || e.Status == http.StatusUnauthorized {
			return true
		}
	}
	return false
}

// Probe probes every GitLab target and returns one report each, in order.
// Targets on other forges are skipped with a reason.
func Probe(ctx context.Context, o Options, targets []ProbeTarget) []ProbeReport {
	o.defaults()
	out := make([]ProbeReport, len(targets))
	for i, t := range targets {
		out[i] = probeTarget(ctx, o, t)
	}
	return out
}

func probeTarget(ctx context.Context, o Options, t ProbeTarget) ProbeReport {
	r := ProbeReport{ProbeTarget: t}
	if t.Err != "" {
		return r
	}
	if o.Config == nil {
		r.Err = "no config loaded"
		return r
	}
	f, ok := o.Config.Forges[t.Forge]
	switch {
	case !ok:
		r.Err = fmt.Sprintf("forge %q is not configured", t.Forge)
		return r
	case f.Kind != "gitlab":
		r.Skipped = fmt.Sprintf("the shape probe covers gitlab forges only (this one is %s)", f.Kind)
		return r
	}
	tctx, cancel := context.WithTimeout(ctx, o.Timeout)
	tok, err := f.Token(tctx)
	cancel()
	if err != nil {
		r.Err = err.Error()
		return r
	}
	p := &prober{ctx: ctx, o: o, base: strings.TrimRight(f.URL, "/"), token: tok}
	p.probeAll(t)
	r.Endpoints = p.endpoints
	r.Caps, r.CapsError = p.caps(forge.Group(t.Project))
	return r
}

type prober struct {
	ctx       context.Context
	o         Options
	base      string
	token     string
	endpoints []Endpoint
}

// object is the first element of a list answer, or the answer itself.
type object map[string]json.RawMessage

// probeHeaders are the response headers whose presence is worth reporting:
// pagination (ADE follows the first two and never reads x-total) and rate
// limiting.
var probeHeaders = []string{"X-Next-Page", "Link", "X-Total", "RateLimit-Limit", "RateLimit-Remaining"}

// probeIterationStates are the filter values whose integer `state` codes the
// docs never define.
var probeIterationStates = []string{"upcoming", "current", "closed"}

func (p *prober) probeAll(t ProbeTarget) {
	proj := "/projects/" + url.PathEscape(t.Project)
	grp := "/groups/" + url.PathEscape(forge.Group(t.Project))

	p.probe("current user", "/user", "/user", "id", "username", "name")
	p.probe("issues", "/projects/:id/issues", proj+"/issues?per_page=1&state=all",
		"iid", "state", "references", "task_completion_status", "milestone", "labels", "assignees",
		"weight", "iteration", "epic")
	p.issueRelations(proj)
	p.probe("group milestones", "/groups/:id/milestones", grp+"/milestones?per_page=1",
		"id", "title", "state", "start_date", "due_date")
	p.iterations(grp)
	p.probe("approval rules", "/projects/:id/approval_rules", proj+"/approval_rules?per_page=1",
		"id", "name", "rule_type", "approvals_required")
	p.mergeRequests(append([]string{t.Project}, t.Repos...))
}

// relationSamples bounds how many issues are tried to find one that has links.
const relationSamples = 10

// issueRelations probes the link and MR-relation endpoints on an issue that
// actually has links, so link fields can be reported; the newest issue often
// has none.
func (p *prober) issueRelations(proj string) {
	const links = "/projects/:id/issues/:iid/links"
	objs, status, _, err := p.fetch(proj + "/issues?per_page=" + strconv.Itoa(relationSamples) + "&state=all")
	if err != nil || status != http.StatusOK || len(objs) == 0 {
		p.note("issue links", links, "skipped: no issue to ask about")
		return
	}
	var base string
	for _, o := range objs {
		var iid int
		if json.Unmarshal(o["iid"], &iid) != nil || iid == 0 {
			continue
		}
		base = proj + "/issues/" + strconv.Itoa(iid)
		if linked, _, _, err := p.fetch(base + "/links?per_page=1"); err == nil && len(linked) > 0 {
			break
		}
	}
	if base == "" {
		p.note("issue links", links, "skipped: no issue to ask about")
		return
	}
	p.probe("issue links", links, base+"/links?per_page=1", "iid", "link_type", "issue_link_id", "references", "weight")
	p.probe("related merge requests", "/projects/:id/issues/:iid/related_merge_requests", base+"/related_merge_requests?per_page=1",
		"iid", "state", "references")
	p.probe("closing merge requests", "/projects/:id/issues/:iid/closed_by", base+"/closed_by?per_page=1",
		"iid", "state", "references")
}

// iterations probes the endpoint and, where it answers, reports which integer
// `state` code each state filter returns: the docs never define the mapping,
// so ADE filters by state and never decodes the integer.
func (p *prober) iterations(grp string) {
	const tmpl = "/groups/:id/iterations"
	first := p.probe("group iterations", tmpl, grp+"/iterations?per_page=1&include_ancestors=true",
		"id", "iid", "sequence", "title", "state", "start_date", "due_date")
	if first == nil {
		return
	}
	var notes []string
	for _, state := range probeIterationStates {
		objs, status, _, err := p.fetch(grp + "/iterations?per_page=1&include_ancestors=true&state=" + state)
		if err != nil || status != http.StatusOK || len(objs) == 0 {
			notes = append(notes, fmt.Sprintf("state=%s: no iteration to sample", state))
			continue
		}
		notes = append(notes, stateCodeNote(state, objs[0]))
	}
	last := &p.endpoints[len(p.endpoints)-1]
	last.Notes = append(last.Notes, notes...)
}

// stateCodeNote reports the code an iteration came back with under a state
// filter. It is a classification code (an enum), never user data, and is the
// one value the probe reveals.
func stateCodeNote(filter string, obj object) string {
	shape := shapeOf(obj, "state")
	if shape != "number" {
		return fmt.Sprintf("state=%s: `state` is %s", filter, shape)
	}
	return fmt.Sprintf("state=%s: `state` is the integer code %s", filter, bytes.TrimSpace(obj["state"]))
}

// mergeRequests samples merge requests from the first project that has one
// (the tracker usually has none; the repos do) and probes the single-MR,
// approvals and discussions shapes on it.
func (p *prober) mergeRequests(projects []string) {
	for _, path := range projects {
		proj := "/projects/" + url.PathEscape(path)
		sample := p.probe("merge requests ("+path+")", "/projects/:id/merge_requests", proj+"/merge_requests?per_page=1&state=all",
			"iid", "state", "draft", "work_in_progress", "detailed_merge_status", "has_conflicts",
			"head_pipeline", "pipeline", "references", "reviewers", "sha")
		var iid int
		if err := json.Unmarshal(sample["iid"], &iid); err != nil || iid == 0 {
			continue
		}
		base := proj + "/merge_requests/" + strconv.Itoa(iid)
		p.probe("merge request", "/projects/:id/merge_requests/:iid", base,
			"state", "draft", "detailed_merge_status", "has_conflicts", "head_pipeline", "references", "sha")
		p.probe("merge request approvals", "/projects/:id/merge_requests/:iid/approvals", base+"/approvals",
			"approved", "approvals_required", "approved_by", "has_approval_rules",
			"merge_request_approvers_available", "multiple_approval_rules_available")
		p.probePreferring("merge request discussions", "/projects/:id/merge_requests/:iid/discussions", base+"/discussions?per_page=20",
			"notes[].resolved",
			"id", "individual_note", "notes", "notes[].resolvable", "notes[].resolved", "notes[].system", "notes[].position")
		return
	}
	p.note("merge requests", "/projects/:id/merge_requests", "skipped: no merge request in the tracker or its repos to sample")
}

func (p *prober) caps(group string) (*forge.Caps, string) {
	ctx, cancel := context.WithTimeout(p.ctx, p.o.Timeout)
	defer cancel()
	c := gitlab.New(p.base, p.token, gitlab.WithHTTPClient(p.o.HTTP))
	caps, err := c.Capabilities(ctx, group)
	if err != nil {
		return nil, err.Error()
	}
	return &caps, ""
}

func (p *prober) note(name, path, note string) {
	p.endpoints = append(p.endpoints, Endpoint{Name: name, Path: path, Notes: []string{note}})
}

// probe GETs path, records the endpoint's shape and returns the sampled
// object (nil when there is none).
func (p *prober) probe(name, template, path string, fields ...string) object {
	return p.probePreferring(name, template, path, "", fields...)
}

// probePreferring samples the first object whose `prefer` field is present and
// not null, falling back to the first object. A discussions list starts with
// system notes, which lack the resolution fields ADE counts.
func (p *prober) probePreferring(name, template, path, prefer string, fields ...string) object {
	e := Endpoint{Name: name, Path: template}
	objs, status, hdr, err := p.fetch(path)
	e.Status = status
	obj := pick(objs, prefer)
	switch {
	case err != nil:
		e.Error = err.Error()
	case status == http.StatusOK && obj == nil:
		e.Notes = append(e.Notes, "empty list: no object to take field shapes from")
	case status == http.StatusOK:
		for _, f := range fields {
			e.Fields = append(e.Fields, Field{Name: f, Shape: shapeOf(obj, f)})
		}
	}
	for _, h := range probeHeaders {
		if hdr.Get(h) != "" {
			e.Headers = append(e.Headers, strings.ToLower(h))
		}
	}
	p.endpoints = append(p.endpoints, e)
	return obj
}

func pick(objs []object, prefer string) object {
	if len(objs) == 0 {
		return nil
	}
	if prefer != "" {
		for _, o := range objs {
			if s := shapeOf(o, prefer); s != "absent" && s != "null" {
				return o
			}
		}
	}
	return objs[0]
}

// fetch GETs an API path and returns the answer's objects (one for a single
// resource, none for an empty list). Only the status, headers and the decoded
// objects leave this function; a body is never kept.
func (p *prober) fetch(path string) (objs []object, status int, hdr http.Header, err error) {
	ctx, cancel := context.WithTimeout(p.ctx, p.o.Timeout)
	defer cancel()
	r, err := get(ctx, p.o.HTTP, p.base+"/api/v4"+path, map[string]string{"PRIVATE-TOKEN": p.token})
	if err != nil {
		return nil, 0, nil, err
	}
	if r.status != http.StatusOK {
		return nil, r.status, r.header, nil
	}
	objs, err = objects(r.body)
	return objs, r.status, r.header, err
}

func objects(body []byte) ([]object, error) {
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var list []object
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("unexpected response shape: %w", err)
		}
		return list, nil
	}
	var o object
	if err := json.Unmarshal(body, &o); err != nil {
		return nil, fmt.Errorf("unexpected response shape: %w", err)
	}
	return []object{o}, nil
}

// shapeOf names the JSON type of a field, or "absent". A name like
// "notes[].resolvable" looks inside the first element of an array.
func shapeOf(o object, field string) string {
	raw, ok := lookup(o, field)
	if !ok {
		return "absent"
	}
	return jsonType(raw)
}

func lookup(o object, field string) (json.RawMessage, bool) {
	head, rest, nested := strings.Cut(field, "[].")
	raw, ok := o[head]
	if !nested || !ok {
		return raw, ok
	}
	var items []object
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return nil, false
	}
	return lookup(items[0], rest)
}

func jsonType(raw json.RawMessage) string {
	switch b := bytes.TrimSpace(raw); {
	case len(b) == 0, string(b) == "null":
		return "null"
	case b[0] == '"':
		return "string"
	case b[0] == '{':
		return "object"
	case b[0] == '[':
		return "array"
	case string(b) == "true", string(b) == "false":
		return "bool"
	default:
		return "number"
	}
}
