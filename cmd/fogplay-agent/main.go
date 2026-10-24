// Command fogplay-agent runs on a host PC next to Sunshine. It registers the
// PC with the Fog Play server, keeps it online, and lets players stream from
// it with Moonlight.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/estetsenko/fog-play/internal/agent"
	"github.com/estetsenko/fog-play/internal/cli"
	"github.com/estetsenko/fog-play/internal/client"
)

type config struct {
	Server   string `json:"server"`
	Email    string `json:"email"`
	Password string `json:"password"`

	Name    string `json:"name"`
	Region  string `json:"region"`
	GPU     string `json:"gpu"`
	CPU     string `json:"cpu"`
	Address string `json:"address"`

	SunshineURL      string   `json:"sunshine_url"`
	SunshineUser     string   `json:"sunshine_user"`
	SunshinePassword string   `json:"sunshine_password"`
	Games            []string `json:"games"`

	HostID string `json:"host_id"`
}

const example = `{
  "server": "http://192.168.1.20:8080",
  "email": "host@example.com",
  "password": "change-me-please",
  "name": "Living room PC",
  "region": "home",
  "gpu": "RTX 4070",
  "cpu": "Ryzen 7 7700",
  "address": "",
  "sunshine_url": "https://localhost:47990",
  "sunshine_user": "sunshine",
  "sunshine_password": "your-sunshine-web-ui-password"
}`

func main() {
	defDir, _ := cli.ConfigDir()
	path := flag.String("config", filepath.Join(defDir, "agent.json"), "agent config file")
	debug := flag.Bool("debug", false, "verbose logs")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	var cfg config
	if err := cli.LoadJSON(*path, &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "No config at %s. Create it like this:\n%s\n", *path, example)
			os.Exit(2)
		}
		fatal(err)
	}
	if cfg.Server == "" || cfg.Email == "" || cfg.Password == "" {
		fatal(errors.New("server, email and password are required in the config"))
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
	if cfg.SunshineURL == "" {
		cfg.SunshineURL = "https://localhost:47990"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	api := client.New(cfg.Server, "")
	if err := signIn(ctx, api, cfg, log); err != nil {
		fatal(err)
	}

	a := agent.New(agent.Config{
		HostID: cfg.HostID, Name: cfg.Name, Region: cfg.Region, GPU: cfg.GPU, CPU: cfg.CPU,
		Address: cfg.Address, FallbackGames: cfg.Games,
	}, api, agent.NewSunshineHTTP(cfg.SunshineURL, cfg.SunshineUser, cfg.SunshinePassword), log)

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

// signIn logs in, creating the host account on first run.
func signIn(ctx context.Context, api *client.Client, cfg config, log *slog.Logger) error {
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

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "fogplay-agent:", err)
	os.Exit(1)
}
