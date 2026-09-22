package mcpaskuser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	agenttypes "curvature/backend/internal/agent/types"
)

// Server is the stdio MCP server the agent launches. It exposes exactly one
// tool (ask_user_question) and forwards each call to the backend.
type Server struct {
	SessionKey string
	Agent      string
	Client     Client
	Timeout    time.Duration

	In     io.Reader
	Out    io.Writer
	Logf   func(format string, args ...any)
	Parser func(agenttypes.MCPAskUserRequest) (agenttypes.MCPAskUserRequest, error)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type askUserArguments struct {
	Questions []agenttypes.MCPAskUserQuestion `json:"questions"`
}

type cancelledParams struct {
	RequestID json.RawMessage `json:"requestId"`
	Reason    string          `json:"reason,omitempty"`
}

const toolDescription = "Ask the user a question and wait for their answer. Use this when you need a decision, preference, or missing information from the human before continuing."

// Run serves the MCP protocol until stdin closes.
func (s *Server) Run(ctx context.Context) error {
	if s.In == nil {
		return errors.New("mcp ask user server: input is nil")
	}
	if s.Out == nil {
		return errors.New("mcp ask user server: output is nil")
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}

	inflight := &inflightCalls{cancels: map[string]context.CancelFunc{}}
	defer inflight.cancelAll()

	reader := bufio.NewReaderSize(s.In, 1<<20)
	for {
		line, err := readMessage(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		var request rpcRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			s.logf("dropping malformed message: %v", err)
			continue
		}
		if err := s.handle(ctx, request, timeout, inflight); err != nil {
			return err
		}
	}
}

func (s *Server) handle(ctx context.Context, request rpcRequest, timeout time.Duration, inflight *inflightCalls) error {
	switch request.Method {
	case "initialize":
		return s.reply(request.ID, map[string]any{
			"protocolVersion": initializeProtocolVersion(request.Params),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": ServerName, "version": Version},
		})
	case "notifications/initialized", "notifications/roots/list_changed":
		return nil
	case "ping":
		return s.reply(request.ID, map[string]any{})
	case "tools/list":
		return s.reply(request.ID, map[string]any{"tools": []any{toolDescriptor()}})
	case "tools/call":
		return s.handleToolCall(ctx, request, timeout, inflight)
	case "notifications/cancelled":
		var params cancelledParams
		if err := json.Unmarshal(request.Params, &params); err == nil {
			inflight.cancel(keyOf(params.RequestID), params.Reason)
		}
		return nil
	default:
		if len(request.ID) == 0 {
			return nil
		}
		return s.replyError(request.ID, -32601, "method not found: "+request.Method)
	}
}

func (s *Server) handleToolCall(ctx context.Context, request rpcRequest, timeout time.Duration, inflight *inflightCalls) error {
	var params callToolParams
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return s.replyError(request.ID, -32602, "invalid params: "+err.Error())
	}
	if params.Name != ToolName {
		return s.replyError(request.ID, -32602, "unknown tool: "+params.Name)
	}
	var args askUserArguments
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return s.replyError(request.ID, -32602, "invalid arguments: "+err.Error())
		}
	}
	if s.Parser != nil {
		parsed, err := s.Parser(agenttypes.MCPAskUserRequest{Questions: args.Questions})
		if err != nil {
			return s.replyError(request.ID, -32602, err.Error())
		}
		args.Questions = parsed.Questions
	}
	if err := validateQuestions(args.Questions); err != nil {
		return s.replyError(request.ID, -32602, err.Error())
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	callKey := keyOf(request.ID)
	inflight.add(callKey, cancel)
	defer inflight.remove(callKey)

	answer, err := s.Client.Ask(callCtx, EndpointRequest{
		SessionKey: s.SessionKey,
		Agent:      s.Agent,
		Request:    agenttypes.MCPAskUserRequest{CallID: callKey, Questions: args.Questions},
		TimeoutMS:  int(timeout / time.Millisecond),
	})
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return s.replyToolError(request.ID, fmt.Sprintf("the user did not answer within %s", timeout))
		}
		if errors.Is(callCtx.Err(), context.Canceled) {
			return s.replyToolError(request.ID, "the question was cancelled before the user answered")
		}
		return s.replyToolError(request.ID, err.Error())
	}

	payload, err := json.Marshal(answer)
	if err != nil {
		return s.replyToolError(request.ID, "cannot encode the answer: "+err.Error())
	}
	return s.reply(request.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(payload)}},
		"isError": false,
	})
}

// replyToolError reports a tool-level failure to the model: the question could
// not be answered, so the model must not invent one.
func (s *Server) replyToolError(id json.RawMessage, message string) error {
	return s.reply(id, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": message}},
		"isError": true,
	})
}

func (s *Server) reply(id json.RawMessage, result any) error {
	if len(id) == 0 {
		return nil
	}
	return s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) replyError(id json.RawMessage, code int, message string) error {
	if len(id) == 0 {
		return nil
	}
	return s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func (s *Server) write(response rpcResponse) error {
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	_, err = s.Out.Write(payload)
	return err
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func toolDescriptor() map[string]any {
	return map[string]any{
		"name":        ToolName,
		"description": toolDescription,
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"questions": map[string]any{
					"type":        "array",
					"description": "One or more questions to ask the user.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":       map[string]any{"type": "string", "description": "Stable identifier echoed back with the answer."},
							"question": map[string]any{"type": "string", "description": "The question to show the user."},
							"header":   map[string]any{"type": "string", "description": "Short label for the question."},
							"options": map[string]any{
								"type":        "array",
								"description": "Choices the user picks from. Omit for a free-form answer.",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"label":       map[string]any{"type": "string"},
										"description": map[string]any{"type": "string"},
									},
									"required": []string{"label"},
								},
							},
							"multi_select": map[string]any{"type": "boolean", "description": "Allow more than one option to be selected."},
						},
						"required": []string{"id", "question"},
					},
				},
			},
			"required": []string{"questions"},
		},
	}
}

func validateQuestions(questions []agenttypes.MCPAskUserQuestion) error {
	if len(questions) == 0 {
		return errors.New("at least one question is required")
	}
	for index, question := range questions {
		if strings.TrimSpace(question.ID) == "" {
			return fmt.Errorf("question %d is missing an id", index)
		}
		if strings.TrimSpace(question.Question) == "" {
			return fmt.Errorf("question %d is missing its text", index)
		}
	}
	return nil
}

func initializeProtocolVersion(params json.RawMessage) string {
	var payload struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &payload); err == nil {
		if version := strings.TrimSpace(payload.ProtocolVersion); version != "" {
			return version
		}
	}
	return "2025-06-18"
}

// readMessage reads one newline-delimited JSON-RPC message, tolerating the
// newline-free EOF that some clients produce on shutdown.
func readMessage(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && strings.TrimSpace(line) != "" {
			return line, nil
		}
		return "", err
	}
	return line, nil
}

func keyOf(id json.RawMessage) string {
	if len(id) == 0 {
		return ""
	}
	return strings.TrimSpace(string(id))
}

type inflightCalls struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	all     []context.CancelFunc
}

func (c *inflightCalls) add(key string, cancel context.CancelFunc) {
	if key == "" {
		return
	}
	c.mu.Lock()
	c.cancels[key] = cancel
	c.all = append(c.all, cancel)
	c.mu.Unlock()
}

func (c *inflightCalls) remove(key string) {
	if key == "" {
		return
	}
	c.mu.Lock()
	delete(c.cancels, key)
	c.mu.Unlock()
}

func (c *inflightCalls) cancel(key, reason string) {
	c.mu.Lock()
	cancel := c.cancels[key]
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *inflightCalls) cancelAll() {
	c.mu.Lock()
	cancels := append([]context.CancelFunc{}, c.all...)
	c.cancels = map[string]context.CancelFunc{}
	c.all = nil
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
