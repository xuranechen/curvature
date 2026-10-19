package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"curvature/backend/internal/agent"
	"curvature/backend/internal/preferences"
)

type agentMemoryResponse struct {
	agent.MemorySnapshot
	IdleHours int `json:"idle_hours"`
}

type agentSessionReleaseResponse struct {
	agentMemoryResponse
	ReleasedSessions int `json:"released_sessions"`
	FailedSessions   int `json:"failed_sessions"`
}

type agentReleaseRequest struct {
	Agent string `json:"agent"`
}

type agentReleaseResponse struct {
	agentMemoryResponse
	Agent            string `json:"agent"`
	ReleasedSessions int    `json:"released_sessions"`
	FailedSessions   int    `json:"failed_sessions"`
	ProcessReleased  bool   `json:"process_released"`
}

func (h *HTTPHandler) handleAgentMemoryGet(w http.ResponseWriter, _ *http.Request) {
	pool, hours, ok := h.agentMemoryDependencies(w)
	if !ok {
		return
	}
	snapshot := pool.MemorySnapshot(true)
	respondJSON(w, http.StatusOK, agentMemoryResponse{
		MemorySnapshot: snapshot,
		IdleHours:      hours,
	})
}

func (h *HTTPHandler) handleAgentIdleRelease(w http.ResponseWriter, _ *http.Request) {
	pool, hours, ok := h.agentMemoryDependencies(w)
	if !ok {
		return
	}
	result := pool.ReleaseInactiveSessionsDetailed(time.Now())
	snapshot := pool.MemorySnapshot(true)
	respondJSON(w, http.StatusOK, agentSessionReleaseResponse{
		agentMemoryResponse: agentMemoryResponse{
			MemorySnapshot: snapshot,
			IdleHours:      hours,
		},
		ReleasedSessions: result.ReleasedSessions,
		FailedSessions:   result.FailedSessions,
	})
}

// handleAgentRelease releases a single agent on demand, without waiting for the
// idle threshold. It closes that agent's inactive sessions and recycles its
// shared runtime process once no session is left.
func (h *HTTPHandler) handleAgentRelease(w http.ResponseWriter, r *http.Request) {
	var req agentReleaseRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxUploadRequestBytes)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, errInvalidRequest("invalid request body"))
		return
	}
	pool, hours, ok := h.agentMemoryDependencies(w)
	if !ok {
		return
	}
	result, err := pool.ReleaseAgentDetailed(req.Agent, time.Now())
	if err != nil {
		respondError(w, http.StatusBadRequest, errInvalidRequest(err.Error()))
		return
	}
	respondJSON(w, http.StatusOK, agentReleaseResponse{
		agentMemoryResponse: agentMemoryResponse{
			MemorySnapshot: pool.MemorySnapshot(true),
			IdleHours:      hours,
		},
		Agent:            result.Agent,
		ReleasedSessions: result.ReleasedSessions,
		FailedSessions:   result.FailedSessions,
		ProcessReleased:  result.ProcessReleased,
	})
}

func (h *HTTPHandler) agentMemoryDependencies(w http.ResponseWriter) (*agent.Pool, int, bool) {
	if h == nil || h.AppContext == nil || h.AppContext.GetAgentPool() == nil {
		respondError(w, http.StatusServiceUnavailable, errInvalidRequest("agent pool not configured"))
		return nil, 0, false
	}
	hours := preferences.DefaultIdleSessionResourceReleaseHours
	if store := h.AppContext.GetPreferences(); store != nil {
		hours = store.IdleSessionResourceReleaseHours()
	}
	return h.AppContext.GetAgentPool(), hours, true
}
