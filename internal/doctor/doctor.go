// Package doctor diagnoses an ADE installation: config, tools, directories,
// forge credentials and host capabilities. Every check reports a status and,
// when something is wrong, a hint on how to fix it. Secrets are never included
// in a result.
package doctor

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/httpdebug"
)

// Status of a single check.
type Status string

const (
	OK    Status = "ok"
	Warn  Status = "warn"
	Error Status = "error"
)

// Check is one diagnosis.
type Check struct {
	Group  string `json:"group"` // config | tools | paths | forge | host
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// Options describes what to inspect. Zero values are usable except ConfigFile.
type Options struct {
	ConfigFile string
	// Config is nil when the file failed to load; ConfigErr then says why.
	Config    *config.Config
	ConfigErr error
	Root      string // resolved projects root
	Records   string // resolved records directory

	HTTP    *http.Client  // nil: a client with Timeout
	Timeout time.Duration // per network or subprocess check; default 10s
	Getenv  func(string) string
	// LookPath finds a program on PATH (exec.LookPath when nil); tests replace it.
	LookPath func(string) (string, error)
}

// HasErrors reports whether any check failed.
func HasErrors(checks []Check) bool {
	for _, c := range checks {
		if c.Status == Error {
			return true
		}
	}
	return false
}

func (o *Options) defaults() {
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: o.Timeout, Transport: httpdebug.Transport(nil)}
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
}

// Run executes all checks concurrently and returns them in a stable order:
// config, tools, paths, forges (by name), host.
func Run(ctx context.Context, o Options) []Check {
	o.defaults()
	var groups []func(context.Context) []Check
	groups = append(groups,
		func(context.Context) []Check { return configChecks(o) },
		func(ctx context.Context) []Check { return toolChecks(ctx, o) },
		func(context.Context) []Check { return pathChecks(o) },
	)
	groups = append(groups, forgeChecks(o)...)
	groups = append(groups, func(context.Context) []Check { return hostChecks(o) })
	groups = append(groups, func(context.Context) []Check { return cdeChecks(o) })

	results := make([][]Check, len(groups))
	var wg sync.WaitGroup
	for i, g := range groups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = g(ctx)
		}()
	}
	wg.Wait()

	var all []Check
	for _, r := range results {
		all = append(all, r...)
	}
	return all
}
