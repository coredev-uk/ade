package codews

import (
	"encoding/json"
	"testing"
)

func TestTruncateName(t *testing.T) {
	if got := truncateName("short", 24); got != "short" {
		t.Errorf("short = %q", got)
	}
	if got := truncateName("1-rate-limit-exports-for-api", 12); got != "1-rate-limi…" || len([]rune(got)) != 12 {
		t.Errorf("long = %q", got)
	}
}

func TestFolderName(t *testing.T) {
	if got := FolderName("web", "/r/acme/web/main"); got != "web (main)" {
		t.Errorf("FolderName = %q", got)
	}
}

func TestMergeKeepsUserKeys(t *testing.T) {
	existing := []byte(`{"extensions":{"recommendations":["a.b"]},"settings":{"editor.tabSize":2,"files.exclude":{"x":true}},"folders":[{"path":"/old"}]}`)
	out, err := Merge(existing, []Folder{{Name: "web (main)", Path: "/r/web/main"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Folders    []Folder       `json:"folders"`
		Settings   map[string]any `json:"settings"`
		Extensions map[string]any `json:"extensions"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Folders) != 1 || got.Folders[0].Path != "/r/web/main" {
		t.Errorf("folders = %+v", got.Folders)
	}
	if got.Settings["editor.tabSize"] != float64(2) {
		t.Errorf("user setting lost: %v", got.Settings)
	}
	if _, ok := got.Settings["git.autoRepositoryDetection"]; !ok {
		t.Errorf("ADE setting missing: %v", got.Settings)
	}
	if got.Extensions == nil {
		t.Error("extensions key lost")
	}
	if _, err := Merge([]byte("{ // comment\n}"), nil, nil); err == nil {
		t.Error("a file with comments must be an error")
	}
}
