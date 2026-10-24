// Package agent runs on a host PC. It keeps the PC registered and online,
// picks up sessions booked on it, pairs the player's Moonlight with the local
// Sunshine, and unpairs everyone when the session ends.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/estetsenko/fog-play/internal/client"
	"github.com/estetsenko/fog-play/internal/signal"
	"github.com/estetsenko/fog-play/internal/store"
)

type Config struct {
	HostID string // empty registers a new host
	Name   string
	Region string
	GPU    string
	CPU    string
	// Address is what the player's Moonlight connects to (IP or name, optional :port).
	Address string
	// Games used when Sunshine's app list cannot be read.
	FallbackGames []string

	PollInterval      time.Duration // how often to check for a booking
	HeartbeatInterval time.Duration
	PairTimeout       time.Duration // how long to keep retrying a PIN
}

type Agent struct {
	cfg      Config
	api      *client.Client
	sunshine Sunshine
	log      *slog.Logger

	apps map[string]string // game slug -> Sunshine app name
}

func New(cfg Config, api *client.Client, sun Sunshine, log *slog.Logger) *Agent {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = 10 * time.Second
	}
	if cfg.PairTimeout == 0 {
		cfg.PairTimeout = 90 * time.Second
	}
	return &Agent{cfg: cfg, api: api, sunshine: sun, log: log}
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns a Sunshine app name into a catalog slug: "Steam Big Picture" -> "steam-big-picture".
func Slug(name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 64 {
		s = strings.TrimRight(s[:64], "-")
	}
	return s
}

func (a *Agent) loadGames(ctx context.Context) []store.Game {
	names, err := a.sunshine.Apps(ctx)
	if err != nil {
		a.log.Warn("cannot read Sunshine apps, using configured games", "err", err)
		names = a.cfg.FallbackGames
	}
	a.apps = map[string]string{}
	var games []store.Game
	for _, n := range names {
		slug := Slug(n)
		if slug == "" || a.apps[slug] != "" {
			continue
		}
		a.apps[slug] = n
		games = append(games, store.Game{Slug: slug, Title: n})
	}
	return games
}

// Setup registers the host if needed and syncs its games. It returns the host id.
func (a *Agent) Setup(ctx context.Context) (string, error) {
	games := a.loadGames(ctx)
	if len(games) == 0 {
		return "", errors.New("no games: add apps in Sunshine or set games in the agent config")
	}
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
	a.log.Info("online, waiting for players", "address", a.cfg.Address)

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
	defer func() {
		// Revoke the player's pairing so they cannot reconnect directly later.
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.sunshine.UnpairAll(cctx); err != nil {
			log.Error("unpair Moonlight clients failed; remove them in the Sunshine web UI", "err", err)
		}
		log.Info("session over")
	}()

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
	// Stop pairing attempts as soon as the session ends.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-sig.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	if err := sig.Send(ctx, signal.TypeHostInfo, signal.HostInfo{Address: a.cfg.Address, App: app}); err != nil {
		log.Error("send host info failed", "err", err)
		return
	}

	// The relay closes when the session ends, from either side or the reaper.
	for {
		m, err := sig.Receive(ctx)
		if err != nil {
			return
		}
		if m.Type != signal.TypePairPIN {
			continue
		}
		var p signal.PairPIN
		if json.Unmarshal(m.Data, &p) != nil {
			continue
		}
		res := signal.PairResult{OK: true}
		if err := a.pair(ctx, p); err != nil {
			log.Warn("pairing failed", "err", err)
			res = signal.PairResult{Error: err.Error()}
		} else {
			log.Info("Moonlight paired", "client", p.ClientName)
		}
		if err := sig.Send(ctx, signal.TypePairResult, res); err != nil {
			return
		}
	}
}

// pair keeps offering the PIN to Sunshine until Moonlight's pairing request
// shows up and is accepted, or PairTimeout passes.
func (a *Agent) pair(ctx context.Context, p signal.PairPIN) error {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.PairTimeout)
	defer cancel()
	for {
		ok, err := a.sunshine.SubmitPIN(ctx, p.PIN, p.ClientName)
		if ok {
			return nil
		}
		if err != nil {
			a.log.Debug("PIN not accepted yet", "err", err)
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return fmt.Errorf("Sunshine did not accept the PIN: %w", err)
			}
			return errors.New("Sunshine did not see a pairing request from Moonlight in time")
		case <-time.After(time.Second):
		}
	}
}
