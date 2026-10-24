// Command fakesunshine imitates the bits of Sunshine that ManagedSunshine
// uses: "sunshine CONF --creds USER PASS" and serving the web API on port+1.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
)

func main() {
	conf := map[string]string{}
	b, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, " = "); ok {
			conf[k] = v
		}
	}
	if len(os.Args) == 5 && os.Args[2] == "--creds" {
		out, _ := json.Marshal(map[string]string{"user": os.Args[3], "pass": os.Args[4]})
		if err := os.WriteFile(conf["credentials_file"], out, 0o600); err != nil {
			panic(err)
		}
		return
	}
	var creds map[string]string
	b, _ = os.ReadFile(conf["credentials_file"])
	json.Unmarshal(b, &creds)
	port, _ := strconv.Atoi(conf["port"])

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != creds["user"] || p != creds["pass"] {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/apps" {
			apps, _ := os.ReadFile(conf["file_apps"])
			w.Write(apps)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port+1))
	if err != nil {
		panic(err)
	}
	srv.Listener = l
	srv.StartTLS()
	select {}
}
