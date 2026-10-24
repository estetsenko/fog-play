package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Sunshine is the part of the Sunshine Web API the agent uses.
type Sunshine interface {
	Apps(ctx context.Context) ([]string, error)
	// SubmitPIN completes a pending Moonlight pairing. It returns ok=false
	// while no pairing request is waiting yet, so callers can retry.
	SubmitPIN(ctx context.Context, pin, clientName string) (ok bool, err error)
	UnpairAll(ctx context.Context) error
}

// SunshineHTTP talks to Sunshine's Web UI API (default https://localhost:47990).
// Sunshine serves a self-signed certificate, so TLS verification is off; only
// point it at the Sunshine instance on this machine.
type SunshineHTTP struct {
	BaseURL  string
	Username string
	Password string
	HTTP     *http.Client
}

func NewSunshineHTTP(baseURL, user, pass string) *SunshineHTTP {
	return &SunshineHTTP{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		Username: user,
		Password: pass,
		HTTP: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		},
	}
}

var errNotFound = errors.New("not found")

func (s *SunshineHTTP) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.SetBasicAuth(s.Username, s.Password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("sunshine %s %s: wrong username or password", method, path)
	case resp.StatusCode >= 300:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("sunshine %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (s *SunshineHTTP) Apps(ctx context.Context) ([]string, error) {
	var res struct {
		Apps []struct {
			Name string `json:"name"`
		} `json:"apps"`
	}
	if err := s.do(ctx, "GET", "/api/apps", nil, &res); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(res.Apps))
	for _, a := range res.Apps {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	return names, nil
}

// statusOK reads Sunshine's {"status": ...}, which is a bool in current
// versions and was the string "true" in older ones.
type statusOK bool

func (b *statusOK) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	*b = statusOK(s == "true")
	return nil
}

func (s *SunshineHTTP) SubmitPIN(ctx context.Context, pin, clientName string) (bool, error) {
	name := clientName
	if name == "" {
		name = "fogplay-player"
	}
	body := map[string]string{"pin": pin, "name": name}

	// Sunshine 2026.x and later identify the pairing request by id and list
	// pending ones at GET /api/pin. Older versions have no such endpoint.
	var pending struct {
		Pairings []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"pairings"`
	}
	err := s.do(ctx, "GET", "/api/pin", nil, &pending)
	switch {
	case err == nil:
		if len(pending.Pairings) == 0 {
			return false, nil
		}
		pick := pending.Pairings[len(pending.Pairings)-1]
		for _, p := range pending.Pairings {
			if clientName != "" && strings.EqualFold(p.Name, clientName) {
				pick = p
			}
		}
		body["pairing_id"] = pick.ID
	case errors.Is(err, errNotFound):
	default:
		return false, err
	}

	var res struct {
		Status statusOK `json:"status"`
	}
	if err := s.do(ctx, "POST", "/api/pin", body, &res); err != nil {
		return false, err
	}
	return bool(res.Status), nil
}

func (s *SunshineHTTP) UnpairAll(ctx context.Context) error {
	return s.do(ctx, "POST", "/api/clients/unpair-all", nil, nil)
}
