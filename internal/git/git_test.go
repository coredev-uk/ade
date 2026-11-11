package git

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestTokenEnvKeepsTokenOutOfArgv(t *testing.T) {
	env := TokenEnv("https://h/x.git", "oauth2", "s3cret")
	want := "GIT_CONFIG_VALUE_0=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("oauth2:s3cret"))
	if len(env) != 5 || env[0] != "GIT_CONFIG_COUNT=2" || env[1] != "GIT_CONFIG_KEY_0=http.extraHeader" || env[2] != want || env[3] != "GIT_CONFIG_KEY_1=url.https://h/x.git.insteadOf" || env[4] != "GIT_CONFIG_VALUE_1=https://h/x.git" {
		t.Errorf("env = %v", env)
	}
}

func TestErrorCapturesStderrAndClassifies(t *testing.T) {
	_, err := Client{}.Run(context.Background(), t.TempDir(), "clone", "--bare", "file:///definitely/not/here.git", "x")
	var ge *Error
	if !errors.As(err, &ge) || !strings.Contains(ge.Stderr, "does not appear to be a git repository") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "git clone") {
		t.Errorf("message = %q", err)
	}
}

func TestReason(t *testing.T) {
	tests := map[string]string{
		"remote: HTTP Basic: Access denied":                         "auth",
		"fatal: could not read Username: terminal prompts disabled": "auth",
		"git@x: Permission denied (publickey).":                     "auth",
		"fatal: Could not resolve host: gitlab.com":                 "network",
		"remote: Repository not found.":                             "not found",
		"something else":                                            "failed",
	}
	for stderr, want := range tests {
		if got := Reason(&Error{Stderr: stderr, Err: errors.New("exit 128")}); got != want {
			t.Errorf("Reason(%q) = %q, want %q", stderr, got, want)
		}
	}
	if got := Reason(context.Canceled); got != "cancelled" {
		t.Errorf("cancel = %q", got)
	}
}

func TestBranchDir(t *testing.T) {
	if got := BranchDir("feature/412-rate-limit"); got != "feature-412-rate-limit" {
		t.Errorf("BranchDir = %q", got)
	}
}
