package player

import (
	"context"
	"os"
	"os/exec"
	"runtime"
)

// MoonlightCLI runs the moonlight-qt executable.
type MoonlightCLI struct {
	Path string
}

// FindMoonlight returns the moonlight-qt executable from PATH or its usual
// install location, or "" if it is not found.
func FindMoonlight() string {
	for _, name := range []string{"moonlight", "moonlight-qt", "Moonlight"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		candidates = []string{os.Getenv("ProgramFiles") + `\Moonlight Game Streaming\Moonlight.exe`}
	case "darwin":
		candidates = []string{"/Applications/Moonlight.app/Contents/MacOS/Moonlight"}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func (m MoonlightCLI) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, m.Path, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func (m MoonlightCLI) Pair(ctx context.Context, address, pin string) error {
	return m.run(ctx, "pair", address, "--pin", pin)
}

func (m MoonlightCLI) Stream(ctx context.Context, address, app string) error {
	return m.run(ctx, "stream", address, app)
}
