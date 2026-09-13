package main

import (
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
	case path == "/api/token-station/userinfo":
		s.handleTokenStationUserInfo(w, r)
	case path == "/api/auth/me":
		// The Curvature frontend probes /api/auth/me on the relay origin when a
		// relay WebSocket drops (App.tsx handleRelayWebSocketClosed). It only
		// checks response.ok; a 200 keeps the session from being treated as
		// logged out, so no relay login system is required.
		respondJSON(w, http.StatusOK, map[string]any{"user": map[string]any{"id": "local"}})
	case path == "/api/devices":
		s.handleDevicesList(w, r)
	case strings.HasPrefix(path, "/api/device/nodes/"):
		switch r.Method {
		case http.MethodPut:
			s.handleServicePut(w, r)
		case http.MethodDelete:
			s.handleServiceDelete(w, r)
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
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
		s.forwarder.handleNode(w, r)
	default:
		writeJSONError(w, http.StatusNotFound, "not_found")
	}
}

func (s *Server) handleBindPoll(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	result, err := s.binds.poll(q.Get("code"), r.Header.Get(deviceIDHeader), q.Get("purpose"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, result)
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
