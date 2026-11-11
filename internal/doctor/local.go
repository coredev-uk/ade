package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func configChecks(o Options) []Check {
	const group = "config"
	switch {
	case o.ConfigErr != nil:
		return []Check{{group, "config file", Error, o.ConfigErr.Error(), "fix the TOML syntax in " + o.ConfigFile}}
	case o.Config == nil:
		return nil
	case o.Config.Missing:
		return []Check{{
			group, "config file", Warn, "not found at " + o.ConfigFile + "; defaults in use",
			"create " + o.ConfigFile + " with at least one [forges.<name>] and a [[projects]] entry",
		}}
	}
	checks := []Check{{Group: group, Name: "config file", Status: OK, Detail: o.ConfigFile}}
	issues := o.Config.Validate()
	if len(issues) == 0 {
		return append(checks, Check{Group: group, Name: "config values", Status: OK})
	}
	for _, i := range issues {
		status := Warn
		if i.Severity == "error" {
			status = Error
		}
		checks = append(checks, Check{group, "config " + i.Path, status, i.Msg, "run `ade config check` for details"})
	}
	return checks
}

type need int

const (
	required need = iota
	recommended
	optional
)

type tool struct {
	bin  string
	need need
	min  [2]int // minimum major.minor; zero means any
	why  string
}

// agentDrivers are the CLIs `ade agent` drives: when one is missing, it is
// reported unavailable rather than merely not installed.
var agentDrivers = map[string]bool{"omp": true, "claude": true, "codex": true}

var tools = []tool{
	{"git", required, [2]int{2, 38}, "bare clones and worktrees"},
	{"zellij", recommended, [2]int{}, "the work-tab host"},
	{"lazygit", recommended, [2]int{}, "the git pane"},
	{"nvim", recommended, [2]int{}, "the default editor"},
	{"gh", optional, [2]int{}, "GitHub credential helper"},
	{"glab", optional, [2]int{}, "GitLab credential helper"},
	{"omp", optional, [2]int{}, "agent driver"},
	{"claude", optional, [2]int{}, "agent driver"},
	{"codex", optional, [2]int{}, "agent driver"},
}

func toolChecks(ctx context.Context, o Options) []Check {
	out := make([]Check, len(tools))
	done := make(chan struct{})
	for i, t := range tools {
		go func() {
			out[i] = checkTool(ctx, o, t)
			done <- struct{}{}
		}()
	}
	for range tools {
		<-done
	}
	return out
}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

func checkTool(ctx context.Context, o Options, t tool) Check {
	c := Check{Group: "tools", Name: t.bin}
	path, err := exec.LookPath(t.bin)
	if err != nil {
		switch t.need {
		case required:
			c.Status, c.Detail, c.Fix = Error, "not found on PATH", "install "+t.bin+" ("+t.why+")"
		case recommended:
			c.Status, c.Detail, c.Fix = Warn, "not found on PATH", "install "+t.bin+": "+t.why
		default:
			c.Status, c.Detail = OK, "not installed (optional: "+t.why+")"
			if agentDrivers[t.bin] {
				c.Detail = "unavailable: not installed (agent driver: ade agent --driver " + t.bin + " cannot run)"
			}
		}
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	m := versionRe.FindStringSubmatch(string(out))
	if err != nil || m == nil {
		c.Status, c.Detail = OK, path+" (version unknown)"
		if t.min != [2]int{} {
			c.Status, c.Fix = Warn, "could not read `"+t.bin+" --version`; need "+versionString(t.min)+" or newer"
		}
		return c
	}
	c.Detail = m[0]
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	c.Status = OK
	if major < t.min[0] || (major == t.min[0] && minor < t.min[1]) {
		c.Status, c.Fix = Error, "upgrade "+t.bin+" to "+versionString(t.min)+" or newer"
	}
	return c
}

func versionString(v [2]int) string { return fmt.Sprintf("%d.%d", v[0], v[1]) }

func pathChecks(o Options) []Check {
	var checks []Check
	for _, d := range []struct{ name, dir string }{{"projects root", o.Root}, {"records dir", o.Records}} {
		if d.dir == "" {
			continue
		}
		checks = append(checks, checkWritable(d.name, d.dir))
	}
	return checks
}

// checkWritable probes with a temp file instead of trusting mode bits, which
// miss read-only mounts and ACLs. A directory that does not exist yet passes
// when its nearest existing ancestor is writable, because ADE creates it.
func checkWritable(name, dir string) Check {
	c := Check{Group: "paths", Name: name, Detail: dir}
	probe, missing := dir, false
	for {
		info, err := os.Stat(probe)
		if err == nil {
			if !info.IsDir() {
				c.Status, c.Fix = Error, probe+" is not a directory; point the setting elsewhere"
				return c
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			c.Status, c.Detail, c.Fix = Error, err.Error(), "check permissions on "+probe
			return c
		}
		missing = true
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	f, err := os.CreateTemp(probe, ".ade-doctor-*")
	if err != nil {
		c.Status, c.Detail, c.Fix = Error, "not writable: "+err.Error(), "fix permissions on "+probe+" or change the path in config"
		return c
	}
	f.Close()
	os.Remove(f.Name())
	c.Status = OK
	if missing {
		c.Detail = dir + " (will be created)"
	}
	return c
}

func hostChecks(o Options) []Check {
	detected := "plain terminal"
	switch {
	case o.Getenv("ZELLIJ") != "":
		detected = "zellij"
	case o.Getenv("TERM_PROGRAM") == "vscode":
		detected = "vscode terminal"
	}
	detail := "detected: " + detected
	if o.Config != nil && o.Config.Host.Mode != "auto" && o.Config.Host.Mode != "" {
		detail += "; host.mode forced to " + strings.TrimSpace(o.Config.Host.Mode)
	}
	return []Check{{Group: "host", Name: "host", Status: OK, Detail: detail}}
}
