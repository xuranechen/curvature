package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agenttypes "curvature/backend/internal/agent/types"
	"curvature/backend/mcpaskuser"
)

func TestIsLocalBridgeRequestRequiresTokenAndLoopback(t *testing.T) {
	handler := &HTTPHandler{LocalCLIToken: "bridge-token"}

	request := func(remoteAddr, header, token string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, mcpaskuser.EndpointPath, nil)
		req.RemoteAddr = remoteAddr
		if header != "" {
			req.Header.Set(header, token)
		}
		return req
	}

	cases := []struct {
		name       string
		handler    *HTTPHandler
		request    *http.Request
		wantAccept bool
	}{
		{
			name:       "loopback with the bridge token",
			handler:    handler,
			request:    request("127.0.0.1:51000", mcpaskuser.TokenHeaderName, "bridge-token"),
			wantAccept: true,
		},
		{
			name:       "loopback with the local CLI token header",
			handler:    handler,
			request:    request("127.0.0.1:51000", localCLIHeaderName, "bridge-token"),
			wantAccept: true,
		},
		{
			name:    "wrong token",
			handler: handler,
			request: request("127.0.0.1:51000", mcpaskuser.TokenHeaderName, "other"),
		},
		{
			name:    "missing token",
			handler: handler,
			request: request("127.0.0.1:51000", "", ""),
		},
		{
			name:    "a remote caller cannot use the token",
			handler: handler,
			request: request("10.1.2.3:51000", mcpaskuser.TokenHeaderName, "bridge-token"),
		},
		{
			name:    "an unconfigured server accepts nothing",
			handler: &HTTPHandler{},
			request: request("127.0.0.1:51000", mcpaskuser.TokenHeaderName, "bridge-token"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.handler.isLocalBridgeRequest(tc.request); got != tc.wantAccept {
				t.Fatalf("isLocalBridgeRequest = %v, want %v", got, tc.wantAccept)
			}
		})
	}
}

func TestHandleMCPAskUserValidatesBeforeTouchingASession(t *testing.T) {
	handler := &HTTPHandler{LocalCLIToken: "bridge-token"}
	post := func(body string, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, mcpaskuser.EndpointPath, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:51000"
		if token != "" {
			req.Header.Set(mcpaskuser.TokenHeaderName, token)
		}
		recorder := httptest.NewRecorder()
		handler.handleMCPAskUser(recorder, req)
		return recorder
	}

	if code := post(`{}`, "").Code; code != http.StatusUnauthorized {
		t.Fatalf("missing token = %d, want 401", code)
	}
	if code := post(`{}`, "wrong").Code; code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d, want 401", code)
	}
	if code := post(`{`, "bridge-token").Code; code != http.StatusBadRequest {
		t.Fatalf("malformed payload = %d, want 400", code)
	}
	if code := post(`{"session_key":"s"}`, "bridge-token").Code; code != http.StatusBadRequest {
		t.Fatalf("missing agent = %d, want 400", code)
	}
	if code := post(`{"session_key":"s","agent":"dsh"}`, "bridge-token").Code; code != http.StatusBadRequest {
		t.Fatalf("missing questions = %d, want 400", code)
	}
}

func TestMCPAskUserPayloadRoundTrips(t *testing.T) {
	payload := mcpaskuser.EndpointRequest{
		SessionKey: "session-1",
		Agent:      "dsh",
		TimeoutMS:  1000,
		Request: agenttypes.MCPAskUserRequest{
			CallID: "3",
			Questions: []agenttypes.MCPAskUserQuestion{{
				ID:       "color",
				Question: "Which color?",
				Options:  []agenttypes.AskUserQuestionOption{{Label: "Red"}, {Label: "Blue"}},
			}},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded mcpaskuser.EndpointRequest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.SessionKey != "session-1" || decoded.Agent != "dsh" || decoded.Request.CallID != "3" {
		t.Fatalf("decoded = %+v", decoded)
	}
	if len(decoded.Request.Questions) != 1 || len(decoded.Request.Questions[0].Options) != 2 {
		t.Fatalf("questions = %+v", decoded.Request.Questions)
	}
}
