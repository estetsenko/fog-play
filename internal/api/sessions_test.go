package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/estetsenko/fog-play/internal/store"
)

var dota = []store.Game{{Slug: "dota-2", Title: "Dota 2"}}

// idleHost registers a host with Dota 2 and sends an idle heartbeat.
func idleHost(t *testing.T, srv *httptest.Server, tok, region string) string {
	t.Helper()
	var h store.Host
	if code := call(t, srv, "POST", "/v1/hosts", tok, hostRequest{Name: region + " PC", Region: region, Games: dota}, &h); code != http.StatusCreated {
		t.Fatalf("create host: %d", code)
	}
	if code := call(t, srv, "PUT", "/v1/hosts/"+h.ID+"/status", tok, statusRequest{Status: "idle"}, nil); code != http.StatusNoContent {
		t.Fatalf("heartbeat: %d", code)
	}
	return h.ID
}

func idleCount(t *testing.T, srv *httptest.Server, tok string) int {
	t.Helper()
	var games []gameResponse
	call(t, srv, "GET", "/v1/games", tok, nil, &games)
	for _, g := range games {
		if g.Slug == "dota-2" {
			return g.IdleHosts
		}
	}
	return -1
}

func TestSessionMatchingAndLifecycle(t *testing.T) {
	srv := newTestServer(t)
	owner := register(t, srv, "owner@example.com", "both")
	eu := idleHost(t, srv, owner, "eu-central")
	us := idleHost(t, srv, owner, "us-east")
	p1 := register(t, srv, "p1@example.com", "player")
	p2 := register(t, srv, "p2@example.com", "player")
	p3 := register(t, srv, "p3@example.com", "player")

	// The owner cannot book their own PCs.
	if code := call(t, srv, "POST", "/v1/sessions", owner, sessionRequest{Game: "dota-2"}, nil); code != http.StatusServiceUnavailable {
		t.Errorf("owner booking own host: got %d", code)
	}
	if code := call(t, srv, "POST", "/v1/sessions", p1, sessionRequest{Game: "no-such-game"}, nil); code != http.StatusNotFound {
		t.Errorf("unknown game: got %d", code)
	}

	// p1 is closer to us-east, so gets that host.
	var s1 store.Session
	if code := call(t, srv, "POST", "/v1/sessions", p1, sessionRequest{Game: "dota-2", LatencyMS: map[string]int{"us-east": 20, "eu-central": 80}}, &s1); code != http.StatusCreated {
		t.Fatalf("p1 session: %d", code)
	}
	if s1.HostID != us || s1.State != "matched" || s1.Game != "dota-2" {
		t.Errorf("p1 session: %+v", s1)
	}
	if n := idleCount(t, srv, p1); n != 1 {
		t.Errorf("idle hosts after first match: %d", n)
	}

	// The agent still reports idle before it notices the booking; it must stay out of matching.
	call(t, srv, "PUT", "/v1/hosts/"+us+"/status", owner, statusRequest{Status: "idle"}, nil)
	if n := idleCount(t, srv, p1); n != 1 {
		t.Errorf("idle hosts after stale idle heartbeat: %d", n)
	}

	var s2 store.Session
	call(t, srv, "POST", "/v1/sessions", p2, sessionRequest{Game: "dota-2", LatencyMS: map[string]int{"us-east": 20}}, &s2)
	if s2.HostID != eu {
		t.Errorf("p2 should fall back to eu host, got %+v", s2)
	}
	if code := call(t, srv, "POST", "/v1/sessions", p3, sessionRequest{Game: "dota-2"}, nil); code != http.StatusServiceUnavailable {
		t.Errorf("no hosts left: got %d", code)
	}

	// The host agent finds its booking.
	var booked store.Session
	if code := call(t, srv, "GET", "/v1/hosts/"+us+"/session", owner, nil, &booked); code != http.StatusOK || booked.ID != s1.ID {
		t.Errorf("host session: %d %+v", code, booked)
	}

	// Strangers cannot see or change it.
	if code := call(t, srv, "GET", "/v1/sessions/"+s1.ID, p3, nil, nil); code != http.StatusNotFound {
		t.Errorf("stranger reading session: got %d", code)
	}
	if code := call(t, srv, "DELETE", "/v1/sessions/"+s1.ID, p3, nil, nil); code != http.StatusNotFound {
		t.Errorf("stranger ending session: got %d", code)
	}

	// Lifecycle: matched -> connecting (host) -> active (player) -> ended.
	if code := call(t, srv, "PUT", "/v1/sessions/"+s1.ID+"/state", p1, stateRequest{State: "active"}, nil); code != http.StatusConflict {
		t.Errorf("skip to active: got %d", code)
	}
	if code := call(t, srv, "PUT", "/v1/sessions/"+s1.ID+"/state", owner, stateRequest{State: "connecting"}, nil); code != http.StatusOK {
		t.Errorf("connecting: got %d", code)
	}
	var active store.Session
	call(t, srv, "PUT", "/v1/sessions/"+s1.ID+"/state", p1, stateRequest{State: "active"}, &active)
	if active.State != "active" || active.StartedAt == nil {
		t.Errorf("active: %+v", active)
	}
	var ended store.Session
	if code := call(t, srv, "DELETE", "/v1/sessions/"+s1.ID, p1, nil, &ended); code != http.StatusOK {
		t.Fatalf("end: got %d", code)
	}
	if ended.State != "ended" || ended.EndedAt == nil || ended.Minutes != 1 {
		t.Errorf("ended: %+v", ended)
	}
	if code := call(t, srv, "DELETE", "/v1/sessions/"+s1.ID, p1, nil, nil); code != http.StatusConflict {
		t.Errorf("end twice: got %d", code)
	}
	if code := call(t, srv, "GET", "/v1/hosts/"+us+"/session", owner, nil, nil); code != http.StatusNoContent {
		t.Errorf("host session after end: got %d", code)
	}

	// The freed host is matchable again.
	var s3 store.Session
	if code := call(t, srv, "POST", "/v1/sessions", p3, sessionRequest{Game: "dota-2"}, &s3); code != http.StatusCreated || s3.HostID != us {
		t.Errorf("p3 after host freed: %d %+v", code, s3)
	}

	var history []store.Session
	call(t, srv, "GET", "/v1/sessions", p1, nil, &history)
	if len(history) != 1 || history[0].ID != s1.ID {
		t.Errorf("history: %+v", history)
	}
}

func TestSessionFailureReason(t *testing.T) {
	srv := newTestServer(t)
	owner := register(t, srv, "owner@example.com", "host")
	idleHost(t, srv, owner, "eu-central")
	p := register(t, srv, "p@example.com", "player")

	var s store.Session
	call(t, srv, "POST", "/v1/sessions", p, sessionRequest{Game: "dota-2"}, &s)
	var failed store.Session
	call(t, srv, "PUT", "/v1/sessions/"+s.ID+"/state", owner, stateRequest{State: "failed", Reason: "ICE negotiation failed"}, &failed)
	if failed.State != "failed" || failed.FailureReason != "ICE negotiation failed" || failed.Minutes != 0 {
		t.Errorf("failed: %+v", failed)
	}
	if code := call(t, srv, "PUT", "/v1/sessions/"+s.ID+"/state", owner, stateRequest{State: "paused"}, nil); code != http.StatusBadRequest {
		t.Errorf("unknown state: got %d", code)
	}
}

func TestReapStaleSessions(t *testing.T) {
	srv, api := newTestEnv(t)
	owner := register(t, srv, "owner@example.com", "host")
	idleHost(t, srv, owner, "eu-central")
	p := register(t, srv, "p@example.com", "player")

	var s store.Session
	call(t, srv, "POST", "/v1/sessions", p, sessionRequest{Game: "dota-2"}, &s)
	if n := idleCount(t, srv, p); n != 0 {
		t.Fatalf("idle before reap: %d", n)
	}
	n, err := api.ReapStaleSessions(context.Background(), 0)
	if err != nil || n != 1 {
		t.Fatalf("reap: %d %v", n, err)
	}
	var got store.Session
	call(t, srv, "GET", "/v1/sessions/"+s.ID, p, nil, &got)
	if got.State != "failed" || got.FailureReason == "" {
		t.Errorf("reaped session: %+v", got)
	}
	if n := idleCount(t, srv, p); n != 1 {
		t.Errorf("idle after reap: %d", n)
	}
}

// Many players racing for one host: exactly one gets it.
func TestSessionRace(t *testing.T) {
	srv := newTestServer(t)
	owner := register(t, srv, "owner@example.com", "host")
	idleHost(t, srv, owner, "eu-central")

	const players = 10
	toks := make([]string, players)
	for i := range toks {
		toks[i] = register(t, srv, fmt.Sprintf("p%d@example.com", i), "player")
	}
	var wg sync.WaitGroup
	codes := make([]int, players)
	for i := range toks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = call(t, srv, "POST", "/v1/sessions", toks[i], sessionRequest{Game: "dota-2"}, nil)
		}(i)
	}
	wg.Wait()
	won := 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			won++
		case http.StatusServiceUnavailable:
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if won != 1 {
		t.Errorf("%d players got the host, want 1 (%v)", won, codes)
	}
}
