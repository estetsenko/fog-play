package player

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/estetsenko/fog-play/internal/fetch"
)

// moonlightAsset picks this platform's portable Moonlight build.
func moonlightAsset(r fetch.Release) (fetch.Asset, error) {
	var want []string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		want = []string{"moonlightportable-x64", ".zip"}
	case "windows/arm64":
		want = []string{"moonlightportable-arm64", ".zip"}
	case "linux/amd64":
		want = []string{"x86_64", ".appimage"}
	case "darwin/amd64", "darwin/arm64":
		want = []string{".dmg"}
	default:
		return fetch.Asset{}, fmt.Errorf("no Moonlight download for %s/%s; install it and pass --moonlight", runtime.GOOS, runtime.GOARCH)
	}
	if a, ok := r.Pick(want, "symbols"); ok {
		return a, nil
	}
	return fetch.Asset{}, fmt.Errorf("Moonlight %s has no %s build", r.Tag, strings.Join(want, " "))
}

// EnsureMoonlight returns an installed Moonlight, or downloads a private
// portable copy into dir the first time.
func EnsureMoonlight(ctx context.Context, dir string, log *slog.Logger) (string, error) {
	if p := FindMoonlight(); p != "" {
		return p, nil
	}
	marker := filepath.Join(dir, "installed.json")
	var inst struct{ Tag, Bin string }
	if b, err := os.ReadFile(marker); err == nil && json.Unmarshal(b, &inst) == nil {
		if _, err := os.Stat(inst.Bin); err == nil {
			return inst.Bin, nil
		}
	}

	rel, err := fetch.Latest(ctx, "moonlight-stream/moonlight-qt")
	if err != nil {
		return "", fmt.Errorf("find Moonlight release: %w", err)
	}
	asset, err := moonlightAsset(rel)
	if err != nil {
		return "", err
	}
	log.Info("downloading Moonlight (one time)", "version", rel.Tag, "file", asset.Name)
	vdir := filepath.Join(dir, rel.Tag)
	file := filepath.Join(vdir, asset.Name)
	if err := fetch.Download(ctx, asset.URL, file); err != nil {
		return "", err
	}

	var bin string
	switch name := strings.ToLower(asset.Name); {
	case strings.HasSuffix(name, ".zip"):
		if err := fetch.Unzip(file, vdir); err != nil {
			return "", err
		}
		os.Remove(file)
		bin, err = fetch.FindFile(vdir, "Moonlight.exe")
	case strings.HasSuffix(name, ".dmg"):
		bin, err = installDMG(ctx, file, vdir)
		os.Remove(file)
	default:
		bin, err = file, os.Chmod(file, 0o755)
	}
	if err != nil {
		return "", fmt.Errorf("unpack %s: %w", asset.Name, err)
	}
	b, _ := json.Marshal(map[string]string{"Tag": rel.Tag, "Bin": bin})
	return bin, os.WriteFile(marker, b, 0o600)
}

// installDMG copies Moonlight.app out of a disk image without installing it.
func installDMG(ctx context.Context, dmg, dir string) (string, error) {
	mnt, err := os.MkdirTemp("", "fogplay-moonlight-")
	if err != nil {
		return "", err
	}
	defer os.Remove(mnt)
	if out, err := exec.CommandContext(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", "-mountpoint", mnt, dmg).CombinedOutput(); err != nil {
		return "", fmt.Errorf("hdiutil attach: %v: %s", err, out)
	}
	defer exec.Command("hdiutil", "detach", mnt, "-force").Run()
	if out, err := exec.CommandContext(ctx, "cp", "-R", filepath.Join(mnt, "Moonlight.app"), dir).CombinedOutput(); err != nil {
		return "", fmt.Errorf("copy Moonlight.app: %v: %s", err, out)
	}
	return filepath.Join(dir, "Moonlight.app", "Contents", "MacOS", "Moonlight"), nil
}
