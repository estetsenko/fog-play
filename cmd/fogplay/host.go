package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/estetsenko/fog-play/internal/agent"
	"github.com/estetsenko/fog-play/internal/cli"
	"github.com/estetsenko/fog-play/internal/client"
	"github.com/estetsenko/fog-play/internal/fetch"
	"github.com/estetsenko/fog-play/internal/stream"
)

// hostConfig is the config for "fogplay host", which streams this PC's games
// to players.
type hostConfig struct {
	Server   string `json:"server"`
	Email    string `json:"email"`
	Password string `json:"password"`

	Name   string `json:"name"`
	Region string `json:"region"`
	GPU    string `json:"gpu"`
	CPU    string `json:"cpu"`

	// Apps offered besides the desktop, e.g.
	// {"name": "Dota 2", "cmd": "C:\\path\\to\\dota2.exe"}.
	Apps []agent.App `json:"apps,omitempty"`

	// Stream settings; zero values use the defaults.
	BitrateMbps int    `json:"bitrate_mbps,omitempty"`
	FPS         int    `json:"fps,omitempty"`
	Encoder     string `json:"encoder,omitempty"` // e.g. h264_nvenc; default: the first that works
	FFmpeg      string `json:"ffmpeg,omitempty"`  // path; default: on PATH, else downloaded
	TestPattern bool   `json:"test_pattern,omitempty"`

	HostID string `json:"host_id"`
}

const hostExample = `{
  "server": "http://192.168.1.20:8080",
  "email": "host@example.com",
  "password": "change-me-please",
  "name": "Living room PC",
  "gpu": "RTX 4070"
}`

// host registers this PC with the server, keeps it online, and lets players
// stream from it until interrupted.
func host(ctx context.Context, args []string) {
	defDir, _ := cli.ConfigDir()
	fs := flag.NewFlagSet("host", flag.ExitOnError)
	path := fs.String("config", filepath.Join(defDir, "host.json"), "host config file")
	debug := fs.Bool("debug", false, "verbose logs")
	fs.Parse(args)

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	var cfg hostConfig
	if err := cli.LoadJSON(*path, &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "No host config at %s. Create it like this:\n%s\n", *path, hostExample)
			os.Exit(2)
		}
		fatal(err)
	}
	if cfg.Server == "" || cfg.Email == "" || cfg.Password == "" {
		fatal(errors.New("server, email and password are required in the host config"))
	}
	if cfg.Name == "" {
		cfg.Name, _ = os.Hostname()
	}
	if cfg.Region == "" {
		cfg.Region = "home"
	}

	api := client.New(cfg.Server, "")
	if err := hostSignIn(ctx, api, cfg, log); err != nil {
		fatal(err)
	}

	st, err := streamer(ctx, cfg, log)
	if err != nil {
		fatal(err)
	}
	a := agent.New(agent.Config{
		HostID: cfg.HostID, Name: cfg.Name, Region: cfg.Region, GPU: cfg.GPU, CPU: cfg.CPU, Apps: cfg.Apps,
	}, api, st, log)

	id, err := a.Setup(ctx)
	if err != nil {
		fatal(err)
	}
	if id != cfg.HostID {
		cfg.HostID = id
		if err := cli.SaveJSON(*path, cfg); err != nil {
			log.Warn("could not save host id to config", "err", err)
		}
	}
	if err := a.Run(ctx); err != nil {
		fatal(err)
	}
}

// hostSignIn logs in, creating the host account on first run.
func hostSignIn(ctx context.Context, api *client.Client, cfg hostConfig, log *slog.Logger) error {
	err := api.Login(ctx, cfg.Email, cfg.Password)
	if !client.IsStatus(err, http.StatusUnauthorized) {
		return err
	}
	if err := api.Register(ctx, cfg.Email, cfg.Password, "host"); err != nil {
		if client.IsStatus(err, http.StatusConflict) {
			return errors.New("wrong password for " + cfg.Email)
		}
		return err
	}
	log.Info("created host account", "email", cfg.Email)
	return nil
}

// streamer finds FFmpeg and a working encoder and sets up screen capture.
func streamer(ctx context.Context, cfg hostConfig, log *slog.Logger) (*stream.Host, error) {
	ffmpeg := cfg.FFmpeg
	if ffmpeg == "" {
		dir, err := fetch.CacheDir("ffmpeg")
		if err != nil {
			return nil, err
		}
		if ffmpeg, err = stream.EnsureFFmpeg(ctx, dir, log); err != nil {
			return nil, err
		}
	}
	var enc stream.Encoder
	if cfg.Encoder != "" {
		for _, e := range stream.Encoders {
			if e.Name == cfg.Encoder {
				enc = e
			}
		}
		if enc.Name == "" {
			return nil, fmt.Errorf("unknown encoder %q", cfg.Encoder)
		}
	} else {
		var err error
		if enc, err = stream.PickEncoder(ctx, ffmpeg); err != nil {
			return nil, err
		}
	}
	fps := cfg.FPS
	if fps == 0 {
		fps = 60
	}
	capture := stream.TestCapture(1280, 720, fps)
	if !cfg.TestPattern {
		var err error
		if capture, err = stream.ScreenCapture(fps); err != nil {
			return nil, err
		}
	}
	log.Info("stream ready", "ffmpeg", ffmpeg, "encoder", enc.Name, "fps", fps)
	return &stream.Host{
		FFmpeg: ffmpeg, Encoder: enc, Capture: capture,
		Bitrate: cfg.BitrateMbps * 1_000_000, GOP: fps,
		Input: stream.NewInjector(log), Log: log,
	}, nil
}
