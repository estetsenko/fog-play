package api

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/estetsenko/fog-play/internal/auth"
	"github.com/estetsenko/fog-play/internal/store"
)

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role,omitempty"`
}

type tokenResponse struct {
	Token string     `json:"token"`
	User  store.User `json:"user"`
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if !decode(w, r, &req) {
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if _, err := mail.ParseAddress(req.Email); err != nil {
		writeError(w, http.StatusBadRequest, "invalid email")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if req.Role == "" {
		req.Role = "player"
	}
	if req.Role != "player" && req.Role != "host" && req.Role != "both" {
		writeError(w, http.StatusBadRequest, "role must be player, host, or both")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	u, err := s.store.CreateUser(r.Context(), req.Email, hash, req.Role)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "email already registered")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.respondWithToken(w, r, http.StatusCreated, u)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if !decode(w, r, &req) {
		return
	}
	u, err := s.store.UserByEmail(r.Context(), strings.ToLower(strings.TrimSpace(req.Email)))
	if err != nil && !isNotFound(err) {
		s.internal(w, r, err)
		return
	}
	if err != nil || !auth.CheckPassword(u.PasswordHash, req.Password) {
		writeError(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	s.respondWithToken(w, r, http.StatusOK, u)
}

func (s *Server) respondWithToken(w http.ResponseWriter, r *http.Request, status int, u store.User) {
	tok, err := s.tokens.Issue(auth.Claims{UserID: u.ID, Role: u.Role})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, status, tokenResponse{Token: tok, User: u})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.UserByID(r.Context(), claims(r).UserID)
	if isNotFound(err) {
		writeError(w, http.StatusUnauthorized, "user no longer exists")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}
