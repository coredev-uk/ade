package bootstrap

import (
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
)

// taskCmds flattens detected tasks to name -> "cmd [oneshot] [port]" for comparison.
func taskCmds(d Detected) map[string]string {
	out := map[string]string{}
	for _, t := range d.Tasks {
		s := t.Task.Cmd
		if t.Task.Kind == "oneshot" {
			s += " |oneshot"
		}
		if t.Task.Port.Auto {
			s += " |port"
		}
		out[t.Name] = s
	}
	return out
}

func TestDetectTasks(t *testing.T) {
	for _, c := range []struct {
		name    string
		present []string
		content map[string]string
		want    map[string]string
		dev     []string
	}{
		{
			name:    "go service",
			present: []string{"go.mod", "main.go"},
			want:    map[string]string{"dev": "go run .", "test": "go test ./... |oneshot"},
			dev:     []string{"dev"},
		},
		{
			name:    "go library without main: tests only",
			present: []string{"go.mod"},
			want:    map[string]string{"test": "go test ./... |oneshot"},
		},
		{
			name:    "node scripts through the package manager the lockfile names",
			present: []string{"package.json", "pnpm-lock.yaml"},
			content: map[string]string{"package.json": `{"scripts":{"dev":"vite","test":"vitest","build":"vite build","prepare":"x"}}`},
			want:    map[string]string{"dev": "pnpm run dev", "test": "pnpm run test |oneshot", "build": "pnpm run build |oneshot"},
			dev:     []string{"dev"},
		},
		{
			name:    "node without a dev script emits no dev task",
			present: []string{"package.json"},
			content: map[string]string{"package.json": `{"scripts":{"start":"node .","test":"jest"}}`},
			want:    map[string]string{"test": "npm run test |oneshot"},
		},
		{
			name:    "makefile targets",
			present: []string{"Makefile"},
			content: map[string]string{"Makefile": ".PHONY: run test\nVERSION := 1\nrun:\n\tgo run .\ntest:\n\tgo test ./...\nbuild: run\n\tgo build\n"},
			want:    map[string]string{"dev": "make run", "test": "make test |oneshot", "build": "make build |oneshot"},
			dev:     []string{"dev"},
		},
		{
			name:    "makefile check stands in for test",
			present: []string{"Makefile"},
			content: map[string]string{"Makefile": "check:\n\ttrue\n"},
			want:    map[string]string{"test": "make check |oneshot"},
		},
		{
			name:    "package.json scripts beat makefile targets and go defaults",
			present: []string{"package.json", "Makefile", "go.mod", "main.go"},
			content: map[string]string{
				"package.json": `{"scripts":{"dev":"next dev"}}`,
				"Makefile":     "dev:\n\ttrue\ntest:\n\ttrue\n",
			},
			want: map[string]string{"dev": "npm run dev", "test": "make test |oneshot"},
			dev:  []string{"dev"},
		},
		{
			name:    "procfile processes; PORT wires the port",
			present: []string{"Procfile"},
			content: map[string]string{"Procfile": "# comment\nweb: bundle exec puma -p $PORT\nworker: bundle exec sidekiq\n"},
			want:    map[string]string{"web": "bundle exec puma -p $PORT |port", "worker": "bundle exec sidekiq"},
			dev:     []string{"web", "worker"},
		},
		{
			name:    "a nix devShell wraps every command",
			present: []string{"go.mod", "main.go", "flake.nix"},
			content: map[string]string{"flake.nix": "{ outputs = { ... }: { devShells.x86_64-linux.default = 1; }; }"},
			want:    map[string]string{"dev": "nix develop -c go run .", "test": "nix develop -c go test ./... |oneshot"},
			dev:     []string{"dev"},
		},
		{
			name:    "a flake without a devShell does not wrap",
			present: []string{"go.mod", "flake.nix"},
			content: map[string]string{"flake.nix": "{ outputs = { ... }: { packages = 1; }; }"},
			want:    map[string]string{"test": "go test ./... |oneshot"},
		},
		{
			name:    "nothing recognised",
			present: []string{"README.md", "docs"},
			want:    map[string]string{},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := DetectTasks(c.present, c.content)
			got := taskCmds(d)
			if len(got) != len(c.want) {
				t.Fatalf("tasks = %v, want %v", got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Errorf("tasks[%s] = %q, want %q (all: %v)", k, got[k], v, got)
				}
			}
			if !slices.Equal(d.Dev, c.dev) {
				t.Errorf("dev = %v, want %v", d.Dev, c.dev)
			}
		})
	}
}

func TestDetectTasksPortEnvAndHints(t *testing.T) {
	d := DetectTasks([]string{"Procfile", "docker-compose.yml", "go.mod"}, map[string]string{
		"Procfile":           "web: ./server --listen :$PORT\n",
		"docker-compose.yml": "name: x\nservices:\n  db:\n    image: postgres\n  cache:\n    image: redis\nvolumes:\n  data:\n",
	})
	var web config.Task
	for _, tk := range d.Tasks {
		if tk.Name == "web" {
			web = tk.Task
		}
	}
	if !web.Port.Auto || web.Env["PORT"] != "{port}" {
		t.Errorf("web = %+v: want port auto and PORT={port}", web)
	}
	hints := ""
	for _, h := range d.Hints {
		hints += h + "\n"
	}
	for _, want := range []string{"db, cache", "go.mod without a main.go"} {
		if !strings.Contains(hints, want) {
			t.Errorf("hints lack %q:\n%s", want, hints)
		}
	}
	if strings.Contains(hints, "data") {
		t.Errorf("compose volumes are not services:\n%s", hints)
	}
}

func TestComposeServices(t *testing.T) {
	for _, c := range []struct{ name, text, want string }{
		{"two-space", "services:\n  a:\n    image: x\n  b:\n    build: .\n", "a,b"},
		{"four-space", "services:\n    a:\n        image: x\n    b:\n        image: y\n", "a,b"},
		{"networks are not services", "services:\n  a:\n    networks:\n      - n\nnetworks:\n  n:\n", "a"},
		{"no services", "version: '3'\n", ""},
	} {
		got := ""
		for i, s := range composeServices(c.text) {
			if i > 0 {
				got += ","
			}
			got += s
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
