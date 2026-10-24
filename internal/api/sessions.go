package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/estetsenko/fog-play/internal/match"
	"github.com/estetsenko/fog-play/internal/store"
)

type sessionRequest struct {
	Game string `json:"game"`
	// LatencyMS is the round trip the client measured to each region it probed.
	LatencyMS map[string]int `json:"latency_ms"`
}

type stateRequest struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// allowedFrom lists, for each target state, the states a session may leave to reach it.
var allowedFrom = map[string][]string{
	"connecting": {"matched"},
	"active":     {"connecting"},
	"ended":      {"matched", "connecting", "active"},
	"failed":     {"matched", "connecting", "active"},
}

// createSession matches the player to the best idle host that has the game
// and books it. If a host is taken by a concurrent request, it tries the next.
func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req sessionRequest
	if !decode(w, r, &req) {
		return
	}
	if !slugRe.MatchString(req.Game) {
		writeError(w, http.StatusBadRequest, "game must be a catalog slug")
		return
	}
	playerID := claims(r).UserID
	gameID, cands, err := s.store.MatchCandidates(r.Context(), req.Game, playerID)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "game not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ids := make([]string, len(cands))
	for i, c := range cands {
		ids[i] = c.HostID
	}
	statuses, err := s.presence.Statuses(r.Context(), ids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	idle := cands[:0]
	for _, c := range cands {
		if statuses[c.HostID] == "idle" {
			idle = append(idle, c)
		}
	}
	for _, c := range match.Rank(idle, req.LatencyMS) {
		sess, err := s.store.CreateSession(r.Context(), playerID, c.HostID, gameID)
		if errors.Is(err, store.ErrHostTaken) {
			continue
		}
		if errors.Is(err, store.ErrPlayerBusy) {
			writeError(w, http.StatusConflict, "you already have a live session")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if err := s.presence.Update(r.Context(), c.HostID, "busy"); err != nil {
			s.log.Warn("mark host busy", "host_id", c.HostID, "err", err)
		}
		writeJSON(w, http.StatusCreated, sess)
		return
	}
	writeError(w, http.StatusServiceUnavailable, "no idle host has this game right now")
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.SessionsByPlayer(r.Context(), claims(r).UserID, 50)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// visibleSession loads the session in the URL if the caller is its player or the host's owner.
func (s *Server) visibleSession(w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		writeError(w, http.StatusNotFound, "session not found")
		return store.Session{}, false
	}
	sess, err := s.store.SessionByID(r.Context(), id)
	if err != nil && !isNotFound(err) {
		s.internal(w, r, err)
		return store.Session{}, false
	}
	uid := claims(r).UserID
	if err != nil || (sess.PlayerID != uid && sess.HostOwnerID != uid) {
		writeError(w, http.StatusNotFound, "session not found")
		return store.Session{}, false
	}
	return sess, true
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	if sess, ok := s.visibleSession(w, r); ok {
		writeJSON(w, http.StatusOK, sess)
	}
}

func (s *Server) updateSessionState(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.visibleSession(w, r)
	if !ok {
		return
	}
	var req stateRequest
	if !decode(w, r, &req) {
		return
	}
	s.transition(w, r, sess, req.State, req.Reason)
}

// endSession ends the session from either side, whatever live state it is in.
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) {
	if sess, ok := s.visibleSession(w, r); ok {
		s.transition(w, r, sess, "ended", "")
	}
}

func (s *Server) transition(w http.ResponseWriter, r *http.Request, sess store.Session, to, reason string) {
	from, ok := allowedFrom[to]
	if !ok {
		writeError(w, http.StatusBadRequest, "state must be connecting, active, ended, or failed")
		return
	}
	if len(reason) > 500 {
		reason = reason[:500]
	}
	if to != "failed" {
		reason = ""
	}
	updated, err := s.store.TransitionSession(r.Context(), sess.ID, from, to, reason)
	if errors.Is(err, store.ErrBadTransition) {
		writeError(w, http.StatusConflict, "session is "+updated.State+" and cannot become "+to)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if to == "ended" || to == "failed" {
		if err := s.presence.Update(r.Context(), sess.HostID, "idle"); err != nil {
			s.log.Warn("mark host idle", "host_id", sess.HostID, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, updated)
}

// hostSession is polled by the host agent to learn about a session booked on it.
func (s *Server) hostSession(w http.ResponseWriter, r *http.Request) {
	h, ok := s.ownHost(w, r)
	if !ok {
		return
	}
	sess, err := s.store.LiveHostSession(r.Context(), h.ID)
	if isNotFound(err) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// ReapStaleSessions fails sessions that did not start streaming within maxAge
// and frees their hosts. It returns how many sessions it failed.
func (s *Server) ReapStaleSessions(ctx context.Context, maxAge time.Duration) (int, error) {
	hosts, err := s.store.ExpireStaleSessions(ctx, maxAge)
	if err != nil {
		return 0, err
	}
	for _, id := range hosts {
		if err := s.presence.Update(ctx, id, "idle"); err != nil {
			s.log.Warn("mark host idle", "host_id", id, "err", err)
		}
	}
	return len(hosts), nil
}
