package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"curvature/backend/internal/api/usecase"
	"curvature/backend/mcpaskuser"
)

// maxMCPAskUserTimeout caps how long a bridge call may keep the HTTP request
// open; the MCP server process applies its own, usually shorter, timeout.
const maxMCPAskUserTimeout = 12 * time.Hour

// handleMCPAskUser receives an ask_user_question tool call from the MCP server
// Curvature attached to an agent session, renders it as an ask-user card, and
// blocks until the human answers.
//
// The caller is a local process Curvature started, so it authenticates with the
// local CLI token instead of an E2EE session, and the session identity travels
// in the body it was handed through its environment.
func (h *HTTPHandler) handleMCPAskUser(w http.ResponseWriter, r *http.Request) {
	if !h.isLocalBridgeRequest(r) {
		respondError(w, http.StatusUnauthorized, errors.New("local bridge token required"))
		return
	}
	var req mcpaskuser.EndpointRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, errors.New("invalid bridge payload"))
		return
	}
	if strings.TrimSpace(req.SessionKey) == "" || strings.TrimSpace(req.Agent) == "" {
		respondError(w, http.StatusBadRequest, errors.New("session and agent are required"))
		return
	}
	if len(req.Request.Questions) == 0 {
		respondError(w, http.StatusBadRequest, errors.New("at least one question is required"))
		return
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > maxMCPAskUserTimeout {
		timeout = maxMCPAskUserTimeout
	}
	// Keep waiting for the human even if the browser hangs up: the card is
	// still on screen and the answer still has somewhere to go.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), timeout)
	defer cancel()

	answer, err := h.service().AskUserViaMCP(ctx, usecase.AskUserViaMCPInput{
		SessionKey: req.SessionKey,
		Agent:      req.Agent,
		Request:    req.Request,
	})
	if err != nil {
		log.Printf("[mcp-ask-user] request.failed session=%q agent=%q err=%v", req.SessionKey, req.Agent, err)
		respondError(w, http.StatusGatewayTimeout, err)
		return
	}
	respondJSON(w, http.StatusOK, mcpaskuser.EndpointResponse{Answer: answer})
}

// isLocalBridgeRequest authenticates a loopback call from a process Curvature
// started, using the same token the local CLI uses.
func (h *HTTPHandler) isLocalBridgeRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	token := strings.TrimSpace(h.LocalCLIToken)
	if token == "" {
		return false
	}
	presented := strings.TrimSpace(r.Header.Get(mcpaskuser.TokenHeaderName))
	if presented == "" {
		presented = strings.TrimSpace(r.Header.Get(localCLIHeaderName))
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
