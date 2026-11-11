// Command web is the sandbox "export dashboard" dev server. It serves a page
// and a /healthz that reports whether the API at $API_URL is reachable, so a
// workspace that wires API_URL wrongly is visible at a glance.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

type apiState struct {
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
	Status    int    `json:"status,omitempty"`
	Error     string `json:"error,omitempty"`
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	apiURL := os.Getenv("API_URL")
	client := &http.Client{Timeout: 2 * time.Second}

	check := func() apiState {
		st := apiState{URL: apiURL}
		if apiURL == "" {
			st.Error = "API_URL is not set"
			return st
		}
		resp, err := client.Get(apiURL + "/healthz")
		if err != nil {
			st.Error = err.Error()
			return st
		}
		defer resp.Body.Close()
		st.Status = resp.StatusCode
		st.Reachable = resp.StatusCode >= 200 && resp.StatusCode < 300
		return st
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "service": "web", "api": check()})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		st := check()
		state := "unreachable"
		if st.Reachable {
			state = "reachable"
		}
		fmt.Fprintf(w, "<h1>Export dashboard</h1><p>API %s is %s.</p>\n", st.URL, state)
	})
	log.Printf("web ready on :%s (api %s)", port, apiURL)
	log.Fatal(http.ListenAndServe("127.0.0.1:"+port, mux))
}
