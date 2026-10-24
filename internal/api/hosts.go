package api

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/estetsenko/fog-play/internal/store"
)

type hostRequest struct {
	Name   string       `json:"name"`
	Region string       `json:"region"`
	GPU    string       `json:"gpu"`
	CPU    string       `json:"cpu"`
	Games  []store.Game `json:"games"`
}

type gamesRequest struct {
	Games []store.Game `json:"games"`
}

type statusRequest struct {
	Status string `json:"status"`
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func validGames(games []store.Game) bool {
	for i := range games {
		games[i].Title = strings.TrimSpace(games[i].Title)
		if !slugRe.MatchString(games[i].Slug) || games[i].Title == "" {
			return false
		}
	}
	return true
}

func (s *Server) createHost(w http.ResponseWriter, r *http.Request) {
	var req hostRequest
	if !decode(w, r, &req) {
		return
	}
	req.Name, req.Region = strings.TrimSpace(req.Name), strings.TrimSpace(req.Region)
	if req.Name == "" || req.Region == "" {
		writeError(w, http.StatusBadRequest, "name and region are required")
		return
	}
	if !validGames(req.Games) {
		writeError(w, http.StatusBadRequest, "each game needs a lowercase slug and a title")
		return
	}
	h, err := s.store.CreateHost(r.Context(), store.Host{
		OwnerID: claims(r).UserID, Name: req.Name, Region: req.Region,
		GPU: req.GPU, CPU: req.CPU, Games: req.Games,
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, h)
}

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.HostsByOwner(r.Context(), claims(r).UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hosts)
}

// ownHost loads the host in the URL and checks the caller owns it.
func (s *Server) ownHost(w http.ResponseWriter, r *http.Request) (store.Host, bool) {
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		writeError(w, http.StatusNotFound, "host not found")
		return store.Host{}, false
	}
	h, err := s.store.HostByID(r.Context(), id)
	if err != nil && !isNotFound(err) {
		s.internal(w, r, err)
		return store.Host{}, false
	}
	if err != nil || h.OwnerID != claims(r).UserID {
		writeError(w, http.StatusNotFound, "host not found")
		return store.Host{}, false
	}
	return h, true
}

func (s *Server) getHost(w http.ResponseWriter, r *http.Request) {
	if h, ok := s.ownHost(w, r); ok {
		writeJSON(w, http.StatusOK, h)
	}
}

func (s *Server) replaceHostGames(w http.ResponseWriter, r *http.Request) {
	h, ok := s.ownHost(w, r)
	if !ok {
		return
	}
	var req gamesRequest
	if !decode(w, r, &req) {
		return
	}
	if !validGames(req.Games) {
		writeError(w, http.StatusBadRequest, "each game needs a lowercase slug and a title")
		return
	}
	if err := s.store.ReplaceHostGames(r.Context(), h.ID, req.Games); err != nil {
		s.internal(w, r, err)
		return
	}
	h, err := s.store.HostByID(r.Context(), h.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// heartbeat is called by the host agent every few seconds. The live status
// goes to Redis with a TTL; Postgres keeps the last reported status for history.
func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	h, ok := s.ownHost(w, r)
	if !ok {
		return
	}
	var req statusRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Status != "idle" && req.Status != "busy" && req.Status != "offline" {
		writeError(w, http.StatusBadRequest, "status must be idle, busy, or offline")
		return
	}
	if err := s.presence.Set(r.Context(), h.ID, req.Status); err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.store.TouchHost(r.Context(), h.ID, req.Status); err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
