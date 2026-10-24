// Package client is a Go client for the Fog Play API, used by the host
// agent and the player CLI.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/estetsenko/fog-play/internal/signal"
	"github.com/estetsenko/fog-play/internal/store"
)

// APIError is a non-2xx response from the server.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("fogplay api: %d %s", e.Status, e.Message) }

// IsStatus reports whether err is an APIError with the given HTTP status.
func IsStatus(err error, status int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == status
}

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		return &APIError{Status: resp.StatusCode, Message: e.Error}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type authResponse struct {
	Token string     `json:"token"`
	User  store.User `json:"user"`
}

// Register creates an account and keeps its token on the client.
func (c *Client) Register(ctx context.Context, email, password, role string) error {
	var res authResponse
	if err := c.do(ctx, "POST", "/v1/auth/register", map[string]string{"email": email, "password": password, "role": role}, &res); err != nil {
		return err
	}
	c.Token = res.Token
	return nil
}

// Login signs in and keeps the token on the client.
func (c *Client) Login(ctx context.Context, email, password string) error {
	var res authResponse
	if err := c.do(ctx, "POST", "/v1/auth/login", map[string]string{"email": email, "password": password}, &res); err != nil {
		return err
	}
	c.Token = res.Token
	return nil
}

type Game struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	IdleHosts   int    `json:"idle_hosts"`
	OnlineHosts int    `json:"online_hosts"`
}

func (c *Client) Games(ctx context.Context) ([]Game, error) {
	var out []Game
	return out, c.do(ctx, "GET", "/v1/games", nil, &out)
}

type HostSpec struct {
	Name   string       `json:"name"`
	Region string       `json:"region"`
	GPU    string       `json:"gpu"`
	CPU    string       `json:"cpu"`
	Games  []store.Game `json:"games"`
}

func (c *Client) CreateHost(ctx context.Context, h HostSpec) (store.Host, error) {
	var out store.Host
	return out, c.do(ctx, "POST", "/v1/hosts", h, &out)
}

func (c *Client) Host(ctx context.Context, id string) (store.Host, error) {
	var out store.Host
	return out, c.do(ctx, "GET", "/v1/hosts/"+url.PathEscape(id), nil, &out)
}

func (c *Client) SetHostGames(ctx context.Context, id string, games []store.Game) error {
	return c.do(ctx, "PUT", "/v1/hosts/"+url.PathEscape(id)+"/games", map[string]any{"games": games}, nil)
}

func (c *Client) Heartbeat(ctx context.Context, id, status string) error {
	return c.do(ctx, "PUT", "/v1/hosts/"+url.PathEscape(id)+"/status", map[string]string{"status": status}, nil)
}

// HostSession returns the live session booked on the host, or nil if none.
func (c *Client) HostSession(ctx context.Context, hostID string) (*store.Session, error) {
	var out store.Session
	var raw json.RawMessage
	if err := c.do(ctx, "GET", "/v1/hosts/"+url.PathEscape(hostID)+"/session", nil, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	return &out, json.Unmarshal(raw, &out)
}

func (c *Client) CreateSession(ctx context.Context, game string, latencyMS map[string]int) (store.Session, error) {
	var out store.Session
	return out, c.do(ctx, "POST", "/v1/sessions", map[string]any{"game": game, "latency_ms": latencyMS}, &out)
}

// Sessions lists the caller's recent sessions as a player, newest first.
func (c *Client) Sessions(ctx context.Context) ([]store.Session, error) {
	var out []store.Session
	return out, c.do(ctx, "GET", "/v1/sessions", nil, &out)
}

func (c *Client) Session(ctx context.Context, id string) (store.Session, error) {
	var out store.Session
	return out, c.do(ctx, "GET", "/v1/sessions/"+url.PathEscape(id), nil, &out)
}

func (c *Client) SetSessionState(ctx context.Context, id, state, reason string) error {
	return c.do(ctx, "PUT", "/v1/sessions/"+url.PathEscape(id)+"/state", map[string]string{"state": state, "reason": reason}, nil)
}

func (c *Client) EndSession(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/v1/sessions/"+url.PathEscape(id), nil, nil)
}

// Signal is one side of a session's signaling relay. A background reader
// owns the connection, so a caller giving up on Receive does not close it.
type Signal struct {
	conn *websocket.Conn
	msgs chan signal.Message
	done chan struct{}
}

// Signal connects to the session's relay.
func (c *Client) Signal(ctx context.Context, sessionID string) (*Signal, error) {
	u := c.BaseURL + "/v1/sessions/" + url.PathEscape(sessionID) + "/signal"
	conn, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: c.HTTP.Transport},
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.Token}},
	})
	if err != nil {
		if resp != nil && resp.StatusCode >= 300 {
			return nil, &APIError{Status: resp.StatusCode, Message: "signal connect failed"}
		}
		return nil, err
	}
	conn.SetReadLimit(64 << 10)
	s := &Signal{conn: conn, msgs: make(chan signal.Message, 16), done: make(chan struct{})}
	go s.read()
	return s, nil
}

func (s *Signal) read() {
	defer close(s.done)
	for {
		var m signal.Message
		if err := wsjson.Read(context.Background(), s.conn, &m); err != nil {
			return
		}
		select {
		case s.msgs <- m:
		default: // nobody is reading; drop rather than block the connection
		}
	}
}

func (s *Signal) Send(ctx context.Context, typ string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return wsjson.Write(ctx, s.conn, signal.Message{Type: typ, Data: b})
}

// ErrSignalClosed is returned by Receive once the relay connection is gone,
// for example because the session ended.
var ErrSignalClosed = errors.New("signaling connection closed")

// Receive blocks for the next message, ctx cancellation, or the connection closing.
func (s *Signal) Receive(ctx context.Context) (signal.Message, error) {
	select {
	case m := <-s.msgs:
		return m, nil
	case <-ctx.Done():
		return signal.Message{}, ctx.Err()
	case <-s.done:
		select {
		case m := <-s.msgs:
			return m, nil
		default:
			return signal.Message{}, ErrSignalClosed
		}
	}
}

// Done is closed once the relay connection is gone.
func (s *Signal) Done() <-chan struct{} { return s.done }

func (s *Signal) Close() error { return s.conn.Close(websocket.StatusNormalClosure, "") }
