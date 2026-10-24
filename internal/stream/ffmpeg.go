package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/estetsenko/fog-play/internal/fetch"
)

// FFmpegRepo publishes the Windows FFmpeg builds fogplay downloads.
const FFmpegRepo = "BtbN/FFmpeg-Builds"

type installed struct {
	Tag  string `json:"tag"`
	Path string `json:"path"`
}

// EnsureFFmpeg returns an FFmpeg to capture with: one on PATH, one fogplay
// downloaded earlier into dir, or (on Windows) a fresh download.
func EnsureFFmpeg(ctx context.Context, dir string, log *slog.Logger) (string, error) {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p, nil
	}
	marker := filepath.Join(dir, "installed.json")
	var in installed
	if b, err := os.ReadFile(marker); err == nil && json.Unmarshal(b, &in) == nil {
		if _, err := os.Stat(in.Path); err == nil {
			return in.Path, nil
		}
	}
	if runtime.GOOS != "windows" {
		return "", errors.New("ffmpeg not found; install it with your package manager (for example: sudo apt install ffmpeg)")
	}

	rel, err := fetch.Latest(ctx, FFmpegRepo)
	if err != nil {
		return "", err
	}
	// The GPL build has every GPU encoder plus libx264 as a fallback.
	asset, ok := rel.Pick([]string{"master-latest-win64-gpl.zip"})
	if !ok {
		return "", fmt.Errorf("no ffmpeg-master-latest-win64-gpl.zip in %s release %s", FFmpegRepo, rel.Tag)
	}
	log.Info("downloading FFmpeg (one time)", "release", rel.Tag, "file", asset.Name)
	vdir := filepath.Join(dir, rel.Tag)
	zip := filepath.Join(vdir, asset.Name)
	if err := fetch.Download(ctx, asset.URL, zip); err != nil {
		return "", err
	}
	if err := fetch.Unzip(zip, vdir); err != nil {
		return "", err
	}
	os.Remove(zip)
	p, err := fetch.FindFile(vdir, "ffmpeg.exe")
	if err != nil {
		return "", fmt.Errorf("ffmpeg.exe not in %s: %w", asset.Name, err)
	}
	b, _ := json.Marshal(installed{Tag: rel.Tag, Path: p})
	if err := os.WriteFile(marker, b, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

func displayName() string {
	if d := os.Getenv("DISPLAY"); d != "" {
		return d
	}
	return ":0"
}
