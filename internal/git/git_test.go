package git

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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

func TestRemoteEnvAddsScopedSettings(t *testing.T) {
	ca := [][2]string{{"http.https://h/.sslCAInfo", "/ca.pem"}}
	env := RemoteEnv("https://h/x.git", "oauth2", "s3cret", ca)
	if env[0] != "GIT_CONFIG_COUNT=3" || env[5] != "GIT_CONFIG_KEY_2=http.https://h/.sslCAInfo" || env[6] != "GIT_CONFIG_VALUE_2=/ca.pem" {
		t.Errorf("env = %v", env)
	}
	if got := RemoteEnv("https://h/x.git", "oauth2", "", ca); len(got) != 3 || got[0] != "GIT_CONFIG_COUNT=1" {
		t.Errorf("without a token only the extra setting applies, got %v", got)
	}
	if got := RemoteEnv("https://h/x.git", "oauth2", "", nil); got != nil {
		t.Errorf("nothing to set must leave the environment alone, got %v", got)
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
		"remote: HTTP Basic: Access denied":                           "auth",
		"fatal: could not read Username: terminal prompts disabled":   "auth",
		"git@x: Permission denied (publickey).":                       "auth",
		"fatal: Could not resolve host: gitlab.com":                   "network",
		"remote: Repository not found.":                               "not found",
		"fatal: repository 'https://x/y.git/' not found":              "not found",
		"remote: The project you were looking for could not be found": "not found",
		"something else": "failed",
	}
	for stderr, want := range tests {
		if got := Reason(&Error{Stderr: stderr, Err: errors.New("exit 128")}); got != want {
			t.Errorf("Reason(%q) = %q, want %q", stderr, got, want)
		}
	}
	if got := Reason(fmt.Errorf("forge x token: %w: boom", ErrCredentials)); got != "auth" {
		t.Errorf("credentials = %q", got)
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
