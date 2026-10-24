package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeSunshine mimics the Web UI API. newAPI switches on the 2026+ pairing
// flow (GET /api/pin lists pending requests; POST needs pairing_id).
func fakeSunshine(t *testing.T, newAPI bool, pending []map[string]string, got *map[string]string) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "admin" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			w.Write([]byte(`{"apps":[{"name":"Desktop"},{"name":"Steam Big Picture"}]}`))
		case r.Method == "GET" && r.URL.Path == "/api/pin" && newAPI:
			json.NewEncoder(w).Encode(map[string]any{"pairings": pending})
		case r.Method == "POST" && r.URL.Path == "/api/pin":
			json.NewDecoder(r.Body).Decode(got)
			if newAPI && (*got)["pairing_id"] == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if newAPI {
				w.Write([]byte(`{"status":true}`))
			} else {
				w.Write([]byte(`{"status":"true"}`))
			}
		case r.Method == "POST" && r.URL.Path == "/api/clients/unpair-all":
			w.Write([]byte(`{"status":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestSunshineNewPairingAPI(t *testing.T) {
	var got map[string]string
	srv := fakeSunshine(t, true, []map[string]string{
		{"id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "name": "other-pc"},
		{"id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "name": "player-pc"},
		{"id": "cccccccccccccccccccccccccccccccc", "name": "third"},
	}, &got)
	defer srv.Close()
	s := NewSunshineHTTP(srv.URL, "admin", "secret")
	ok, err := s.SubmitPIN(context.Background(), "1234", "player-pc")
	if err != nil || !ok {
		t.Fatalf("submit: %v %v", ok, err)
	}
	if got["pairing_id"] != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || got["pin"] != "1234" {
		t.Errorf("sent %v", got)
	}
}

func TestSunshineNewPairingAPINothingPending(t *testing.T) {
	var got map[string]string
	srv := fakeSunshine(t, true, nil, &got)
	defer srv.Close()
	ok, err := NewSunshineHTTP(srv.URL, "admin", "secret").SubmitPIN(context.Background(), "1234", "pc")
	if ok || err != nil || got != nil {
		t.Errorf("ok=%v err=%v sent=%v", ok, err, got)
	}
}

func TestSunshineOldPairingAPI(t *testing.T) {
	var got map[string]string
	srv := fakeSunshine(t, false, nil, &got)
	defer srv.Close()
	s := NewSunshineHTTP(srv.URL, "admin", "secret")
	ok, err := s.SubmitPIN(context.Background(), "0042", "")
	if err != nil || !ok {
		t.Fatalf("submit: %v %v", ok, err)
	}
	if got["pin"] != "0042" || got["name"] == "" || got["pairing_id"] != "" {
		t.Errorf("sent %v", got)
	}
	apps, err := s.Apps(context.Background())
	if err != nil || len(apps) != 2 || Slug(apps[1]) != "steam-big-picture" {
		t.Errorf("apps %v %v", apps, err)
	}
	if err := s.UnpairAll(context.Background()); err != nil {
		t.Error(err)
	}
}

func TestSunshineWrongPassword(t *testing.T) {
	var got map[string]string
	srv := fakeSunshine(t, false, nil, &got)
	defer srv.Close()
	if _, err := NewSunshineHTTP(srv.URL, "admin", "nope").Apps(context.Background()); err == nil {
		t.Error("expected an error")
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"Dota 2": "dota-2", "  Cyberpunk 2077!! ": "cyberpunk-2077", "Steam Big Picture": "steam-big-picture", "!!!": ""} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
