package doctor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/config"
)

const aiGroup = "ai"

// aiChecks tells "AI off" from "AI misconfigured" without making a model call:
// the mode, whether the selected profile resolves, whether its driver binary is
// on PATH or its gateway answers a read-only GET of /models (no tokens spent),
// and whether its key resolves. AI is optional, so a problem is a warning, never
// an error: ADE works without it. The key is never printed, only where it comes
// from.
func aiChecks(ctx context.Context, o Options) []Check {
	if o.Config == nil {
		return nil
	}
	a := o.Config.AI
	mode := Check{Group: aiGroup, Name: "mode"}
	switch a.State() {
	case config.AIOff:
		mode.Status, mode.Detail = OK, `off: ADE runs without AI (ai.mode = "off"), nothing else is checked`
		return []Check{mode}
	case config.AINone:
		mode.Status, mode.Detail = OK, "no profile configured: ADE runs without AI; add one under [ai.profiles] to use it"
		return []Check{mode}
	case config.AINoProfile:
		mode.Status, mode.Detail = Warn, fmt.Sprintf("ai.mode = %q, but no profile is selected: AI stays unavailable", a.Mode)
		mode.Fix = "set ai.profile to one of: " + profileNames(a)
		return []Check{mode}
	case config.AIUnknown:
		mode.Status, mode.Detail = Warn, fmt.Sprintf("ai.profile = %q is not defined: AI stays unavailable", a.Profile)
		mode.Fix = "set ai.profile to one of: " + profileNames(a)
		return []Check{mode}
	}
	mode.Status, mode.Detail = OK, fmt.Sprintf("ai.mode = %q, profile %q", a.Mode, a.Profile)
	checks := []Check{mode}

	p := a.Profiles[a.Profile]
	table := "[ai.profiles." + a.Profile + "]"
	prof := Check{Group: aiGroup, Name: "profile"}
	model, err := p.Model("explain")
	switch {
	case err != nil:
		prof.Status, prof.Detail = Warn, err.Error()
		prof.Fix = "set models.explain under " + table + ": every role falls back to it"
	default:
		prof.Status, prof.Detail = OK, fmt.Sprintf("%s: driver %s, explain model %s", a.Profile, p.Driver, model)
	}
	checks = append(checks, prof)

	if p.Driver == "omp" {
		return append(checks, ompCheck(o, p, table))
	}
	key := keyCheck(ctx, o, p, table)
	checks = append(checks, key)
	if key.Status != OK {
		return checks // no point asking a gateway with no key
	}
	return append(checks, gatewayCheck(ctx, o, p, table))
}

func profileNames(a config.AI) string {
	return strings.Join(slices.Sorted(maps.Keys(a.Profiles)), ", ")
}

func ompCheck(o Options, p config.AIProfile, table string) Check {
	c := Check{Group: aiGroup, Name: "driver"}
	bin := p.Command
	if bin == "" {
		bin = "omp"
	}
	path, err := o.LookPath(bin)
	if err != nil {
		c.Status, c.Detail = Warn, bin+" is not installed: AI calls fail"
		c.Fix = "install omp, set command under " + table + ", or set ai.mode = \"off\""
		return c
	}
	c.Status, c.Detail = OK, "omp found at "+path
	return c
}

func keyCheck(ctx context.Context, o Options, p config.AIProfile, table string) Check {
	c := Check{Group: aiGroup, Name: "key"}
	kctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	if _, err := p.Key(kctx); err != nil {
		c.Status = Warn
		c.Detail = "the key command failed"
		if errors.Is(err, config.ErrNoSecret) {
			c.Detail = err.Error() // names the setting, never a value
		}
		c.Fix = "set key_env or key_cmd under " + table + " so it yields the API key"
		return c
	}
	c.Status, c.Detail = OK, "resolved via "+keySource(p)
	return c
}

func keySource(p config.AIProfile) string {
	if p.KeyEnv != "" {
		return "$" + p.KeyEnv
	}
	return "key_cmd"
}

func gatewayCheck(ctx context.Context, o Options, p config.AIProfile, table string) Check {
	c := Check{Group: aiGroup, Name: "gateway"}
	host := p.Driver
	if u, err := url.Parse(p.BaseURL); err == nil && u.Host != "" {
		host = u.Host
	} else if p.Driver == "anthropic" {
		host = "api.anthropic.com"
	}
	gctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	d, err := ai.DriverFor(gctx, p, o.HTTP)
	if err == nil {
		var models []string
		if models, err = d.Models(gctx); err == nil {
			c.Status, c.Detail = OK, fmt.Sprintf("%s answers: %s", host, countOf(len(models), "model"))
			return c
		}
	}
	// ai.Human words the cause; the raw error (a gateway's body) is left out.
	cause, fix, _ := strings.Cut(ai.Human(err), " · ")
	c.Status, c.Detail = Warn, host+": "+cause
	c.Fix = fix
	if fix == "" || strings.Contains(fix, "Log page") {
		c.Fix = "check base_url and the network under " + table
	}
	return c
}

func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
