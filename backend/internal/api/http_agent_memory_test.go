package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"curvature/backend/internal/agent"
)

func newAgentReleaseTestHandler(t *testing.T) *HTTPHandler {
	t.Helper()
	pool := agent.NewPool(agent.Config{Agents: []agent.Definition{
		{Name: "gemini", Protocol: agent.ProtocolACP},
	}})
	t.Cleanup(pool.CloseAll)
	return &HTTPHandler{AppContext: &AppContext{Agents: pool}}
}

func TestHandleAgentReleaseRejectsUnknownAgent(t *testing.T) {
	handler := newAgentReleaseTestHandler(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/release", strings.NewReader(`{"agent":"nope"}`))

	handler.handleAgentRelease(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleAgentReleaseReportsIdleAgentWithoutSessions(t *testing.T) {
	handler := newAgentReleaseTestHandler(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/release", strings.NewReader(`{"agent":"gemini"}`))

	handler.handleAgentRelease(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Agent            string `json:"agent"`
		ReleasedSessions int    `json:"released_sessions"`
		FailedSessions   int    `json:"failed_sessions"`
		ProcessReleased  bool   `json:"process_released"`
		IdleHours        int    `json:"idle_hours"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if payload.Agent != "gemini" {
		t.Fatalf("agent = %q, want gemini", payload.Agent)
	}
	if payload.ReleasedSessions != 0 || payload.FailedSessions != 0 || payload.ProcessReleased {
		t.Fatalf("unexpected release outcome: %#v", payload)
	}
	if payload.IdleHours <= 0 {
		t.Fatalf("idle_hours = %d, want the configured default", payload.IdleHours)
	}
}
