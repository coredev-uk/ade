package bootstrap

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/config"
)

// rootFiles are the files at a repo's root whose content says how to run it.
// Nothing deeper is read: a conservative manifest only states what the root
// shows.
var rootFiles = []string{
	"go.mod", "main.go", "package.json", "Makefile", "makefile", "GNUmakefile", "Procfile", "flake.nix",
	"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml",
	"pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb", "package-lock.json",
}

// contentFiles are the rootFiles whose content is parsed; the others only need
// to exist.
var contentFiles = map[string]bool{
	"package.json": true, "Makefile": true, "makefile": true, "GNUmakefile": true, "Procfile": true,
	"flake.nix": true, "docker-compose.yml": true, "docker-compose.yaml": true, "compose.yml": true, "compose.yaml": true,
}

// NamedTask is a task with its name in the repo's run graph.
type NamedTask struct {
	Name string
	Task config.Task
	// From says which file the command came from ("package.json", "Makefile").
	From string
}

// Detected is what one repo's root files say about running it.
type Detected struct {
	Tasks []NamedTask
	// Dev lists the tasks that make up the repo's share of the dev group.
	Dev []string
	// Hints are suggestions the files support but do not settle; they are
	// written as comments.
	Hints []string
}

var (
	nixShell     = regexp.MustCompile(`\bdevShells?\b`)
	makeTarget   = regexp.MustCompile(`(?m)^([A-Za-z0-9][A-Za-z0-9_-]*)[ \t]*:([^=]|$)`)
	procLine     = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_-]*):\s*(\S.*)$`)
	envPort      = regexp.MustCompile(`\$\{?PORT\b\}?`)
	composeTop   = regexp.MustCompile(`^[A-Za-z_]`)
	composeChild = regexp.MustCompile(`^([ \t]+)([A-Za-z0-9][A-Za-z0-9_.-]*):\s*$`)
)

// DetectTasks reads a repo's root: names lists the files present and content
// holds the text of those in contentFiles. Precedence when two files offer a
// task of the same name: Procfile, package.json scripts, Makefile targets,
// then the language default (go.mod).
func DetectTasks(names []string, content map[string]string) Detected {
	has := func(n string) bool { return slices.Contains(names, n) }
	var d Detected
	seen := map[string]bool{}
	wrap := ""
	if nixShell.MatchString(content["flake.nix"]) {
		wrap = "nix develop -c "
		d.Hints = append(d.Hints, "flake.nix has a devShell: commands run through `nix develop -c`")
	}
	add := func(name, from, cmd string, oneshot bool, port bool) {
		if seen[name] {
			return
		}
		seen[name] = true
		t := config.Task{Cmd: wrap + cmd}
		if oneshot {
			t.Kind = "oneshot"
		}
		if port {
			t.Port = config.PortSpec{Auto: true}
			t.Env = map[string]string{"PORT": "{port}"}
		}
		d.Tasks = append(d.Tasks, NamedTask{Name: name, Task: t, From: from})
	}

	if procfile, ok := content["Procfile"]; ok {
		for line := range strings.SplitSeq(procfile, "\n") {
			m := procLine.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			add(m[1], "Procfile", strings.TrimSpace(m[2]), false, envPort.MatchString(m[2]))
		}
	}

	if raw, ok := content["package.json"]; ok {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if err := json.Unmarshal([]byte(raw), &pkg); err == nil {
			pm := packageManager(has)
			for _, s := range []struct {
				name    string
				oneshot bool
			}{{"dev", false}, {"test", true}, {"build", true}, {"lint", true}} {
				if _, ok := pkg.Scripts[s.name]; ok {
					add(s.name, "package.json", pm+" run "+s.name, s.oneshot, false)
				}
			}
			if _, hasDev := pkg.Scripts["dev"]; !hasDev {
				if _, hasStart := pkg.Scripts["start"]; hasStart {
					d.Hints = append(d.Hints, fmt.Sprintf("package.json has a start script but no dev: [repos.<name>.tasks.dev] cmd = %q", pm+" run start"))
				}
			}
			d.Hints = append(d.Hints, "node_modules are per worktree: hooks.post_clone / hooks.post_worktree = [\""+installCommand(pm, has)+"\"]")
		}
	}

	for _, mk := range []string{"Makefile", "makefile", "GNUmakefile"} {
		text, ok := content[mk]
		if !ok {
			continue
		}
		targets := map[string]bool{}
		for _, m := range makeTarget.FindAllStringSubmatch(text, -1) {
			targets[m[1]] = true
		}
		for _, name := range []string{"dev", "run", "serve", "start"} {
			if targets[name] {
				add("dev", "Makefile", "make "+name, false, false)
				break
			}
		}
		for _, s := range []string{"test", "build", "lint"} {
			if targets[s] {
				add(s, "Makefile", "make "+s, true, false)
			}
		}
		if !targets["test"] && targets["check"] {
			add("test", "Makefile", "make check", true, false)
		}
		break
	}

	if has("go.mod") {
		if has("main.go") {
			add("dev", "go.mod", "go run .", false, false)
		} else {
			d.Hints = append(d.Hints, "go.mod without a main.go at the root: if the service lives under cmd/, [repos.<name>.tasks.dev] cmd = \"go run ./cmd/<name>\"")
		}
		add("test", "go.mod", "go test ./...", true, false)
	}

	for _, f := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		if svcs := composeServices(content[f]); len(svcs) > 0 {
			d.Hints = append(d.Hints, fmt.Sprintf("%s defines %s: shared infrastructure could be [services.<name>] (cmd = \"docker compose up <name>\")", f, strings.Join(svcs, ", ")))
			break
		}
	}

	d.Dev = devTasks(d.Tasks)
	return d
}

func devTasks(tasks []NamedTask) []string {
	for _, t := range tasks {
		if t.Name == "dev" {
			return []string{"dev"}
		}
	}
	var out []string
	for _, t := range tasks {
		if t.From == "Procfile" && t.Task.Kind != "oneshot" {
			out = append(out, t.Name)
		}
	}
	return out
}

// packageManager picks the node package manager by lockfile; npm otherwise.
func packageManager(has func(string) bool) string {
	switch {
	case has("pnpm-lock.yaml"):
		return "pnpm"
	case has("yarn.lock"):
		return "yarn"
	case has("bun.lock"), has("bun.lockb"):
		return "bun"
	}
	return "npm"
}

func installCommand(pm string, has func(string) bool) string {
	switch pm {
	case "pnpm":
		return "pnpm install --frozen-lockfile"
	case "yarn":
		return "yarn install --frozen-lockfile"
	case "bun":
		return "bun install --frozen-lockfile"
	}
	if has("package-lock.json") {
		return "npm ci"
	}
	return "npm install"
}

// composeServices lists the service names under the top-level `services:` key
// of a compose file. It reads indentation, not YAML: enough for names.
func composeServices(text string) []string {
	var out []string
	inServices, indent := false, ""
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if composeTop.MatchString(line) {
			inServices, indent = strings.HasPrefix(line, "services:"), ""
			continue
		}
		if !inServices {
			continue
		}
		m := composeChild.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if indent == "" {
			indent = m[1]
		}
		if m[1] == indent {
			out = append(out, m[2])
		}
	}
	return out
}

// wantedContent lists the root files worth reading, from the names present.
func wantedContent(present []string) []string {
	var out []string
	for _, n := range rootFiles {
		if contentFiles[n] && slices.Contains(present, n) {
			out = append(out, n)
		}
	}
	return out
}

// presentRoot keeps the names of rootFiles found among a root listing.
func presentRoot(listing []string) []string {
	var out []string
	for _, p := range listing {
		if slices.Contains(rootFiles, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// envKeys returns a task's env keys in order, for stable rendering.
func envKeys(env map[string]string) []string { return slices.Sorted(maps.Keys(env)) }
