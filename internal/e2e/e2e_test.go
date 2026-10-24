// Package e2e runs the server, a host agent and a player in one process,
// with fake Sunshine and Moonlight, to check the whole session flow.
package e2e

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/estetsenko/fog-play/internal/agent"
	"github.com/estetsenko/fog-play/internal/api"
	"github.com/estetsenko/fog-play/internal/auth"
	"github.com/estetsenko/fog-play/internal/client"
	"github.com/estetsenko/fog-play/internal/db"
	"github.com/estetsenko/fog-play/internal/player"
	"github.com/estetsenko/fog-play/internal/presence"
	"github.com/estetsenko/fog-play/internal/store"
)

// fakeRig stands in for Sunshine on the host and Moonlight on the player:
// Moonlight's pair registers a PIN request, and Sunshine accepts it only if
// the agent submits the same PIN.
type fakeRig struct {
	mu        sync.Mutex
	pending   string // PIN Moonlight is waiting on
	paired    chan struct{}
	unpairs   int
	streamed  []string
	streamFor time.Duration
}

func newRig() *fakeRig {
	return &fakeRig{paired: make(chan struct{}), streamFor: 200 * time.Millisecond}
}

func (f *fakeRig) Apps(context.Context) ([]string, error) { return []string{"Desktop", "Dota 2"}, nil }

func (f *fakeRig) SubmitPIN(_ context.Context, pin, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending == "" || f.pending != pin {
		return false, nil
	}
	f.pending = ""
	close(f.paired)
	return true, nil
}

func (f *fakeRig) UnpairAll(context.Context) error {
	f.mu.Lock()
	f.unpairs++
	f.mu.Unlock()
	return nil
}

func (f *fakeRig) Pair(ctx context.Context, _, pin string) error {
	f.mu.Lock()
	f.pending = pin
	f.mu.Unlock()
	select {
	case <-f.paired:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeRig) Stream(ctx context.Context, address, app string) error {
	f.mu.Lock()
	f.streamed = append(f.streamed, address+" "+app)
	f.mu.Unlock()
	select {
	case <-time.After(f.streamFor):
	case <-ctx.Done():
	}
	return nil
}

func startServer(t *testing.T) *httptest.Server {
	t.Helper()
	dbURL, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if dbURL == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE users, hosts, games, host_games, sessions CASCADE`); err != nil {
		t.Fatal(err)
	}
	pres, err := presence.NewRedis(redisURL, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pres.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(api.New(store.New(pool), pres, auth.NewIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Hour), log).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func dotaIdle(t *testing.T, api *client.Client) int {
	games, err := api.Games(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range games {
		if g.Slug == "dota-2" {
			return g.IdleHosts
		}
	}
	return -1
}

func TestPlaySession(t *testing.T) {
	srv := startServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rig := newRig()

	// Host side.
	hostAPI := client.New(srv.URL, "")
	if err := hostAPI.Register(ctx, "host@example.com", "password1", "host"); err != nil {
		t.Fatal(err)
	}
	ag := agent.New(agent.Config{Name: "Test PC", Region: "home", Address: "10.0.0.5",
		PollInterval: 50 * time.Millisecond, HeartbeatInterval: time.Second}, hostAPI, rig, log)
	if _, err := ag.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	agentDone := make(chan struct{})
	go func() { ag.Run(ctx); close(agentDone) }()

	// Player side.
	playerAPI := client.New(srv.URL, "")
	if err := playerAPI.Register(ctx, "player@example.com", "password1", "player"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "host online", func() bool { return dotaIdle(t, playerAPI) == 1 })

	var started string
	err := player.Play(ctx, playerAPI, rig, player.Options{
		Game: "dota-2", ClientName: "player-pc",
		Started: func(address, app string) { started = address + " " + app },
	}, log)
	if err != nil {
		t.Fatalf("play: %v", err)
	}
	if started != "10.0.0.5 Dota 2" || len(rig.streamed) != 1 || rig.streamed[0] != started {
		t.Errorf("streamed %q, started %q", rig.streamed, started)
	}

	sessions, err := playerAPI.Sessions(ctx)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions: %v %+v", err, sessions)
	}
	if s := sessions[0]; s.State != "ended" || s.Minutes != 1 || s.StartedAt == nil {
		t.Errorf("session after play: %+v", s)
	}

	// The agent unpairs Moonlight and goes back to idle.
	waitFor(t, "unpair", func() bool { rig.mu.Lock(); defer rig.mu.Unlock(); return rig.unpairs == 1 })
	waitFor(t, "host idle again", func() bool { return dotaIdle(t, playerAPI) == 1 })

	cancel()
	<-agentDone
}

// Ctrl+C on the player while streaming ends the session and frees the host.
func TestPlayerCancelsMidStream(t *testing.T) {
	srv := startServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rig := newRig()
	rig.streamFor = time.Hour

	hostAPI := client.New(srv.URL, "")
	hostAPI.Register(ctx, "host@example.com", "password1", "host")
	ag := agent.New(agent.Config{Name: "PC", Region: "home", Address: "10.0.0.5",
		PollInterval: 50 * time.Millisecond, HeartbeatInterval: time.Second}, hostAPI, rig, log)
	if _, err := ag.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	go ag.Run(ctx)

	playerAPI := client.New(srv.URL, "")
	playerAPI.Register(ctx, "player@example.com", "password1", "player")
	waitFor(t, "host online", func() bool { return dotaIdle(t, playerAPI) == 1 })

	playCtx, stopPlay := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- player.Play(playCtx, playerAPI, rig, player.Options{Game: "dota-2",
			Started: func(string, string) { stopPlay() }}, log)
	}()
	if err := <-done; err != nil {
		t.Fatalf("play: %v", err)
	}
	sessions, _ := playerAPI.Sessions(ctx)
	if len(sessions) != 1 || sessions[0].State != "ended" {
		t.Fatalf("sessions: %+v", sessions)
	}
	waitFor(t, "unpair", func() bool { rig.mu.Lock(); defer rig.mu.Unlock(); return rig.unpairs == 1 })
	waitFor(t, "host idle again", func() bool { return dotaIdle(t, playerAPI) == 1 })
}

type failingPair struct{ *fakeRig }

func (failingPair) Pair(context.Context, string, string) error { return errors.New("exit status 1") }

// If Moonlight cannot pair, the player gives up at once, the session is
// marked failed with the reason, and the host is freed.
func TestMoonlightPairFails(t *testing.T) {
	srv := startServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rig := newRig()

	hostAPI := client.New(srv.URL, "")
	hostAPI.Register(ctx, "host@example.com", "password1", "host")
	ag := agent.New(agent.Config{Name: "PC", Region: "home", Address: "10.0.0.5",
		PollInterval: 50 * time.Millisecond, HeartbeatInterval: time.Second}, hostAPI, rig, log)
	if _, err := ag.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	go ag.Run(ctx)

	playerAPI := client.New(srv.URL, "")
	playerAPI.Register(ctx, "player@example.com", "password1", "player")
	waitFor(t, "host online", func() bool { return dotaIdle(t, playerAPI) == 1 })

	start := time.Now()
	err := player.Play(ctx, playerAPI, failingPair{rig}, player.Options{Game: "dota-2"}, log)
	if err == nil || !strings.Contains(err.Error(), "moonlight pair") {
		t.Fatalf("play error: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("player took %s to give up", time.Since(start))
	}
	sessions, _ := playerAPI.Sessions(ctx)
	if len(sessions) != 1 || sessions[0].State != "failed" || !strings.Contains(sessions[0].FailureReason, "moonlight pair") {
		t.Fatalf("sessions: %+v", sessions)
	}
	waitFor(t, "unpair", func() bool { rig.mu.Lock(); defer rig.mu.Unlock(); return rig.unpairs == 1 })
	waitFor(t, "host idle again", func() bool { return dotaIdle(t, playerAPI) == 1 })
}
