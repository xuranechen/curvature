// Package mcpaskuser implements Curvature's client-supplied ask-user tool.
//
// Agents that cannot ask the human through their own protocol can still ask if
// the client hands them an MCP server: the model calls the server's
// ask_user_question tool, the server process POSTs the question back to the
// Curvature backend over loopback, the backend renders the usual ask-user card
// and blocks, and the human's answer travels back as the tool result.
//
// The package has two halves that share one wire format:
//   - Server speaks the MCP stdio protocol to the agent (protocol.go).
//   - Client talks to the Curvature backend (this file).
package mcpaskuser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	agenttypes "curvature/backend/internal/agent/types"
)

const (
	// EndpointPath is the backend route the MCP server process calls.
	EndpointPath = "/api/mcp/ask-user"
	// TokenHeaderName carries the local CLI token that proves the caller is a
	// local process Curvature itself started.
	TokenHeaderName = "X-Curvature-Token"
	// DefaultTimeout is how long a tool call waits for the human.
	DefaultTimeout = 30 * time.Minute
	// MaxTimeout caps how long a single tool call may block.
	MaxTimeout = 12 * time.Hour
	// ToolName is the tool the agent sees.
	ToolName = "ask_user_question"
	// ServerName is the MCP server name Curvature advertises to the agent.
	// Agents expose its tools as "<ServerName>__<ToolName>".
	ServerName = "curvature-ask-user"
	// Version is reported to the agent during initialize and by --selftest.
	Version = "1.0.0"
)

// EndpointRequest is what the MCP server process POSTs to the backend. The
// session identity travels in the body because the tool call itself carries no
// ACP/MCP session notion: the server process was started for exactly one
// session, so session and agent are pinned by the environment it inherited.
type EndpointRequest struct {
	SessionKey string                       `json:"session_key"`
	Agent      string                       `json:"agent"`
	Request    agenttypes.MCPAskUserRequest `json:"request"`
	TimeoutMS  int                          `json:"timeout_ms,omitempty"`
}

// EndpointResponse is the backend's reply.
type EndpointResponse struct {
	Answer agenttypes.MCPAskUserAnswer `json:"answer"`
}

// Client calls back into the Curvature backend.
type Client struct {
	Endpoint string
	Token    string
	HTTP     *http.Client
}

// Ask posts a question to the backend and waits for the human's answer.
func (c Client) Ask(ctx context.Context, req EndpointRequest) (agenttypes.MCPAskUserAnswer, error) {
	endpoint := strings.TrimSpace(c.Endpoint)
	if endpoint == "" {
		return agenttypes.MCPAskUserAnswer{}, errors.New("mcp ask user endpoint is not configured")
	}
	if !strings.Contains(endpoint, EndpointPath) {
		endpoint = strings.TrimRight(endpoint, "/") + EndpointPath
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return agenttypes.MCPAskUserAnswer{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return agenttypes.MCPAskUserAnswer{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(c.Token); token != "" {
		httpReq.Header.Set(TokenHeaderName, token)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return agenttypes.MCPAskUserAnswer{}, fmt.Errorf("ask user callback: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return agenttypes.MCPAskUserAnswer{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return agenttypes.MCPAskUserAnswer{}, fmt.Errorf("ask user callback failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out EndpointResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return agenttypes.MCPAskUserAnswer{}, fmt.Errorf("ask user callback reply: %w", err)
	}
	return out.Answer, nil
}

// Card builds the tool call Curvature renders as an ask-user card. It mirrors
// the native card so the same UI component handles both paths.
func Card(callID string, questions []agenttypes.MCPAskUserQuestion) agenttypes.ToolCall {
	items := make([]agenttypes.AskUserQuestionItem, 0, len(questions))
	for _, question := range questions {
		items = append(items, agenttypes.AskUserQuestionItem{
			Question:    question.Question,
			Header:      question.Header,
			Options:     append([]agenttypes.AskUserQuestionOption{}, question.Options...),
			MultiSelect: question.MultiSelect,
		})
	}
	title := "Ask user question"
	if len(items) > 0 && strings.TrimSpace(items[0].Header) != "" {
		title = strings.TrimSpace(items[0].Header)
	}
	return agenttypes.ToolCall{
		CallID:  callID,
		Title:   title,
		Status:  "running",
		Kind:    agenttypes.ToolKindAskUser,
		RawType: "tool_use",
		Meta: map[string]any{
			"toolUseId": callID,
			"questions": items,
			"transport": "mcp",
		},
	}
}

// AnswerMeta is the map stored on the completed card so a reloaded session can
// show the answer next to the question.
func AnswerMeta(questions []agenttypes.MCPAskUserQuestion, answers map[string]string) map[string]string {
	out := make(map[string]string, len(questions))
	for index, question := range questions {
		key := strings.TrimSpace(question.ID)
		if value, ok := answers[key]; ok && strings.TrimSpace(value) != "" {
			out[fmt.Sprintf("q_%d", index)] = strings.TrimSpace(value)
			continue
		}
		if value, ok := answers[fmt.Sprintf("q_%d", index)]; ok && strings.TrimSpace(value) != "" {
			out[fmt.Sprintf("q_%d", index)] = strings.TrimSpace(value)
		}
	}
	return out
}

// Answers maps the card's answer map onto the MCP tool's answer list. The UI
// submits answers keyed by question position (q_0, q_1, ...); the tool expects
// them keyed by the question id the model supplied.
func Answers(questions []agenttypes.MCPAskUserQuestion, answers map[string]string) (agenttypes.MCPAskUserAnswer, error) {
	if len(questions) == 0 {
		return agenttypes.MCPAskUserAnswer{}, errors.New("no questions to answer")
	}
	out := agenttypes.MCPAskUserAnswer{Answers: make([]agenttypes.MCPAskUserAnswerItem, 0, len(questions))}
	for index, question := range questions {
		value := strings.TrimSpace(answers[fmt.Sprintf("q_%d", index)])
		if value == "" {
			value = strings.TrimSpace(answers[strings.TrimSpace(question.ID)])
		}
		if value == "" {
			return agenttypes.MCPAskUserAnswer{}, fmt.Errorf("question %d (%s) has no answer", index, question.ID)
		}
		item := agenttypes.MCPAskUserAnswerItem{ID: question.ID}
		switch {
		case len(question.Options) == 0:
			// A question with no options is answered in the user's own words.
			item.Custom = value
		case question.MultiSelect:
			for _, part := range strings.Split(value, ",") {
				if trimmed := strings.TrimSpace(part); trimmed != "" {
					item.Selected = append(item.Selected, trimmed)
				}
			}
		default:
			item.Selected = []string{value}
		}
		out.Answers = append(out.Answers, item)
	}
	return out, nil
}
