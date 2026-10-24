// Package api is the public HTTP API of the Fog Play control plane.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/estetsenko/fog-play/internal/auth"
	"github.com/estetsenko/fog-play/internal/signal"
	"github.com/estetsenko/fog-play/internal/store"
)

// Presence is the live host status store (Redis in production).
type Presence interface {
	Set(ctx context.Context, hostID, status string) error
	Update(ctx context.Context, hostID, status string) error
	Statuses(ctx context.Context, hostIDs []string) (map[string]string, error)
	Ping(ctx context.Context) error
}

type Server struct {
	store    *store.Store
	presence Presence
	tokens   *auth.Issuer
	hub      *signal.Hub
	log      *slog.Logger
}

func New(st *store.Store, p Presence, tokens *auth.Issuer, log *slog.Logger) *Server {
	return &Server{store: st, presence: p, tokens: tokens, hub: signal.NewHub(), log: log}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)

	// Long-lived WebSocket; must not get the request timeout below. Browsers
	// can't set headers on a WebSocket, so the token may come in the query.
	r.With(tokenFromQuery, s.authenticate).Get("/v1/sessions/{id}/signal", s.signal)

	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(15 * time.Second))
		r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		r.Get("/readyz", s.ready)
		r.Get("/play", page("play.html", "text/html; charset=utf-8"))
		r.Get("/play.js", page("play.js", "text/javascript; charset=utf-8"))

		r.Route("/v1", func(r chi.Router) {
			r.Post("/auth/register", s.register)
			r.Post("/auth/login", s.login)

			r.Group(func(r chi.Router) {
				r.Use(s.authenticate)
				r.Get("/me", s.me)
				r.Get("/games", s.listGames)

				r.Post("/sessions", s.createSession)
				r.Get("/sessions", s.listSessions)
				r.Get("/sessions/{id}", s.getSession)
				r.Put("/sessions/{id}/state", s.updateSessionState)
				r.Delete("/sessions/{id}", s.endSession)

				r.Group(func(r chi.Router) {
					r.Use(requireRole("host", "both"))
					r.Post("/hosts", s.createHost)
					r.Get("/hosts", s.listHosts)
					r.Get("/hosts/{id}", s.getHost)
					r.Put("/hosts/{id}/games", s.replaceHostGames)
					r.Put("/hosts/{id}/status", s.heartbeat)
					r.Get("/hosts/{id}/session", s.hostSession)
				})
			})
		})
	})
	return r
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if err := s.presence.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "redis unavailable")
		return
	}
	w.WriteHeader(http.StatusOK)
}

type ctxKey struct{}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		c, err := s.tokens.Parse(tok)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c)))
	})
}

// tokenFromQuery moves an access_token query parameter into the
// Authorization header when there is none.
func tokenFromQuery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t := r.URL.Query().Get("access_token"); t != "" && r.Header.Get("Authorization") == "" {
			r.Header.Set("Authorization", "Bearer "+t)
		}
		next.ServeHTTP(w, r)
	})
}

func claims(r *http.Request) auth.Claims {
	c, _ := r.Context().Value(ctxKey{}).(auth.Claims)
	return c
}

func requireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := claims(r).Role
			for _, want := range roles {
				if role == want {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeError(w, http.StatusForbidden, "role not allowed")
		})
	}
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) internal(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "request_id", middleware.GetReqID(r.Context()), "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
