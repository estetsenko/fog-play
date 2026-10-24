// Package e2e runs the server, a host agent and a player in one process to
// check the whole session flow. TestStreamToPlayer streams real H.264 from
// FFmpeg to a Go WebRTC peer standing in for the browser.
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"

	"github.com/estetsenko/fog-play/internal/agent"
	"github.com/estetsenko/fog-play/internal/api"
	"github.com/estetsenko/fog-play/internal/auth"
	"github.com/estetsenko/fog-play/internal/client"
	"github.com/estetsenko/fog-play/internal/db"
	"github.com/estetsenko/fog-play/internal/player"
	"github.com/estetsenko/fog-play/internal/presence"
	"github.com/estetsenko/fog-play/internal/signal"
	"github.com/estetsenko/fog-play/internal/store"
	"github.com/estetsenko/fog-play/internal/stream"
)

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
	deadline := time.Now().Add(15 * time.Second)
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

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// rig is a server with one online host serving Dota 2 with st, and a
// signed-in player.
type rig struct {
	srv    *httptest.Server
	player *client.Client
	ctx    context.Context
}

func newRig(t *testing.T, st agent.Streamer) *rig {
	srv := startServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	hostAPI := client.New(srv.URL, "")
	if err := hostAPI.Register(ctx, "host@example.com", "password1", "host"); err != nil {
		t.Fatal(err)
	}
	ag := agent.New(agent.Config{Name: "Test PC", Region: "home", Apps: []agent.App{{Name: "Dota 2"}},
		PollInterval: 50 * time.Millisecond, HeartbeatInterval: time.Second}, hostAPI, st, quiet)
	if _, err := ag.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { ag.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	playerAPI := client.New(srv.URL, "")
	if err := playerAPI.Register(ctx, "player@example.com", "password1", "player"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "host online", func() bool { return dotaIdle(t, playerAPI) == 1 })
	return &rig{srv: srv, player: playerAPI, ctx: ctx}
}

func (r *rig) onlySession(t *testing.T) store.Session {
	t.Helper()
	sessions, err := r.player.Sessions(r.ctx)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions: %v %+v", err, sessions)
	}
	return sessions[0]
}

// fakeStreamer serves until the session ends, or fails with err at once.
type fakeStreamer struct {
	err    error
	served atomic.Int32
}

func (f *fakeStreamer) Serve(ctx context.Context, _ stream.Signal) error {
	f.served.Add(1)
	if f.err != nil {
		return f.err
	}
	<-ctx.Done()
	return nil
}

// The browser marks the session active and later presses End; Play notices
// and returns, and the host goes back to idle.
func TestPlayEndsWhenBrowserEnds(t *testing.T) {
	st := &fakeStreamer{}
	r := newRig(t, st)
	var opened string
	err := player.Play(r.ctx, r.player, player.Options{
		Game:         "dota-2",
		PollInterval: 50 * time.Millisecond,
		Open: func(url string) error {
			opened = url
			go func() {
				// What play.js does: active once connected, DELETE on End.
				s := r.onlySession(t)
				waitFor(t, "host accepts", func() bool { s, _ = r.player.Session(r.ctx, s.ID); return s.State == "connecting" })
				r.player.SetSessionState(r.ctx, s.ID, "active", "")
				r.player.EndSession(r.ctx, s.ID)
			}()
			return nil
		},
	}, quiet)
	if err != nil {
		t.Fatalf("play: %v", err)
	}
	s := r.onlySession(t)
	if want := r.srv.URL + "/play#s=" + s.ID + "&t="; !strings.HasPrefix(opened, want) {
		t.Errorf("opened %q, want prefix %q", opened, want)
	}
	if s.State != "ended" || s.Minutes != 1 || s.StartedAt == nil {
		t.Errorf("session after play: %+v", s)
	}
	waitFor(t, "host idle again", func() bool { return dotaIdle(t, r.player) == 1 })
	if st.served.Load() != 1 {
		t.Errorf("served %d times", st.served.Load())
	}
}

// Ctrl+C on the player ends the session and frees the host.
func TestPlayerCancels(t *testing.T) {
	r := newRig(t, &fakeStreamer{})
	ctx, stop := context.WithCancel(r.ctx)
	err := player.Play(ctx, r.player, player.Options{Game: "dota-2", PollInterval: 50 * time.Millisecond,
		Open: func(string) error { time.AfterFunc(300*time.Millisecond, stop); return nil }}, quiet)
	if err != nil {
		t.Fatalf("play: %v", err)
	}
	if s := r.onlySession(t); s.State != "ended" {
		t.Fatalf("session: %+v", s)
	}
	waitFor(t, "host idle again", func() bool { return dotaIdle(t, r.player) == 1 })
}

// If the host cannot stream, the session fails with the reason and the
// player reports it.
func TestHostStreamFails(t *testing.T) {
	r := newRig(t, &fakeStreamer{err: errors.New("ffmpeg stopped: no display")})
	err := player.Play(r.ctx, r.player, player.Options{Game: "dota-2", PollInterval: 50 * time.Millisecond,
		Open: func(string) error { return nil }}, quiet)
	if err == nil || !strings.Contains(err.Error(), "no display") {
		t.Fatalf("play error: %v", err)
	}
	if s := r.onlySession(t); s.State != "failed" || !strings.Contains(s.FailureReason, "stream failed on the host") {
		t.Fatalf("session: %+v", s)
	}
	waitFor(t, "host idle again", func() bool { return dotaIdle(t, r.player) == 1 })
}

func TestPlayPageServed(t *testing.T) {
	srv := startServer(t)
	for path, ctype := range map[string]string{"/play": "text/html", "/play.js": "text/javascript"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), ctype) || resp.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("%s: %d %v", path, resp.StatusCode, resp.Header)
		}
	}
}

// recorder is an Injector that keeps what it gets.
type recorder struct {
	mu     sync.Mutex
	events []stream.InputEvent
}

func (r *recorder) Inject(e stream.InputEvent) error {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
	return nil
}

func (r *recorder) len() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.events) }

// goPlayer does what play.js does, with Pion: says hello, answers the
// offer, counts video frames and sends input on the data channel.
type goPlayer struct {
	pc     *webrtc.PeerConnection
	frames atomic.Int64
	input  chan *webrtc.DataChannel
}

// dialBrowserStyle connects to the relay with the token in the query, as a
// browser has to.
func dialBrowserStyle(ctx context.Context, t *testing.T, srvURL, token, sessionID string) *websocket.Conn {
	u := "ws" + strings.TrimPrefix(srvURL, "http") + "/v1/sessions/" + sessionID + "/signal?access_token=" + token
	conn, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		t.Fatalf("dial signal: %v", err)
	}
	return conn
}

func send(ctx context.Context, t *testing.T, conn *websocket.Conn, typ string, data any) {
	b, _ := json.Marshal(data)
	m, _ := json.Marshal(signal.Message{Type: typ, Data: b})
	if err := conn.Write(ctx, websocket.MessageText, m); err != nil {
		t.Fatal(err)
	}
}

func connectGoPlayer(ctx context.Context, t *testing.T, conn *websocket.Conn) *goPlayer {
	send(ctx, t, conn, signal.TypeHello, struct{}{})
	var offer signal.SDP
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for offer: %v", err)
		}
		var m signal.Message
		json.Unmarshal(b, &m)
		if m.Type == signal.TypeOffer {
			json.Unmarshal(m.Data, &offer)
			break
		}
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	p := &goPlayer{pc: pc, input: make(chan *webrtc.DataChannel, 1)}
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if !strings.EqualFold(tr.Codec().MimeType, webrtc.MimeTypeH264) {
			t.Errorf("codec %s", tr.Codec().MimeType)
		}
		for {
			pkt, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			if pkt.Marker {
				p.frames.Add(1)
			}
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnOpen(func() { p.input <- dc })
	})
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}); err != nil {
		t.Fatal(err)
	}
	ans, err := pc.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	pc.SetLocalDescription(ans)
	<-gathered
	send(ctx, t, conn, signal.TypeAnswer, signal.SDP{SDP: pc.LocalDescription().SDP})
	return p
}

// The full path: FFmpeg encodes a test pattern on the host, frames reach
// the player over WebRTC, the player's keys reach the host, and a reloaded
// page gets a fresh connection to the same stream.
func TestStreamToPlayer(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	var x264 stream.Encoder
	for _, e := range stream.Encoders {
		if e.Name == "libx264" {
			x264 = e
		}
	}
	rec := &recorder{}
	host := &stream.Host{FFmpeg: ffmpeg, Encoder: x264, Capture: stream.TestCapture(320, 240, 30),
		Bitrate: 1_000_000, GOP: 30, Input: rec, Log: quiet}
	r := newRig(t, host)

	sess, err := r.player.CreateSession(r.ctx, "dota-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()

	conn := dialBrowserStyle(ctx, t, r.srv.URL, r.player.Token, sess.ID)
	p := connectGoPlayer(ctx, t, conn)
	waitFor(t, "video frames", func() bool { return p.frames.Load() >= 15 })
	dc := <-p.input
	for _, e := range []string{`{"t":"kd","c":"KeyW"}`, `{"t":"ku","c":"KeyW"}`, `{"t":"mm","x":5,"y":-3}`} {
		if err := dc.SendText(e); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "input on host", func() bool { return rec.len() == 3 })
	rec.mu.Lock()
	if e := rec.events[0]; e.Type != "kd" || e.Code != "KeyW" {
		t.Errorf("first event %+v", e)
	}
	if e := rec.events[2]; e.Type != "mm" || e.X != 5 || e.Y != -3 {
		t.Errorf("mouse event %+v", e)
	}
	rec.mu.Unlock()
	if err := r.player.SetSessionState(ctx, sess.ID, "active", ""); err != nil {
		t.Fatal(err)
	}

	// Reload: a new page connection replaces the old one and streams again.
	conn.CloseNow()
	conn2 := dialBrowserStyle(ctx, t, r.srv.URL, r.player.Token, sess.ID)
	p2 := connectGoPlayer(ctx, t, conn2)
	waitFor(t, "video frames after reload", func() bool { return p2.frames.Load() >= 15 })

	if err := r.player.EndSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "host idle again", func() bool { return dotaIdle(t, r.player) == 1 })
	if s := r.onlySession(t); s.State != "ended" {
		t.Errorf("session: %+v", s)
	}
}
