package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const serviceSlugHeader = "X-Curvature-Relay-Service-Slug"

var serviceSlugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{1,38}[a-z0-9])$`)

func ValidServiceSlug(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return serviceSlugPattern.MatchString(value) && !strings.Contains(value, "--")
}

// handleServicePut handles PUT /api/device/nodes/{node_id}/services/{slug}
func (s *Server) handleServicePut(w http.ResponseWriter, r *http.Request) {
	nodeID, slug, ok := splitServicePath(r.URL.Path)
	if !ok || !ValidServiceSlug(slug) {
		writeJSONError(w, http.StatusBadRequest, "invalid_service_slug")
		return
	}
	dev := s.authorizedDevice(r)
	if dev == nil || dev.NodeID != nodeID {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	svc := &Service{
		Slug:    slug,
		NodeID:  nodeID,
		Name:    strings.TrimSpace(body.Name),
		Enabled: body.Enabled,
	}
	if svc.Name == "" {
		svc.Name = slug
	}
	if err := s.store.saveService(svc); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "store_failed")
		return
	}
	respondJSON(w, http.StatusOK, svc)
}

// handleServiceDelete handles DELETE /api/device/nodes/{node_id}/services/{slug}
func (s *Server) handleServiceDelete(w http.ResponseWriter, r *http.Request) {
	nodeID, slug, ok := splitServicePath(r.URL.Path)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid_service_slug")
		return
	}
	dev := s.authorizedDevice(r)
	if dev == nil || dev.NodeID != nodeID {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := s.store.deleteService(slug, nodeID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "store_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// splitServicePath parses /api/device/nodes/{node_id}/services/{slug}
func splitServicePath(path string) (nodeID, slug string, ok bool) {
	const prefix = "/api/device/nodes/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	nodeID, rest, found := strings.Cut(rest, "/services/")
	if !found || nodeID == "" {
		return "", "", false
	}
	if rest == "" {
		return "", "", false
	}
	slug, err := url.PathUnescape(rest)
	if err != nil {
		return "", "", false
	}
	return nodeID, slug, true
}
