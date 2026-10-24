// Command fogplay is the Fog Play client. The same binary plays games from
// other PCs (with Moonlight) and shares this PC's games (with Sunshine).
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
	"text/tabwriter"

	"github.com/estetsenko/fog-play/internal/cli"
	"github.com/estetsenko/fog-play/internal/client"
	"github.com/estetsenko/fog-play/internal/fetch"
	"github.com/estetsenko/fog-play/internal/player"
)

const usage = `usage:
  play from another PC:
    fogplay register --server URL --email EMAIL   create a player account and sign in
    fogplay login    --server URL --email EMAIL   sign in
    fogplay games                                 list games and how many hosts are free
    fogplay play GAME [--moonlight PATH]          stream GAME (a slug from "fogplay games")
                                                  Moonlight is downloaded on first use

  share this PC's games:
    fogplay host [--config PATH] [--debug]        go online and serve players until Ctrl+C
                                                  Sunshine is downloaded and run on first use
`

type config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func configPath() string {
	d, err := cli.ConfigDir()
	if err != nil {
		fatal(err)
	}
	return filepath.Join(d, "player.json")
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "register", "login":
		signIn(ctx, cmd, args)
	case "games":
		games(ctx)
	case "play":
		play(ctx, args)
	case "host":
		host(ctx, args)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func signIn(ctx context.Context, cmd string, args []string) {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	server := fs.String("server", "", "Fog Play server URL, e.g. http://192.168.1.20:8080")
	email := fs.String("email", "", "account email")
	password := fs.String("password", "", "password (prompted if empty)")
	role := fs.String("role", "player", "register only: player, host, or both")
	fs.Parse(args)
	if *server == "" || *email == "" {
		fatal(errors.New("--server and --email are required"))
	}
	if *password == "" {
		p, err := cli.Prompt("Password: ", true)
		if err != nil {
			fatal(err)
		}
		*password = p
	}
	api := client.New(*server, "")
	var err error
	if cmd == "register" {
		err = api.Register(ctx, *email, *password, *role)
	} else {
		err = api.Login(ctx, *email, *password)
	}
	if err != nil {
		fatal(err)
	}
	if err := cli.SaveJSON(configPath(), config{Server: *server, Token: api.Token}); err != nil {
		fatal(err)
	}
	fmt.Println("Signed in as", *email)
}

func apiClient() *client.Client {
	var c config
	if err := cli.LoadJSON(configPath(), &c); err != nil || c.Token == "" {
		fatal(errors.New(`not signed in; run "fogplay login" first`))
	}
	return client.New(c.Server, c.Token)
}

func games(ctx context.Context) {
	list, err := apiClient().Games(ctx)
	if err != nil {
		fatal(err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "GAME\tTITLE\tFREE HOSTS\tONLINE")
	for _, g := range list {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\n", g.Slug, g.Title, g.IdleHosts, g.OnlineHosts)
	}
	tw.Flush()
}

func play(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("play", flag.ExitOnError)
	moonlight := fs.String("moonlight", "", "path to the Moonlight executable (default: an installed Moonlight, else a downloaded portable copy)")
	name := fs.String("name", "", "name this PC pairs under in Sunshine (defaults to the hostname)")
	if len(args) == 0 {
		fatal(errors.New("usage: fogplay play GAME"))
	}
	game := args[0]
	fs.Parse(args[1:])

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *moonlight == "" {
		dir, err := fetch.CacheDir("moonlight")
		if err != nil {
			fatal(err)
		}
		if *moonlight, err = player.EnsureMoonlight(ctx, dir, log); err != nil {
			fatal(fmt.Errorf("get Moonlight: %w (or install it and pass --moonlight)", err))
		}
	}
	if *name == "" {
		*name, _ = os.Hostname()
	}
	err := player.Play(ctx, apiClient(), player.MoonlightCLI{Path: *moonlight}, player.Options{
		Game:       game,
		ClientName: *name,
		Started: func(address, app string) {
			fmt.Printf("Streaming %s from %s. Close Moonlight or press Ctrl+C to end.\n", app, address)
		},
	}, log)
	if client.IsStatus(err, http.StatusServiceUnavailable) {
		fatal(errors.New("no free host has this game right now"))
	}
	if err != nil {
		fatal(err)
	}
	fmt.Println("Session ended.")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "fogplay:", err)
	os.Exit(1)
}
