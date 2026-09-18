package main

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

type Hub struct {
	mu       sync.RWMutex
	sessions map[string]*yamux.Session
}

func NewHub() *Hub {
	return &Hub{sessions: map[string]*yamux.Session{}}
}

func (h *Hub) register(nodeID string, sess *yamux.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.sessions[nodeID]; ok && old != sess {
		_ = old.Close()
	}
	h.sessions[nodeID] = sess
}

func (h *Hub) unregister(nodeID string, sess *yamux.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.sessions[nodeID]; ok && cur == sess {
		delete(h.sessions, nodeID)
	}
}

func (h *Hub) get(nodeID string) *yamux.Session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessions[nodeID]
}

// nodeAccessPasswordHeader carries the node's access password (or its SHA-256
// hex digest for non-ASCII passwords) on the device WebSocket handshake.
// Devices must know the access password they set, so a stolen device token
// alone cannot register a relay session.
const nodeAccessPasswordHeader = "X-Curvature-Node-Password"

// handleDeviceWS accepts the device WebSocket and runs the yamux server session.
func (h *Hub) handleDeviceWS(store *Store, w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" || token == auth {
		w.WriteHeader(401)
		return
	}
	dev := store.getDeviceByToken(token)
	if dev == nil {
		w.WriteHeader(401)
		return
	}
	if dev.AccessPassword != "" {
		entered := strings.TrimSpace(r.Header.Get(nodeAccessPasswordHeader))
		if !passwordMatch(dev.AccessPassword, entered) {
			writeJSONError(w, http.StatusForbidden, "access_password_invalid")
			return
		}
	}

	up := websocket.Upgrader{
		HandshakeTimeout: 15 * time.Second,
		CheckOrigin:      func(r *http.Request) bool { return true },
	}
	conn, err := up.Upgrade(w, r, http.Header{
		"X-Curvature-Relay-Node-Name": []string{dev.NodeName},
	})
	if err != nil {
		return
	}
	defer conn.Close()

	wsConn := NewWebSocketNetConn(conn)
	ycfg := yamux.DefaultConfig()
	ycfg.MaxStreamWindowSize = 4 << 20
	ycfg.ConnectionWriteTimeout = 60 * time.Second
	ycfg.EnableKeepAlive = true
	ycfg.KeepAliveInterval = 30 * time.Second

	sess, err := yamux.Server(wsConn, ycfg)
	if err != nil {
		return
	}
	defer sess.Close()

	h.register(dev.NodeID, sess)
	defer h.unregister(dev.NodeID, sess)

	<-sess.CloseChan()
}
