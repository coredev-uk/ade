// Command fakeai serves a canned OpenAI-compatible /chat/completions for the
// standup and sprint captures, so narratives can be shown without any spend.
// It prints its base URL on the first line and serves until terminated.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

const standup = `Friday was mostly about the export limiter: I commented on api!1 and pushed the branch.
I also closed the legacy CSV path, tracker#4.
Today I'm finishing the review threads on tracker#1 and starting the webhook retries.
Nothing is blocked except tracker#5, which waits on the timezone decision.`

const sprint = `Sprint 42 committed 22 weight and finished most of it; 5 more arrived on the fifth day with the export hotfix.
The remaining carried work is mostly the scheduled exports and the webhook retries.
Two items have no weight and are counted as items, not as zero effort.
Velocity over the last sprints is about 3.5 weight, so the carry-over is the thing to talk about.`

func main() {
	http.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		text := standup
		if len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "sprint review") {
			text = sprint
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "fake-model",
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": text}}},
			"usage":   map[string]int{"prompt_tokens": 900, "completion_tokens": 120},
		})
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("http://" + ln.Addr().String())
	go func() { _ = http.Serve(ln, nil) }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
}
