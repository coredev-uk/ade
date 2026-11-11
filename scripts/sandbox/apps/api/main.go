// Command api is the sandbox "export service" backend: a tiny HTTP server so
// ADE has a real process to start, probe and share between worktrees.
//
// `api` serves /healthz on $PORT and reports whether the database at $DB_ADDR
// answers. `api db` is the synthetic database it needs: an in-memory key-value
// store speaking a line protocol (PING, SET k v, GET k) on $PORT. Both modes
// live in this one file so a worktree only ever needs main.go and go.mod.
package main

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

func main() {
	port := os.Getenv("PORT")
	if len(os.Args) > 1 && os.Args[1] == "db" {
		if port == "" {
			port = "5432"
		}
		serveDB(port)
		return
	}
	if port == "" {
		port = "8080"
	}
	dbAddr := os.Getenv("DB_ADDR")
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		st := checkDB(dbAddr)
		status, code := "ok", http.StatusOK
		if !st.Reachable {
			status, code = "degraded", http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "service": "api", "db": st})
	})
	mux.HandleFunc("/exports", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{{"id": 1, "name": "audit-log.csv", "rows": 1200}})
	})
	log.Printf("api listening on :%s (db %s)", port, dbAddr)
	log.Fatal(http.ListenAndServe("127.0.0.1:"+port, mux))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type dbState struct {
	Addr      string `json:"addr"`
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

// checkDB pings the database at addr.
func checkDB(addr string) dbState {
	st := dbState{Addr: addr}
	if addr == "" {
		st.Error = "DB_ADDR is not set"
		return st
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("PING\n")); err != nil {
		st.Error = err.Error()
		return st
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Reachable = strings.TrimSpace(reply) == "PONG"
	if !st.Reachable {
		st.Error = "unexpected reply " + strings.TrimSpace(reply)
	}
	return st
}

// serveDB is the synthetic database: one goroutine per connection over a shared map.
func serveDB(port string) {
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		log.Fatal(err)
	}
	var (
		mu   sync.Mutex
		data = map[string]string{}
	)
	log.Printf("db ready on :%s", port)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			defer conn.Close()
			sc := bufio.NewScanner(conn)
			for sc.Scan() {
				f := strings.SplitN(strings.TrimSpace(sc.Text()), " ", 3)
				var reply string
				mu.Lock()
				switch {
				case f[0] == "PING":
					reply = "PONG"
				case f[0] == "SET" && len(f) == 3:
					data[f[1]], reply = f[2], "OK"
				case f[0] == "GET" && len(f) == 2:
					reply = data[f[1]]
				default:
					reply = "ERR unknown command"
				}
				mu.Unlock()
				if _, err := conn.Write([]byte(reply + "\n")); err != nil {
					return
				}
			}
		}()
	}
}
