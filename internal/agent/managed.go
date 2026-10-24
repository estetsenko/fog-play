package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/estetsenko/fog-play/internal/fetch"
)

// App is a program Sunshine can launch for a player.
type App struct {
	Name string `json:"name"`
	// Cmd is the command line Sunshine runs; empty streams the desktop.
	Cmd string `json:"cmd,omitempty"`
}

// ManagedSunshine downloads, configures and runs a private copy of Sunshine
// (https://github.com/LizardByte/Sunshine, GPL-3.0) as a child process, so the
// host does not install or set it up by hand. Its config, credentials and
// pairings live in Dir, apart from any Sunshine installed on the PC.
type ManagedSunshine struct {
	Dir  string // e.g. ~/.cache/fogplay/sunshine
	Name string // how the host shows up in Moonlight
	Apps []App  // written to apps.json on first run
	Log  *slog.Logger
	// Port is Sunshine's base port (default 47989); its web API listens on Port+1.
	Port int

	cmd  *exec.Cmd
	done chan struct{}
}

func (m *ManagedSunshine) webURL() string {
	if m.Port == 0 {
		m.Port = 47989
	}
	return fmt.Sprintf("https://localhost:%d", m.Port+1)
}

type installed struct {
	Tag string `json:"tag"`
	Bin string `json:"bin"`
}

// sunshineAsset picks this platform's portable build from a release.
func sunshineAsset(r fetch.Release) (fetch.Asset, error) {
	var want []string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		want = []string{"windows-amd64", ".zip"}
	case "windows/arm64":
		want = []string{"windows-arm64", ".zip"}
	case "linux/amd64":
		want = []string{"x86_64", ".appimage"}
	case "linux/arm64":
		want = []string{"aarch64", ".appimage"}
	default:
		return fetch.Asset{}, fmt.Errorf("fogplay can't run Sunshine for you on %s/%s yet; install Sunshine yourself and set sunshine_user and sunshine_password in the host config", runtime.GOOS, runtime.GOARCH)
	}
	if a, ok := r.Pick(want, "debug", "symbols"); ok {
		return a, nil
	}
	return fetch.Asset{}, fmt.Errorf("Sunshine %s has no %s build", r.Tag, strings.Join(want, " "))
}

// ensureBinary returns the Sunshine executable, downloading the latest
// release the first time.
func (m *ManagedSunshine) ensureBinary(ctx context.Context) (string, error) {
	marker := filepath.Join(m.Dir, "installed.json")
	var inst installed
	if err := readJSON(marker, &inst); err == nil {
		if _, err := os.Stat(inst.Bin); err == nil {
			return inst.Bin, nil
		}
	}
	rel, err := fetch.Latest(ctx, "LizardByte/Sunshine")
	if err != nil {
		return "", fmt.Errorf("find Sunshine release: %w", err)
	}
	asset, err := sunshineAsset(rel)
	if err != nil {
		return "", err
	}
	m.Log.Info("downloading Sunshine (one time)", "version", rel.Tag, "file", asset.Name)
	dir := filepath.Join(m.Dir, rel.Tag)
	file := filepath.Join(dir, asset.Name)
	if err := fetch.Download(ctx, asset.URL, file); err != nil {
		return "", err
	}
	bin := file
	if strings.HasSuffix(strings.ToLower(asset.Name), ".zip") {
		if err := fetch.Unzip(file, dir); err != nil {
			return "", err
		}
		os.Remove(file)
		if bin, err = fetch.FindFile(dir, "sunshine.exe"); err != nil {
			return "", fmt.Errorf("sunshine.exe not found in %s", asset.Name)
		}
	} else if err := os.Chmod(bin, 0o755); err != nil {
		return "", err
	}
	return bin, writeJSON(marker, installed{Tag: rel.Tag, Bin: bin})
}

type creds struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

// Start launches Sunshine and returns a client for its API. Sunshine stops
// when ctx is cancelled or Stop is called.
func (m *ManagedSunshine) Start(ctx context.Context) (*SunshineHTTP, error) {
	probe := NewSunshineHTTP(m.webURL(), "", "")
	if _, err := probe.Apps(ctx); err == nil || !isConnRefused(err) {
		return nil, errors.New("another Sunshine is already running on this PC; quit it, or set sunshine_user and sunshine_password in the host config to use that one")
	}

	bin, err := m.ensureBinary(ctx)
	if err != nil {
		return nil, err
	}
	data := filepath.Join(m.Dir, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		return nil, err
	}

	conf := filepath.Join(data, "sunshine.conf")
	lines := []string{
		"sunshine_name = " + m.Name,
		fmt.Sprintf("port = %d", m.Port),
		"file_apps = " + filepath.Join(data, "apps.json"),
		"file_state = " + filepath.Join(data, "sunshine_state.json"),
		"credentials_file = " + filepath.Join(data, "sunshine_state.json"),
		"log_path = " + filepath.Join(data, "sunshine.log"),
		"origin_web_ui_allowed = pc",
		"system_tray = disabled",
		"notify_pre_releases = disabled",
	}
	if err := os.WriteFile(conf, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return nil, err
	}

	appsFile := filepath.Join(data, "apps.json")
	if _, err := os.Stat(appsFile); errors.Is(err, os.ErrNotExist) {
		if err := writeApps(appsFile, m.Apps); err != nil {
			return nil, err
		}
	}

	var c creds
	credFile := filepath.Join(data, "fogplay-credentials.json")
	if readJSON(credFile, &c) != nil || c.Password == "" {
		c = creds{User: "fogplay", Password: randomHex(16)}
		if err := writeJSON(credFile, c); err != nil {
			return nil, err
		}
	}
	setCreds := m.command(ctx, bin, conf, "--creds", c.User, c.Password)
	if out, err := setCreds.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("set Sunshine credentials: %w: %s", err, tail(out))
	}

	console, err := os.Create(filepath.Join(data, "sunshine-console.log"))
	if err != nil {
		return nil, err
	}
	m.cmd = m.command(context.Background(), bin, conf)
	m.cmd.Stdout, m.cmd.Stderr = console, console
	if err := m.cmd.Start(); err != nil {
		console.Close()
		return nil, fmt.Errorf("start Sunshine: %w", err)
	}
	m.done = make(chan struct{})
	go func() {
		err := m.cmd.Wait()
		console.Close()
		if ctx.Err() == nil {
			m.Log.Error("Sunshine exited", "err", err, "log", filepath.Join(data, "sunshine.log"))
		}
		close(m.done)
	}()
	go func() {
		<-ctx.Done()
		m.Stop()
	}()

	api := NewSunshineHTTP(m.webURL(), c.User, c.Password)
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := api.Apps(ctx); err == nil {
			break
		} else if time.Now().After(deadline) {
			m.Stop()
			return nil, fmt.Errorf("Sunshine did not start in 60s (last error: %v); see %s", err, filepath.Join(data, "sunshine.log"))
		}
		select {
		case <-m.done:
			return nil, fmt.Errorf("Sunshine exited at startup; see %s", filepath.Join(data, "sunshine.log"))
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	m.Log.Info("Sunshine running", "data", data)
	return api, nil
}

// Stop ends Sunshine and waits for it to exit.
func (m *ManagedSunshine) Stop() {
	if m.cmd == nil || m.cmd.Process == nil {
		return
	}
	m.cmd.Process.Kill()
	<-m.done
}

func (m *ManagedSunshine) command(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = filepath.Dir(bin)
	// AppImages normally need FUSE; this runs them without it.
	cmd.Env = append(os.Environ(), "APPIMAGE_EXTRACT_AND_RUN=1")
	return cmd
}

func writeApps(path string, apps []App) error {
	type sunApp struct {
		Name string `json:"name"`
		Cmd  string `json:"cmd,omitempty"`
	}
	list := []sunApp{{Name: "Desktop"}}
	for _, a := range apps {
		if a.Name != "" && !strings.EqualFold(a.Name, "Desktop") {
			list = append(list, sunApp(a))
		}
	}
	return writeJSON(path, map[string]any{"env": map[string]string{}, "apps": list})
}

func isConnRefused(err error) bool {
	s := err.Error()
	return strings.Contains(s, "connection refused") || strings.Contains(s, "actively refused")
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[len(s)-300:]
	}
	return s
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
