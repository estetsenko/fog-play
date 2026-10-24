// Package signal relays messages between the player and the host of a
// session over WebSocket, so the two sides can set up a stream.
//
// The hub is in memory, so all clients of a session must reach the same
// server instance. Messages sent while the other side is not connected are
// queued (up to maxPending) and delivered when it connects.
package signal

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	RolePlayer = "player"
	RoleHost   = "host"

	maxPending   = 64
	maxMsgBytes  = 64 << 10
	writeTimeout = 10 * time.Second
)

// Message is what clients exchange. The hub sets From; Data is opaque to it.
type Message struct {
	From string          `json:"from,omitempty"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Message types used by the Moonlight flow.
const (
	// TypeHostInfo goes host -> player: where to connect and which app to start.
	TypeHostInfo = "host-info"
	// TypePairPIN goes player -> host: the PIN Moonlight is pairing with.
	TypePairPIN = "pair-pin"
	// TypePairResult goes host -> player: whether Sunshine accepted the PIN.
	TypePairResult = "pair-result"
)

type HostInfo struct {
	Address string `json:"address"`
	App     string `json:"app"`
}

type PairPIN struct {
	PIN string `json:"pin"`
	// ClientName is the name Moonlight pairs under, used to pick the right
	// pending request when Sunshine has several.
	ClientName string `json:"client_name"`
}

type PairResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type peer struct {
	conn *websocket.Conn
	mu   sync.Mutex // serialises writes
}

func (p *peer) write(ctx context.Context, m Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return wsjson.Write(ctx, p.conn, m)
}

type room struct {
	peers   map[string]*peer
	pending map[string][]Message // queued for a role that is not connected
}

type Hub struct {
	mu    sync.Mutex
	rooms map[string]*room
}

func NewHub() *Hub { return &Hub{rooms: map[string]*room{}} }

func other(role string) string {
	if role == RolePlayer {
		return RoleHost
	}
	return RolePlayer
}

// Serve runs one side of a session's relay until the connection closes or
// the session is closed with Close. A second connection for the same role
// replaces the first.
func (h *Hub) Serve(ctx context.Context, sessionID, role string, conn *websocket.Conn) {
	conn.SetReadLimit(maxMsgBytes)
	me := &peer{conn: conn}

	h.mu.Lock()
	r := h.rooms[sessionID]
	if r == nil {
		r = &room{peers: map[string]*peer{}, pending: map[string][]Message{}}
		h.rooms[sessionID] = r
	}
	if old := r.peers[role]; old != nil {
		old.conn.Close(websocket.StatusPolicyViolation, "replaced by a new connection")
	}
	r.peers[role] = me
	queued := r.pending[role]
	delete(r.pending, role)
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		if r.peers[role] == me {
			delete(r.peers, role)
		}
		h.mu.Unlock()
		conn.CloseNow()
	}()

	for _, m := range queued {
		if err := me.write(ctx, m); err != nil {
			return
		}
	}
	for {
		var m Message
		if err := wsjson.Read(ctx, conn, &m); err != nil {
			return
		}
		if m.Type == "" {
			continue
		}
		m.From = role
		h.deliver(ctx, sessionID, other(role), m)
	}
}

func (h *Hub) deliver(ctx context.Context, sessionID, to string, m Message) {
	h.mu.Lock()
	r := h.rooms[sessionID]
	if r == nil {
		h.mu.Unlock()
		return
	}
	p := r.peers[to]
	if p == nil {
		if len(r.pending[to]) < maxPending {
			r.pending[to] = append(r.pending[to], m)
		}
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	if err := p.write(ctx, m); err != nil {
		p.conn.CloseNow()
	}
}

// Close disconnects both sides of a session and drops its queued messages.
func (h *Hub) Close(sessionID string) {
	h.mu.Lock()
	r := h.rooms[sessionID]
	delete(h.rooms, sessionID)
	h.mu.Unlock()
	if r == nil {
		return
	}
	for _, p := range r.peers {
		p.conn.Close(websocket.StatusNormalClosure, "session ended")
	}
}
