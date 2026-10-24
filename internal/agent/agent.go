// Package agent runs on a host PC. It keeps the PC registered and online,
// picks up sessions booked on it, starts the booked game, and streams to the
// player until the session ends.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/estetsenko/fog-play/internal/client"
	"github.com/estetsenko/fog-play/internal/store"
	"github.com/estetsenko/fog-play/internal/stream"
)

type Config struct {
	HostID string // empty registers a new host
	Name   string
	Region string
	GPU    string
	CPU    string
	// Apps this host offers besides the desktop.
	Apps []App

	PollInterval      time.Duration // how often to check for a booking
	HeartbeatInterval time.Duration
}

// App is a game the host offers. Cmd, if set, is started when a session for
// it begins and stopped when the session ends.
type App struct {
	Name string `json:"name"`
	Cmd  string `json:"cmd,omitempty"`
}

// Streamer streams one session to its player until ctx ends.
type Streamer interface {
	Serve(ctx context.Context, sig stream.Signal) error
}

type Agent struct {
	cfg      Config
	api      *client.Client
	streamer Streamer
	log      *slog.Logger

	apps map[string]App // game slug -> app
}

func New(cfg Config, api *client.Client, st Streamer, log *slog.Logger) *Agent {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = 10 * time.Second
	}
	return &Agent{cfg: cfg, api: api, streamer: st, log: log}
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns an app name into a catalog slug: "Steam Big Picture" -> "steam-big-picture".
func Slug(name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 64 {
		s = strings.TrimRight(s[:64], "-")
	}
	return s
}

func (a *Agent) loadGames() []store.Game {
	a.apps = map[string]App{}
	var games []store.Game
	for _, app := range append([]App{{Name: "Desktop"}}, a.cfg.Apps...) {
		slug := Slug(app.Name)
		if slug == "" {
			continue
		}
		if _, dup := a.apps[slug]; dup {
			continue
		}
		a.apps[slug] = app
		games = append(games, store.Game{Slug: slug, Title: app.Name})
	}
	return games
}

// Setup registers the host if needed and syncs its games. It returns the host id.
func (a *Agent) Setup(ctx context.Context) (string, error) {
	games := a.loadGames()
	if a.cfg.HostID != "" {
		_, err := a.api.Host(ctx, a.cfg.HostID)
		if client.IsStatus(err, http.StatusNotFound) {
			a.log.Warn("saved host not found on server, registering a new one", "host_id", a.cfg.HostID)
			a.cfg.HostID = ""
		} else if err != nil {
			return "", err
		}
	}
	if a.cfg.HostID == "" {
		h, err := a.api.CreateHost(ctx, client.HostSpec{Name: a.cfg.Name, Region: a.cfg.Region, GPU: a.cfg.GPU, CPU: a.cfg.CPU, Games: games})
		if err != nil {
			return "", fmt.Errorf("register host: %w", err)
		}
		a.cfg.HostID = h.ID
		a.log.Info("registered host", "host_id", h.ID)
	} else if err := a.api.SetHostGames(ctx, a.cfg.HostID, games); err != nil {
		return "", fmt.Errorf("sync games: %w", err)
	}
	a.log.Info("games synced", "count", len(games))
	return a.cfg.HostID, nil
}

// Run keeps the host online and serves sessions until ctx is cancelled.
// Call Setup first.
func (a *Agent) Run(ctx context.Context) error {
	id := a.cfg.HostID
	var busy atomic.Bool
	beat := func() {
		status := "idle"
		if busy.Load() {
			status = "busy"
		}
		if err := a.api.Heartbeat(ctx, id, status); err != nil && ctx.Err() == nil {
			a.log.Warn("heartbeat failed", "err", err)
		}
	}
	beat()
	a.log.Info("online, waiting for players")

	// Heartbeats keep going while a session is being served.
	go func() {
		t := time.NewTicker(a.cfg.HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				beat()
			}
		}
	}()

	poll := time.NewTicker(a.cfg.PollInterval)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			off, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			a.api.Heartbeat(off, id, "offline")
			cancel()
			return nil
		case <-poll.C:
			sess, err := a.api.HostSession(ctx, id)
			if err != nil {
				if ctx.Err() == nil {
					a.log.Warn("check for sessions failed", "err", err)
				}
				continue
			}
			if sess == nil {
				continue
			}
			busy.Store(true)
			beat()
			a.serve(ctx, *sess)
			busy.Store(false)
			beat()
		}
	}
}

// serve handles one session from booking until it ends.
func (a *Agent) serve(ctx context.Context, sess store.Session) {
	log := a.log.With("session_id", sess.ID, "game", sess.Game)
	log.Info("session booked")
	defer log.Info("session over")

	app, ok := a.apps[sess.Game]
	if !ok {
		log.Error("booked for a game this host does not have")
		a.api.SetSessionState(ctx, sess.ID, "failed", "host does not have this game")
		return
	}
	if err := a.api.SetSessionState(ctx, sess.ID, "connecting", ""); err != nil {
		log.Error("accept session failed", "err", err)
		return
	}
	sig, err := a.api.Signal(ctx, sess.ID)
	if err != nil {
		log.Error("signal connect failed", "err", err)
		a.api.SetSessionState(ctx, sess.ID, "failed", "host could not reach signaling")
		return
	}
	defer sig.Close()
	// The relay closes when the session ends, from either side or the reaper.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-sig.Done():
			cancel()
		case <-ctx.Done():
		}
	}()

	if app.Cmd != "" {
		game, err := startApp(app.Cmd)
		if err != nil {
			log.Error("start game failed", "cmd", app.Cmd, "err", err)
			a.fail(sess.ID, "host could not start the game")
			return
		}
		log.Info("game started", "pid", game.Process.Pid)
		defer stopApp(game)
	}

	if err := a.streamer.Serve(ctx, sig); err != nil {
		log.Error("stream failed", "err", err)
		a.fail(sess.ID, "stream failed on the host: "+err.Error())
	}
}

func (a *Agent) fail(sessionID, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.api.SetSessionState(ctx, sessionID, "failed", reason)
}

// startApp runs a game's command line: an executable path, optionally
// followed by arguments.
func startApp(cmdline string) (*exec.Cmd, error) {
	name, args := splitCommand(cmdline)
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go cmd.Wait()
	return cmd, nil
}

func stopApp(cmd *exec.Cmd) {
	cmd.Process.Kill()
}

// splitCommand splits a command line on spaces, keeping "quoted parts" together.
func splitCommand(s string) (string, []string) {
	var parts []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ' ' && !quoted:
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	if len(parts) == 0 {
		return "", nil
	}
	return parts[0], parts[1:]
}
