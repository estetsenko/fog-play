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
)

// hostConfig is the config for "fogplay host", which shares this PC's games
// through the local Sunshine.
type hostConfig struct {
	Server   string `json:"server"`
	Email    string `json:"email"`
	Password string `json:"password"`

	Name    string `json:"name"`
	Region  string `json:"region"`
	GPU     string `json:"gpu"`
	CPU     string `json:"cpu"`
	Address string `json:"address"`

	// Apps the bundled Sunshine offers besides the desktop, e.g.
	// {"name": "Dota 2", "cmd": "C:\\path\\to\\dota2.exe"}. Read on first run only;
	// later edits go through Sunshine's apps.json in the fogplay cache folder.
	Apps []agent.App `json:"apps"`

	// Set these only to use a Sunshine you installed yourself instead of the
	// one fogplay downloads and runs.
	SunshineURL      string   `json:"sunshine_url,omitempty"`
	SunshineUser     string   `json:"sunshine_user,omitempty"`
	SunshinePassword string   `json:"sunshine_password,omitempty"`
	Games            []string `json:"games,omitempty"`

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
// stream from it with Moonlight until interrupted.
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
	if cfg.Address == "" {
		cfg.Address = cli.LocalIP()
	}

	api := client.New(cfg.Server, "")
	if err := hostSignIn(ctx, api, cfg, log); err != nil {
		fatal(err)
	}

	var sun agent.Sunshine
	var managed *agent.ManagedSunshine
	// fatal exits without running defers; stop our Sunshine first.
	die := func(err error) {
		if managed != nil {
			managed.Stop()
		}
		fatal(err)
	}
	if cfg.SunshineUser != "" {
		if cfg.SunshineURL == "" {
			cfg.SunshineURL = "https://localhost:47990"
		}
		sun = agent.NewSunshineHTTP(cfg.SunshineURL, cfg.SunshineUser, cfg.SunshinePassword)
	} else {
		dir, err := fetch.CacheDir("sunshine")
		if err != nil {
			fatal(err)
		}
		managed = &agent.ManagedSunshine{Dir: dir, Name: cfg.Name, Apps: cfg.Apps, Log: log}
		defer managed.Stop()
		if sun, err = managed.Start(ctx); err != nil {
			die(err)
		}
	}

	a := agent.New(agent.Config{
		HostID: cfg.HostID, Name: cfg.Name, Region: cfg.Region, GPU: cfg.GPU, CPU: cfg.CPU,
		Address: cfg.Address, FallbackGames: cfg.Games,
	}, api, sun, log)

	id, err := a.Setup(ctx)
	if err != nil {
		die(err)
	}
	if id != cfg.HostID {
		cfg.HostID = id
		if err := cli.SaveJSON(*path, cfg); err != nil {
			log.Warn("could not save host id to config", "err", err)
		}
	}
	if err := a.Run(ctx); err != nil {
		die(err)
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
