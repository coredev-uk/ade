package devshell

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// finder serves files from a map keyed by absolute path and finds only the
// named programs on PATH.
func finder(files map[string]string, progs ...string) Finder {
	return Finder{
		LookPath: func(name string) (string, error) {
			for _, p := range progs {
				if p == name {
					return "/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		},
		ReadFile: func(path string) ([]byte, error) {
			if s, ok := files[path]; ok {
				return []byte(s), nil
			}
			return nil, os.ErrNotExist
		},
	}
}

const dir = "/ws/api/42-rate-limit"

var (
	envrc = map[string]string{dir + "/.envrc": "use flake\n"}
	flake = map[string]string{dir + "/flake.nix": "{ outputs = { self }: { devShells.x86_64-linux.default = null; }; }"}
	both  = map[string]string{dir + "/.envrc": "use flake", dir + "/flake.nix": "devShells"}
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		progs []string
		mode  string
		want  Kind
		note  bool
	}{
		{"auto prefers direnv", both, []string{"direnv", "nix"}, "", Direnv, false},
		{"auto without direnv on PATH falls to nix", both, []string{"nix"}, "auto", Nix, false},
		{"auto envrc only", envrc, []string{"direnv"}, "auto", Direnv, false},
		{"auto flake only", flake, []string{"nix"}, "", Nix, false},
		{"auto: an .envrc without direnv installed is not wrapped", envrc, []string{"nix"}, "", None, false},
		{"auto: a flake without a devShell is not wrapped", map[string]string{dir + "/flake.nix": "{ packages = {}; }"}, []string{"nix"}, "", None, false},
		{"auto: nothing to enter", nil, []string{"direnv", "nix"}, "", None, false},
		{"none forces off", both, []string{"direnv", "nix"}, "none", None, false},
		{"direnv forced", both, []string{"direnv", "nix"}, "direnv", Direnv, false},
		{"nix forced beats the .envrc", both, []string{"direnv", "nix"}, "nix", Nix, false},
		{"direnv forced but missing", both, []string{"nix"}, "direnv", None, true},
		{"direnv forced but no .envrc", flake, []string{"direnv"}, "direnv", None, true},
		{"nix forced but missing", both, []string{"direnv"}, "nix", None, true},
		{"nix forced but no flake", envrc, []string{"nix"}, "nix", None, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := finder(tt.files, tt.progs...).Detect(dir, tt.mode)
			if got.Kind != tt.want || got.Dir != dir {
				t.Fatalf("Detect = %+v, want kind %s in %s", got, tt.want, dir)
			}
			if (got.Note != "") != tt.note {
				t.Errorf("Note = %q, want one: %v", got.Note, tt.note)
			}
		})
	}
}

func TestWrap(t *testing.T) {
	argv := []string{"sh", "-c", "go run ./cmd/api"}
	tests := []struct {
		shell Shell
		want  []string
	}{
		{Shell{Kind: Direnv, Dir: dir}, []string{"direnv", "exec", dir, "sh", "-c", "go run ./cmd/api"}},
		{Shell{Kind: Nix, Dir: dir}, []string{"nix", "develop", dir, "-c", "sh", "-c", "go run ./cmd/api"}},
		{Shell{Kind: None, Dir: dir}, argv},
		{Shell{}, argv},
	}
	for _, tt := range tests {
		in := append([]string(nil), argv...)
		if got := tt.shell.Wrap(in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s.Wrap = %q, want %q", tt.shell, got, tt.want)
		}
	}
	if got := (Shell{Kind: Direnv, Dir: dir}).WarmArgv(); !reflect.DeepEqual(got, []string{"direnv", "exec", dir, "true"}) {
		t.Errorf("WarmArgv = %q", got)
	}
	if (Shell{}).WarmArgv() != nil || (Shell{}).Active() {
		t.Error("the zero shell wraps and warms nothing")
	}
	if (Shell{}).String() != "none" || (Shell{Kind: Nix}).String() != "nix" {
		t.Error("String")
	}
}

func TestValidMode(t *testing.T) {
	for _, m := range []string{"", "auto", "direnv", "nix", "none"} {
		if !ValidMode(m) {
			t.Errorf("%q must be valid", m)
		}
	}
	if ValidMode("devbox") {
		t.Error("devbox is not a mode")
	}
}

// The real finder reads the real file system and PATH.
func TestDetectOnDisk(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, ".envrc"), []byte("export X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "direnv"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if got := Detect(d, ""); got.Kind != Direnv {
		t.Fatalf("Detect = %+v, want direnv", got)
	}
	t.Setenv("PATH", t.TempDir())
	if got := Detect(d, ""); got.Kind != None {
		t.Fatalf("without direnv on PATH, Detect = %+v", got)
	}
}
