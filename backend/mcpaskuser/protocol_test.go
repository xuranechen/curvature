package mcpaskuser

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agenttypes "curvature/backend/internal/agent/types"
)

type serverHarness struct {
	server    *Server
	requests  chan map[string]any
	inWriter  *io.PipeWriter
	responses chan map[string]any
	done      chan struct{}
}

func newServerHarness(t *testing.T, handler http.HandlerFunc) *serverHarness {
	t.Helper()
	endpoint := httptest.NewServer(handler)
	t.Cleanup(endpoint.Close)

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	server := &Server{
		SessionKey: "session-1",
		Agent:      "dsh",
		Client:     Client{Endpoint: endpoint.URL, Token: "local-token"},
		Timeout:    10 * time.Second,
		In:         inReader,
		Out:        outWriter,
	}
	harness := &serverHarness{
		server:    server,
		requests:  make(chan map[string]any, 4),
		inWriter:  inWriter,
		responses: make(chan map[string]any, 8),
		done:      make(chan struct{}),
	}
	go func() {
		defer close(harness.done)
		if err := server.Run(context.Background()); err != nil {
			t.Logf("server run: %v", err)
		}
	}()
	go func() {
		reader := bufio.NewReader(outReader)
		for {
			line, err := reader.ReadString('\n')
			if strings.TrimSpace(line) != "" {
				var message map[string]any
				if json.Unmarshal([]byte(line), &message) == nil {
					harness.responses <- message
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return harness
}

func (h *serverHarness) send(t *testing.T, message string) {
	t.Helper()
	if _, err := io.WriteString(h.inWriter, message+"\n"); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func (h *serverHarness) response(t *testing.T) map[string]any {
	t.Helper()
	select {
	case message := <-h.responses:
		return message
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an MCP response")
		return nil
	}
}

func answerHandler(t *testing.T, capture chan map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(TokenHeaderName); got != "local-token" {
			t.Errorf("token header = %q, want local-token", got)
		}
		var request EndpointRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if capture != nil {
			select {
			case capture <- map[string]any{
				"session":   request.SessionKey,
				"agent":     request.Agent,
				"questions": request.Request.Questions,
				"call_id":   request.Request.CallID,
			}:
			default:
			}
		}
		_ = json.NewEncoder(w).Encode(EndpointResponse{Answer: agenttypes.MCPAskUserAnswer{
			Answers: []agenttypes.MCPAskUserAnswerItem{{ID: "color", Selected: []string{"Red"}}},
		}})
	}
}

func TestServerInitializesAndListsTheTool(t *testing.T) {
	harness := newServerHarness(t, answerHandler(t, nil))
	harness.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{}}}`)
	init := harness.response(t)
	result, _ := init["result"].(map[string]any)
	if result["protocolVersion"] != "2025-11-25" {
		t.Fatalf("protocolVersion = %v, want the client's version", result["protocolVersion"])
	}
	serverInfo, _ := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "curvature-ask-user" {
		t.Fatalf("serverInfo = %+v", serverInfo)
	}

	harness.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	harness.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	list := harness.response(t)
	if list["id"] != float64(2) {
		t.Fatalf("answered the wrong request: %+v", list)
	}
	tools, _ := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", tools)
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != ToolName {
		t.Fatalf("tool name = %v", tool["name"])
	}
	assertValidJSONSchema(t, "inputSchema", tool["inputSchema"])
}

func TestServerAnswersAToolCallThroughTheBackend(t *testing.T) {
	captured := make(chan map[string]any, 1)
	harness := newServerHarness(t, answerHandler(t, captured))
	harness.send(t, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"ask_user_question","arguments":{"questions":[{"id":"color","question":"Which color?","options":[{"label":"Red"},{"label":"Blue"}]}]}}}`)
	response := harness.response(t)
	result, _ := response["result"].(map[string]any)
	if result == nil {
		t.Fatalf("tool call returned an error: %+v", response)
	}
	if result["isError"] != false {
		t.Fatalf("isError = %v", result["isError"])
	}
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %+v", content)
	}
	text := content[0].(map[string]any)["text"].(string)
	var answer agenttypes.MCPAskUserAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		t.Fatalf("answer text %q: %v", text, err)
	}
	if len(answer.Answers) != 1 || answer.Answers[0].ID != "color" || answer.Answers[0].Selected[0] != "Red" {
		t.Fatalf("answer = %+v", answer)
	}

	select {
	case request := <-captured:
		if request["session"] != "session-1" || request["agent"] != "dsh" {
			t.Fatalf("backend saw %+v", request)
		}
		if request["call_id"] != "7" {
			t.Fatalf("call id = %v, want the JSON-RPC id", request["call_id"])
		}
		questions, _ := request["questions"].([]agenttypes.MCPAskUserQuestion)
		if len(questions) != 1 || questions[0].ID != "color" {
			t.Fatalf("questions = %+v", questions)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backend was never called")
	}
}

func TestServerReportsABackendFailureAsAToolError(t *testing.T) {
	harness := newServerHarness(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no session", http.StatusServiceUnavailable)
	})
	harness.send(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ask_user_question","arguments":{"questions":[{"id":"color","question":"Which color?"}]}}}`)
	response := harness.response(t)
	result, _ := response["result"].(map[string]any)
	if result == nil || result["isError"] != true {
		t.Fatalf("a failed bridge call must be a tool error, got %+v", response)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "no session") {
		t.Fatalf("tool error %q does not explain the failure", text)
	}
}

func TestServerRejectsMalformedToolArguments(t *testing.T) {
	harness := newServerHarness(t, answerHandler(t, nil))
	harness.send(t, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ask_user_question","arguments":{"questions":[]}}}`)
	response := harness.response(t)
	if response["error"] == nil {
		t.Fatalf("empty questions must be rejected: %+v", response)
	}
	harness.send(t, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	response = harness.response(t)
	if response["error"] == nil {
		t.Fatalf("unknown tool must be rejected: %+v", response)
	}
}

func TestServerCancelsAnInflightQuestion(t *testing.T) {
	release := make(chan struct{})
	harness := newServerHarness(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
		http.Error(w, "gone", http.StatusGatewayTimeout)
	})
	defer close(release)
	harness.send(t, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"ask_user_question","arguments":{"questions":[{"id":"q","question":"Well?"}]}}}`)
	// Give the call a moment to reach the backend before cancelling it.
	time.Sleep(100 * time.Millisecond)
	harness.send(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":9,"reason":"user cancelled"}}`)
	response := harness.response(t)
	result, _ := response["result"].(map[string]any)
	if result == nil || result["isError"] != true {
		t.Fatalf("cancelled call must report a tool error: %+v", response)
	}
}

func TestServerIgnoresNotificationsWithoutIDs(t *testing.T) {
	harness := newServerHarness(t, answerHandler(t, nil))
	harness.send(t, `{"jsonrpc":"2.0","method":"some/notification"}`)
	harness.send(t, `{"jsonrpc":"2.0","id":11,"method":"ping"}`)
	response := harness.response(t)
	if response["id"] != float64(11) {
		t.Fatalf("expected the ping reply, got %+v", response)
	}
}

// assertValidJSONSchema guards the schema shape the model provider accepts:
// `required` is an array of names on an object, never a boolean on a property.
func assertValidJSONSchema(t *testing.T, path string, schema any) {
	t.Helper()
	switch node := schema.(type) {
	case map[string]any:
		if required, ok := node["required"]; ok {
			if _, isArray := required.([]any); !isArray {
				t.Fatalf("%s.required = %#v, want an array of property names", path, required)
			}
		}
		for key, value := range node {
			assertValidJSONSchema(t, path+"."+key, value)
		}
	case []any:
		for _, value := range node {
			assertValidJSONSchema(t, path+"[]", value)
		}
	}
}

func TestToolSchemaRequiresQuestions(t *testing.T) {
	tool := toolDescriptor()
	schema, _ := tool["inputSchema"].(map[string]any)
	required, _ := schema["required"].([]string)
	if len(required) != 1 || required[0] != "questions" {
		t.Fatalf("required = %+v, want [questions]", required)
	}
	properties, _ := schema["properties"].(map[string]any)
	questions, _ := properties["questions"].(map[string]any)
	if _, hasBooleanRequired := questions["required"]; hasBooleanRequired {
		t.Fatal("questions carries a property-level required flag, which is not valid JSON Schema")
	}
	items, _ := questions["items"].(map[string]any)
	itemRequired, _ := items["required"].([]string)
	if len(itemRequired) != 2 || itemRequired[0] != "id" || itemRequired[1] != "question" {
		t.Fatalf("item required = %+v", itemRequired)
	}
}

func TestParseTimeoutBounds(t *testing.T) {
	if got := parseTimeout(""); got != DefaultTimeout {
		t.Fatalf("empty timeout = %s", got)
	}
	if got := parseTimeout("nonsense"); got != DefaultTimeout {
		t.Fatalf("invalid timeout = %s", got)
	}
	if got := parseTimeout("45s"); got != 45*time.Second {
		t.Fatalf("timeout = %s", got)
	}
	if got := parseTimeout("1000h"); got != MaxTimeout {
		t.Fatalf("timeout = %s, want the cap", got)
	}
}
