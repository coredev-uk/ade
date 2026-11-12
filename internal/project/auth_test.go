package project

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/git"
)

// basicHeader is the Authorization header git.TokenEnv writes for tok.
func basicHeader(tok string) string {
	return "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("oauth2:"+tok))
}

func hasCredential(c git.Client, tok string) bool {
	return strings.Contains(strings.Join(c.Env, "\n"), basicHeader(tok))
}

func TestClientReadsWithTheReadTokenAndPushesWithTheWriteToken(t *testing.T) {
	e := newEnv(t)
	e.app.Config.Forges = map[string]config.Forge{"f": {
		Kind: "gitlab", URL: "https://git.example.com", TokenEnv: "ADE_TEST_WRITE", ReadTokenEnv: "ADE_TEST_READ",
	}}
	t.Setenv("ADE_TEST_WRITE", "write-secret")
	t.Setenv("ADE_TEST_READ", "read-secret")
	ctx := context.Background()
	const url = "https://git.example.com/g/api.git"

	read, err := e.svc.client(ctx, "f", url)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCredential(read, "read-secret") || hasCredential(read, "write-secret") {
		t.Errorf("read client env = %v, want the read token only", read.Env)
	}
	push, err := e.svc.pushClient(ctx, "f", url)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCredential(push, "write-secret") || hasCredential(push, "read-secret") {
		t.Errorf("push client env = %v, want the write token only", push.Env)
	}
	// The cache holds one entry per mode: the second lookup of each must not
	// see the other's token even after the variables change.
	t.Setenv("ADE_TEST_WRITE", "changed")
	t.Setenv("ADE_TEST_READ", "changed")
	again, _ := e.svc.pushClient(ctx, "f", url)
	if !hasCredential(again, "write-secret") {
		t.Errorf("write token not cached per mode: %v", again.Env)
	}
	again, _ = e.svc.client(ctx, "f", url)
	if !hasCredential(again, "read-secret") {
		t.Errorf("read token not cached per mode: %v", again.Env)
	}
}

func TestClientsShareTheTokenWithoutAReadToken(t *testing.T) {
	e := newEnv(t)
	e.app.Config.Forges = map[string]config.Forge{"f": {Kind: "gitlab", URL: "https://git.example.com", TokenEnv: "ADE_TEST_WRITE"}}
	t.Setenv("ADE_TEST_WRITE", "only-secret")
	const url = "https://git.example.com/g/api.git"
	read, err := e.svc.client(context.Background(), "f", url)
	if err != nil {
		t.Fatal(err)
	}
	push, err := e.svc.pushClient(context.Background(), "f", url)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCredential(read, "only-secret") || !hasCredential(push, "only-secret") {
		t.Errorf("read env %v, push env %v", read.Env, push.Env)
	}
}
