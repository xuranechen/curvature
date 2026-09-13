package main

import "net/http"

// handleTokenStationUserInfo handles GET /api/token-station/userinfo
func (s *Server) handleTokenStationUserInfo(w http.ResponseWriter, r *http.Request) {
	dev := s.authorizedDevice(r)
	if dev == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "ok",
		"data": map[string]any{
			"balance":            0,
			"balance_text":       "self-hosted",
			"quota_display_text": "self-hosted relay, no quota",
			"used_quota_text":    "0",
			"topup_url":          s.base,
			"api_keys":           []any{},
		},
	})
}

// authorizedDevice extracts and validates the Bearer token against stored devices.
func (s *Server) authorizedDevice(r *http.Request) *Device {
	auth := r.Header.Get("Authorization")
	token := ""
	if len(auth) > 7 && (auth[:7] == "Bearer " || auth[:7] == "bearer ") {
		token = auth[7:]
	}
	if token == "" {
		return nil
	}
	return s.store.getDeviceByToken(token)
}
