package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/estetsenko/fog-play/internal/auth"
	"github.com/estetsenko/fog-play/internal/db"
	"github.com/estetsenko/fog-play/internal/presence"
	"github.com/estetsenko/fog-play/internal/store"
)

// newTestServer needs a real Postgres and Redis; set TEST_DATABASE_URL and
// TEST_REDIS_URL (see docker-compose.yml). The tests wipe both.
func newTestServer(t *testing.T) *httptest.Server {
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
	if _, err := pool.Exec(ctx, `TRUNCATE users, hosts, games, host_games CASCADE`); err != nil {
		t.Fatal(err)
	}
	pres, err := presence.NewRedis(redisURL, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pres.Close() })

	tokens := auth.NewIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(store.New(pool), pres, tokens, log).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path, token string, body any, out any) int {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, srv.URL+path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func register(t *testing.T, srv *httptest.Server, email, role string) string {
	t.Helper()
	var res tokenResponse
	if code := call(t, srv, "POST", "/v1/auth/register", "", credentials{Email: email, Password: "password1", Role: role}, &res); code != http.StatusCreated {
		t.Fatalf("register %s: %d", email, code)
	}
	return res.Token
}

func TestAuth(t *testing.T) {
	srv := newTestServer(t)
	register(t, srv, "Ann@Example.com", "player")

	if code := call(t, srv, "POST", "/v1/auth/register", "", credentials{Email: "ann@example.com", Password: "password1"}, nil); code != http.StatusConflict {
		t.Errorf("duplicate email: got %d", code)
	}
	if code := call(t, srv, "POST", "/v1/auth/login", "", credentials{Email: "ann@example.com", Password: "wrong-pass"}, nil); code != http.StatusUnauthorized {
		t.Errorf("wrong password: got %d", code)
	}
	var res tokenResponse
	if code := call(t, srv, "POST", "/v1/auth/login", "", credentials{Email: "ann@example.com", Password: "password1"}, &res); code != http.StatusOK {
		t.Fatalf("login: got %d", code)
	}
	var me store.User
	if code := call(t, srv, "GET", "/v1/me", res.Token, nil, &me); code != http.StatusOK || me.Email != "ann@example.com" || me.Role != "player" {
		t.Errorf("me: %d %+v", code, me)
	}
	if code := call(t, srv, "GET", "/v1/me", "garbage", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("bad token: got %d", code)
	}
}

func TestHostLifecycle(t *testing.T) {
	srv := newTestServer(t)
	hostTok := register(t, srv, "host@example.com", "host")
	playerTok := register(t, srv, "player@example.com", "player")
	otherHostTok := register(t, srv, "other@example.com", "both")

	req := hostRequest{Name: "Living room PC", Region: "eu-central", GPU: "RTX 4080", CPU: "Ryzen 7",
		Games: []store.Game{{Slug: "cyberpunk-2077", Title: "Cyberpunk 2077"}, {Slug: "dota-2", Title: "Dota 2"}}}

	if code := call(t, srv, "POST", "/v1/hosts", playerTok, req, nil); code != http.StatusForbidden {
		t.Errorf("player registering host: got %d", code)
	}
	var h store.Host
	if code := call(t, srv, "POST", "/v1/hosts", hostTok, req, &h); code != http.StatusCreated {
		t.Fatalf("create host: got %d", code)
	}
	if h.Status != "offline" || len(h.Games) != 2 {
		t.Errorf("new host: %+v", h)
	}

	// Another host owner cannot see or drive this host.
	if code := call(t, srv, "PUT", "/v1/hosts/"+h.ID+"/status", otherHostTok, statusRequest{Status: "idle"}, nil); code != http.StatusNotFound {
		t.Errorf("foreign heartbeat: got %d", code)
	}
	if code := call(t, srv, "GET", "/v1/hosts/not-a-uuid", hostTok, nil, nil); code != http.StatusNotFound {
		t.Errorf("bad id: got %d", code)
	}

	var games []gameResponse
	call(t, srv, "GET", "/v1/games", playerTok, nil, &games)
	if len(games) != 2 || games[0].IdleHosts != 0 {
		t.Errorf("catalog before heartbeat: %+v", games)
	}

	if code := call(t, srv, "PUT", "/v1/hosts/"+h.ID+"/status", hostTok, statusRequest{Status: "idle"}, nil); code != http.StatusNoContent {
		t.Fatalf("heartbeat: got %d", code)
	}
	call(t, srv, "GET", "/v1/games", playerTok, nil, &games)
	for _, g := range games {
		if g.IdleHosts != 1 || g.OnlineHosts != 1 {
			t.Errorf("catalog after idle heartbeat: %+v", g)
		}
	}

	call(t, srv, "PUT", "/v1/hosts/"+h.ID+"/status", hostTok, statusRequest{Status: "busy"}, nil)
	call(t, srv, "GET", "/v1/games", playerTok, nil, &games)
	if games[0].IdleHosts != 0 || games[0].OnlineHosts != 1 {
		t.Errorf("catalog after busy heartbeat: %+v", games[0])
	}

	// Replacing games drops Dota 2 from this host; the catalog keeps the title.
	var updated store.Host
	call(t, srv, "PUT", "/v1/hosts/"+h.ID+"/games", hostTok, gamesRequest{Games: []store.Game{{Slug: "cyberpunk-2077", Title: "Cyberpunk 2077"}}}, &updated)
	if len(updated.Games) != 1 || updated.Status != "busy" || updated.LastSeenAt == nil {
		t.Errorf("after replace: %+v", updated)
	}

	call(t, srv, "PUT", "/v1/hosts/"+h.ID+"/status", hostTok, statusRequest{Status: "offline"}, nil)
	call(t, srv, "GET", "/v1/games", playerTok, nil, &games)
	for _, g := range games {
		if g.OnlineHosts != 0 {
			t.Errorf("catalog after offline: %+v", g)
		}
	}

	var mine []store.Host
	call(t, srv, "GET", "/v1/hosts", hostTok, nil, &mine)
	if len(mine) != 1 {
		t.Errorf("own hosts: %+v", mine)
	}
	call(t, srv, "GET", "/v1/hosts", otherHostTok, nil, &mine)
	if len(mine) != 0 {
		t.Errorf("other owner's hosts: %+v", mine)
	}
}

func TestValidation(t *testing.T) {
	srv := newTestServer(t)
	cases := []credentials{
		{Email: "not-an-email", Password: "password1"},
		{Email: "a@b.co", Password: "short"},
		{Email: "a@b.co", Password: "password1", Role: "admin"},
	}
	for _, c := range cases {
		if code := call(t, srv, "POST", "/v1/auth/register", "", c, nil); code != http.StatusBadRequest {
			t.Errorf("%+v: got %d", c, code)
		}
	}
	tok := register(t, srv, "h@example.com", "host")
	bad := hostRequest{Name: "PC", Region: "eu", Games: []store.Game{{Slug: "Bad Slug", Title: "x"}}}
	if code := call(t, srv, "POST", "/v1/hosts", tok, bad, nil); code != http.StatusBadRequest {
		t.Errorf("bad slug: got %d", code)
	}
}
