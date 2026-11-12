package gitlab

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// Version is what GET /api/v4/version reports. Self-managed instances run any
// release from the last years, so features are gated by it (see Feature) and
// `ade doctor` prints it.
type Version struct {
	Raw   string // "16.9.2-ee", as the server reported it
	Major int
	Minor int
	Patch int
	// Enterprise is true for the Enterprise Edition (EE) build, which also
	// runs as Free until a licence is added; licence tiers are probed apart
	// (Capabilities), not derived from this.
	Enterprise bool
}

// Edition is "EE" or "CE".
func (v Version) Edition() string {
	if v.Enterprise {
		return "EE"
	}
	return "CE"
}

// String is "16.9.2 EE".
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d %s", v.Major, v.Minor, v.Patch, v.Edition())
}

// AtLeast reports whether v is major.minor or newer. The zero Version (not
// read yet, or an unparsable answer) is never at least anything.
func (v Version) AtLeast(major, minor int) bool {
	if v.Major == 0 {
		return false
	}
	return v.Major > major || (v.Major == major && v.Minor >= minor)
}

// Feature is one thing ADE does that needs a minimum GitLab release.
type Feature string

const (
	// FeatureBulkPublish publishes a review's draft notes in one request
	// (POST .../draft_notes/bulk_publish).
	FeatureBulkPublish Feature = "draft-notes-bulk-publish"
	// FeatureReviewers reads reviewer verdicts (GET .../merge_requests/:iid/reviewers).
	FeatureReviewers Feature = "mr-reviewers"
	// FeatureWorkItems is the work-items GraphQL surface (Group.workItemTypes)
	// that tells the Epic tier apart.
	FeatureWorkItems Feature = "work-items-graphql"
	// FeatureCadences is iteration cadences (Group.iterationCadences).
	FeatureCadences Feature = "iteration-cadences"
)

// Requirement is the oldest release a Feature is written against. The floors
// are ADE's, not a claim about when GitLab first shipped the endpoint: below
// them the code paths are untested and an old instance may answer 404, which
// the driver turns into a one-time "not available on this GitLab" note.
type Requirement struct {
	Feature Feature
	Name    string // what the user sees
	Major   int
	Minor   int
}

// Requirements lists every gated feature, in the order doctor reports them.
var Requirements = []Requirement{
	{FeatureBulkPublish, "publishing a review in one request (draft notes bulk_publish)", 15, 9},
	{FeatureReviewers, "reviewer verdicts on merge requests (changes requested)", 14, 0},
	{FeatureWorkItems, "work items and epics (GraphQL)", 16, 0},
	{FeatureCadences, "iteration cadences (GraphQL)", 15, 0},
}

// Supports reports whether v meets f's floor. An unknown feature is supported.
func (v Version) Supports(f Feature) bool {
	for _, r := range Requirements {
		if r.Feature == f {
			return v.AtLeast(r.Major, r.Minor)
		}
	}
	return true
}

// Unsupported lists the requirements v does not meet. An unknown version
// (zero) yields none: nothing is known to be missing.
func (v Version) Unsupported() []Requirement {
	if v.Major == 0 {
		return nil
	}
	var out []Requirement
	for _, r := range Requirements {
		if !v.AtLeast(r.Major, r.Minor) {
			out = append(out, r)
		}
	}
	return out
}

var versionRE = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?`)

// ParseVersion reads "16.9.2-ee", "17.0.0-pre" and "15.1". enterprise is the
// server's own `enterprise` flag (15.6+); older servers only carry the "-ee"
// suffix.
func ParseVersion(raw string, enterprise bool) (Version, error) {
	raw = strings.TrimSpace(raw)
	m := versionRE.FindStringSubmatch(raw)
	if m == nil {
		return Version{}, fmt.Errorf("gitlab: version %q is not major.minor[.patch]", raw)
	}
	v := Version{Raw: raw, Enterprise: enterprise || strings.HasSuffix(strings.ToLower(raw), "-ee")}
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		v.Patch, _ = strconv.Atoi(m[3])
	}
	return v, nil
}

// Version reads GET /api/v4/version. The endpoint needs a valid token
// (forge.ErrAuth otherwise) and exists on every GitLab ADE can talk to.
func (c *Client) Version(ctx context.Context) (Version, error) {
	var w struct {
		Version    string `json:"version"`
		Enterprise bool   `json:"enterprise"`
	}
	if err := c.getJSON(ctx, "/version", nil, false, &w); err != nil {
		return Version{}, err
	}
	return ParseVersion(w.Version, w.Enterprise)
}

// unavailable reports whether err says the instance does not offer an
// endpoint: GitLab answers 404 for routes it lacks (and 403 for licence
// gates), and 400/422 for a GraphQL document naming fields it does not know.
func unavailable(err error) bool {
	if errors.Is(err, forge.ErrUnavailable) || errors.Is(err, forge.ErrNotFound) {
		return true
	}
	var ge *graphqlSchemaError
	return errors.As(err, &ge)
}

// graphqlMissing reports whether err says GraphQL itself is unusable on the
// instance: the endpoint is absent (an HTTP 404/403 from /api/graphql) or the
// schema lacks a field the query names. A group that does not exist is a
// plain forge.ErrNotFound and is not covered.
func graphqlMissing(err error) bool {
	var se *graphqlSchemaError
	if errors.As(err, &se) {
		return true
	}
	var ae *APIError
	return errors.As(err, &ae) && (ae.Status == 404 || ae.Status == 403) && strings.HasSuffix(ae.Path, "/graphql")
}
