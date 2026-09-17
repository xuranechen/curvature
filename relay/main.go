package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strings"
)

type Server struct {
	store     *Store
	hub       *Hub
	binds     *BindService
	forwarder *Forwarder
	base      string
}

const deviceIDHeader = "X-Curvature-Device-ID"

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	base := flag.String("base", "", "public base URL of this relay, e.g. https://relay.example.com")
	data := flag.String("data", "./data", "data directory for bindings, devices and services")
	flag.Parse()

	baseURL := strings.TrimRight(strings.TrimSpace(*base), "/")
	if baseURL == "" {
		log.Fatal("-base is required, e.g. -base https://relay.example.com")
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		log.Fatal("-base must start with http:// or https://")
	}

	store, err := NewStore(*data)
	if err != nil {
		log.Fatalf("init store: %v", err)
	}
	hub := NewHub()
	binds := NewBindService(store, baseURL)
	srv := &Server{
		store:     store,
		hub:       hub,
		binds:     binds,
		forwarder: NewForwarder(hub, store),
		base:      baseURL,
	}

	log.Printf("curvature-relay listening on %s, base=%s data=%s", *addr, baseURL, *data)
	if err := http.ListenAndServe(*addr, srv); err != nil {
		log.Fatal(err)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Subdomain-style local service routing comes first: every request whose
	// Host matches {slug}-{node_id}-relay.{domain} is relayed to the service.
	if s.forwarder.handleServiceHost(w, r) {
		return
	}

	path := r.URL.Path
	switch {
	case path == "/api/bind/poll":
		s.handleBindPoll(w, r)
	case path == "/api/bind/status":
		s.handleBindStatus(w, r)
	case path == "/api/bind/confirm":
		s.handleBindConfirm(w, r)
	case path == "/api/auth/me":
		// The Curvature frontend probes /api/auth/me on the relay origin when a
		// relay WebSocket drops (App.tsx handleRelayWebSocketClosed). It only
		// checks response.ok; a 200 keeps the session from being treated as
		// logged out, so no relay login system is required.
		respondJSON(w, http.StatusOK, map[string]any{"user": map[string]any{"id": "local"}})
	case path == "/api/devices":
		s.handleDevicesList(w, r)
	case strings.HasPrefix(path, "/api/devices/"):
		if r.Method == http.MethodDelete {
			s.handleDeviceDelete(w, r)
		} else {
			writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	case strings.HasPrefix(path, "/api/device/nodes/"):
		switch r.Method {
		case http.MethodPut:
			s.handleServicePut(w, r)
		case http.MethodDelete:
			s.handleServiceDelete(w, r)
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	case path == "/api/device/access-password":
		if r.Method != http.MethodPut && r.Method != http.MethodDelete {
			writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
		s.handleAccessPassword(w, r)
	case path == "/api/device/node-name":
		if r.Method != http.MethodPut {
			writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
		s.handleNodeName(w, r)
	case path == "/ws":
		s.hub.handleDeviceWS(s.store, w, r)
	case path == "/bind":
		s.handleBindPage(w, r)
	case path == "/login":
		// Fallback page in case the frontend ever lands here (no relay login
		// system). Give the user a way back to their nodes.
		writeHTML(w, loginPageHTML(s.base))
	case path == "/nodes":
		s.handleNodesPage(w, r)
	case path == "/" || path == "":
		http.Redirect(w, r, "/nodes", http.StatusFound)
	case strings.HasPrefix(path, "/n/"):
		if strings.HasSuffix(path, "/_auth") {
			s.forwarder.ServeNodeAuth(w, r)
			return
		}
		s.forwarder.HandleNode(w, r)
	default:
		writeJSONError(w, http.StatusNotFound, "not_found")
	}
}

func (s *Server) handleBindPoll(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	result, err := s.binds.poll(
		q.Get("code"),
		r.Header.Get(deviceIDHeader),
		q.Get("purpose"),
		q.Get("node_name"),
	)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, result)
}

// handleDeviceDelete removes a bound device from the relay. The /nodes page
// may delete without a token; the device itself authenticates with
// Authorization: Bearer or X-Curvature-Device-Token.
func (s *Server) handleDeviceDelete(w http.ResponseWriter, r *http.Request) {
	nodeID := strings.TrimPrefix(r.URL.Path, "/api/devices/")
	nodeID = strings.Trim(nodeID, "/")
	if nodeID == "" {
		writeJSONError(w, http.StatusBadRequest, "node_id_required")
		return
	}
	dev := s.store.getDeviceByNode(nodeID)
	if dev == nil {
		writeJSONError(w, http.StatusNotFound, "node_not_found")
		return
	}
	token := strings.TrimSpace(r.Header.Get("X-Curvature-Device-Token"))
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	if token != "" && token != dev.DeviceToken {
		writeJSONError(w, http.StatusForbidden, "device_token_mismatch")
		return
	}
	if sess := s.hub.get(nodeID); sess != nil {
		_ = sess.Close()
		s.hub.unregister(nodeID, sess)
	}
	if err := s.store.deleteDevice(nodeID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "delete_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"deleted": nodeID})
}

// handleAccessPassword sets or clears the access password for the calling
// device. It authenticates via the same Bearer device token used for the
// device WebSocket, so only the device itself can change its own password.
func (s *Server) handleAccessPassword(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" || token == auth {
		writeJSONError(w, http.StatusUnauthorized, "device_token_required")
		return
	}
	dev := s.store.getDeviceByToken(token)
	if dev == nil {
		writeJSONError(w, http.StatusUnauthorized, "device_token_invalid")
		return
	}
	var req struct {
		AccessPassword string `json:"access_password"`
	}
	if r.Method == http.MethodPut {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_json")
			return
		}
	}
	pw := strings.TrimSpace(req.AccessPassword)
	if pw != "" {
		if len(pw) < 4 || len(pw) > 128 {
			writeJSONError(w, http.StatusBadRequest, "password_length")
			return
		}
		sum := sha256.Sum256([]byte(pw))
		dev.AccessPassword = hex.EncodeToString(sum[:])
	} else {
		dev.AccessPassword = ""
	}
	if err := s.store.saveDevice(dev); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"node_id":      dev.NodeID,
		"password_set": dev.AccessPassword != "",
		"node_url":     s.binds.nodeURL(dev.NodeID),
	})
}

// handleNodeName renames the calling device's node. It authenticates via the
// same Bearer device token used for the device WebSocket. The name is echoed to
// the node list, the bind/auth pages and the device WebSocket handshake header.
func (s *Server) handleNodeName(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" || token == auth {
		writeJSONError(w, http.StatusUnauthorized, "device_token_required")
		return
	}
	dev := s.store.getDeviceByToken(token)
	if dev == nil {
		writeJSONError(w, http.StatusUnauthorized, "device_token_invalid")
		return
	}
	var req struct {
		NodeName string `json:"node_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	name := strings.TrimSpace(req.NodeName)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "node_name_required")
		return
	}
	if runes := []rune(name); len(runes) > 64 {
		name = string(runes[:64])
	}
	dev.NodeName = name
	if err := s.store.saveDevice(dev); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"node_id":   dev.NodeID,
		"node_name": dev.NodeName,
		"node_url":  s.binds.nodeURL(dev.NodeID),
	})
}

func (s *Server) handleBindStatus(w http.ResponseWriter, r *http.Request) {
	result, err := s.binds.status(r.URL.Query().Get("code"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func (s *Server) handleBindConfirm(w http.ResponseWriter, r *http.Request) {
	result, err := s.binds.confirm(r.URL.Query().Get("code"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, result)
}

func respondJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
