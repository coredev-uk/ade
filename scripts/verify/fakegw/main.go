// Command fakegw serves a canned LiteLLM GET /key/info for the Budget
// captures, so the gateway profile's provider read has a known outcome
// without any network or spend. It prints its base URL on the first line and
// serves until terminated.
//
//	fakegw            a healthy key: $31.20 of $50.00 over 30 days
//	fakegw -slow 5s   the same answer after a delay (shows the refreshing state)
//	fakegw -reject    HTTP 401, as a revoked or mistyped key gets
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	slow := flag.Duration("slow", 0, "answer after this long")
	reject := flag.Bool("reject", false, "answer 401 Unauthorized")
	flag.Parse()
	http.HandleFunc("/key/info", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(*slow)
		w.Header().Set("Content-Type", "application/json")
		if *reject {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"Authentication Error: invalid key","type":"auth_error","code":"401"}}`)
			return
		}
		now := time.Now().UTC()
		reset := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
		fmt.Fprintf(w, `{"info":{"spend":31.2,"max_budget":50,"budget_duration":"30d","budget_reset_at":%q}}`, reset)
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
