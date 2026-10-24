package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/estetsenko/fog-play/internal/fetch"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// The first Start downloads Sunshine from a (fake) GitHub release, sets
// generated credentials, writes apps, and returns a working API client.
func TestManagedSunshine(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("fake release only has a linux/amd64 AppImage")
	}
	fake := filepath.Join(t.TempDir(), "fakesunshine")
	if out, err := exec.Command("go", "build", "-o", fake, "./testdata/fakesunshine").CombinedOutput(); err != nil {
		t.Fatalf("build fake: %v\n%s", err, out)
	}
	downloads := 0
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/LizardByte/Sunshine/releases/latest":
			json.NewEncoder(w).Encode(fetch.Release{Tag: "v9", Assets: []fetch.Asset{
				{Name: "sunshine-debuginfo-x86_64.AppImage", URL: "http://" + r.Host + "/wrong"},
				{Name: "Sunshine-Windows-AMD64-lite.zip", URL: "http://" + r.Host + "/wrong"},
				{Name: "Sunshine_v9_x86_64.AppImage", URL: "http://" + r.Host + "/bin"},
			}})
		case "/bin":
			downloads++
			b, _ := os.ReadFile(fake)
			w.Write(b)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gh.Close()
	old := fetch.GitHubAPI
	fetch.GitHubAPI = gh.URL
	defer func() { fetch.GitHubAPI = old }()

	dir := t.TempDir()
	port := freePort(t) // web API goes on port+1; fine for a test
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for run := 1; run <= 2; run++ {
		ctx, cancel := context.WithCancel(context.Background())
		m := &ManagedSunshine{Dir: dir, Name: "Test PC", Port: port, Log: log,
			Apps: []App{{Name: "Dota 2", Cmd: "dota2.exe"}}}
		api, err := m.Start(ctx)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		apps, err := api.Apps(ctx)
		if err != nil || len(apps) != 2 || apps[0] != "Desktop" || apps[1] != "Dota 2" {
			t.Errorf("run %d apps: %v %v", run, apps, err)
		}
		cancel()
		m.Stop()
	}
	if downloads != 1 {
		t.Errorf("downloaded %d times, want once", downloads)
	}
}
