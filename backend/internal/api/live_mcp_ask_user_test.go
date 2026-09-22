//go:build acpintegration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"curvature/backend/internal/agent"
	agenttypes "curvature/backend/internal/agent/types"
	"curvature/backend/mcpaskuser"
)

// TestLiveMCPAskUserBridge runs the ask-user bridge end to end through the real
// pool and the real HTTP endpoint, against a real agent.
//
//	dsh model -> MCP tool call -> curvature mcp-ask-user (stdio, started by dsh)
//	  -> POST /api/mcp/ask-user -> ask-user card -> answer
//	  -> tool result -> model continues
//
// Only the human is simulated: the test clicks the first option as soon as the
// card appears. Everything else is production code, including the agent config
// (`mcpAskUser`), the pool's session-key contract and the endpoint's token
// check.
func TestLiveMCPAskUserBridge(t *testing.T) {
	if _, err := exec.LookPath("dsh"); err != nil {
		t.Skipf("dsh is not on PATH: %v", err)
	}
	root := moduleRoot(t)
	cfg, err := agent.LoadConfig(filepath.Join(root, "agents.json"))
	if err != nil {
		t.Skipf("cannot load agents.json: %v", err)
	}
	def, ok := cfg.GetAgent("dsh")
	if !ok {
		t.Skip("dsh is not configured")
	}
	if !def.MCPAskUser {
		t.Fatalf("dsh is configured without mcpAskUser; the bridge would never attach")
	}
	waitFor := liveTimeout("CURVATURE_LIVE_QUESTION_TIMEOUT", 3*time.Minute)

	pool := agent.NewPool(cfg)
	defer pool.CloseAll()
	if err := pool.SetAgentEnv("dsh", map[string]string{"DSH_PERMISSION_MODE": "workspace-write"}); err != nil {
		t.Skipf("cannot set the dsh permission mode: %v", err)
	}

	const bridgeToken = "live-bridge-token"
	var (
		seenMu    sync.Mutex
		seenCalls []mcpaskuser.EndpointRequest
		handler   = &HTTPHandler{AppContext: &AppContext{Agents: pool}, LocalCLIToken: bridgeToken}
	)
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != mcpaskuser.EndpointPath {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		// Capture the payload for assertions, then hand the body to the real
		// handler untouched.
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "unreadable payload", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var request mcpaskuser.EndpointRequest
		if err := json.Unmarshal(raw, &request); err == nil {
			seenMu.Lock()
			seenCalls = append(seenCalls, request)
			seenMu.Unlock()
		}
		handler.handleMCPAskUser(w, r)
	}))
	defer bridge.Close()
	pool.ConfigureMCPAskUserBridge(agent.MCPAskUserBridgeOptions{
		Addr:    strings.TrimPrefix(bridge.URL, "http://"),
		Token:   bridgeToken,
		Command: curvatureBinary(t, root),
	})

	ctx, cancel := context.WithTimeout(context.Background(), waitFor+2*time.Minute)
	defer cancel()

	// The pool key the use case would compute: agentPoolSessionKey("live-mcp-1", "dsh").
	poolKey := "dsh-live-mcp-1"
	sess, err := pool.GetOrCreate(ctx, agenttypes.OpenSessionInput{
		SessionKey: poolKey,
		AgentName:  "dsh",
		RootPath:   t.TempDir(),
	})
	if err != nil {
		t.Skipf("GetOrCreate(dsh): %v", err)
	}

	var (
		eventMu      sync.Mutex
		cardCallID   string
		cardQuestion string
		cardOptions  []string
		rawToolCalls []string
		reply        strings.Builder
	)
	answered := make(chan struct{}, 1)
	doneCh := make(chan struct{}, 1)
	sess.OnUpdate(func(event agenttypes.Event) {
		switch event.Type {
		case agenttypes.EventTypeToolCall:
			toolCall, ok := event.Data.(agenttypes.ToolCall)
			if !ok {
				return
			}
			eventMu.Lock()
			rawToolCalls = append(rawToolCalls, "call kind="+string(toolCall.Kind)+" title="+toolCall.Title)
			eventMu.Unlock()
			if toolCall.Kind != agenttypes.ToolKindAskUser {
				return
			}
			questions, _ := toolCall.Meta["questions"].([]agenttypes.AskUserQuestionItem)
			callID, _ := toolCall.Meta["toolUseId"].(string)
			if len(questions) == 0 || callID == "" {
				t.Errorf("card is missing its question payload: %+v", toolCall.Meta)
				return
			}
			eventMu.Lock()
			cardCallID = callID
			cardQuestion = questions[0].Question
			for _, option := range questions[0].Options {
				cardOptions = append(cardOptions, option.Label)
			}
			eventMu.Unlock()

			answer := ""
			if len(questions[0].Options) > 0 {
				answer = questions[0].Options[0].Label
			}
			if strings.TrimSpace(answer) == "" {
				t.Error("the model asked a free-form question; this test can only click options")
				return
			}
			answerCtx, cancelAnswer := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancelAnswer()
			if err := sess.AnswerQuestion(answerCtx, agenttypes.AskUserAnswer{
				ToolUseID: callID,
				Answers:   map[string]string{"q_0": answer},
			}); err != nil {
				t.Errorf("AnswerQuestion: %v", err)
				return
			}
			select {
			case answered <- struct{}{}:
			default:
			}
		case agenttypes.EventTypeToolUpdate:
			toolCall, ok := event.Data.(agenttypes.ToolCall)
			if !ok || toolCall.Kind != agenttypes.ToolKindAskUser {
				return
			}
			if status := strings.TrimSpace(toolCall.Status); status != "complete" {
				t.Errorf("card finished with status %q (%+v)", status, toolCall.Meta)
			}
			if toolCall.Meta["answered"] != true {
				t.Errorf("card did not record the answer: %+v", toolCall.Meta)
			}
		case agenttypes.EventTypeMessageChunk:
			if chunk, ok := event.Data.(agenttypes.MessageChunk); ok {
				eventMu.Lock()
				reply.WriteString(chunk.Content)
				eventMu.Unlock()
			}
		case agenttypes.EventTypeMessageDone:
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	})

	const prompt = "Use the ask_user_question tool to ask me whether I prefer red or blue. " +
		"Offer those two options, wait for my answer, then reply with exactly one short sentence naming the color I chose."
	sendErr := make(chan error, 1)
	go func() { sendErr <- sess.SendMessage(ctx, prompt) }()

	for waiting := true; waiting; {
		select {
		case <-doneCh:
			waiting = false
		case err := <-sendErr:
			if err != nil {
				t.Logf("[bridge-live] SendMessage: %v", err)
			}
			waiting = false
		case <-time.After(waitFor):
			t.Logf("[bridge-live] the turn did not end within %s", waitFor)
			waiting = false
		}
	}

	eventMu.Lock()
	finalReply := strings.TrimSpace(reply.String())
	question := cardQuestion
	options := append([]string{}, cardOptions...)
	toolCalls := append([]string{}, rawToolCalls...)
	eventMu.Unlock()
	t.Logf("[bridge-live] card: %v question=%q options=%v", cardCallID != "", question, options)
	t.Logf("[bridge-live] tool calls seen: %v", toolCalls)
	t.Logf("[bridge-live] reply: %s", finalReply)

	seenMu.Lock()
	calls := append([]mcpaskuser.EndpointRequest{}, seenCalls...)
	seenMu.Unlock()
	if len(calls) == 0 {
		t.Skipf("dsh never called the bridged tool; reply was %q (the model chose not to ask)", finalReply)
	}

	// This is the assertion that catches a session-key mismatch between the pool
	// and the endpoint.
	if calls[0].SessionKey != poolKey {
		t.Fatalf("bridge call carried session key %q, want the pool key %q", calls[0].SessionKey, poolKey)
	}
	if calls[0].Agent != "dsh" {
		t.Fatalf("bridge call carried agent %q", calls[0].Agent)
	}
	if len(calls[0].Request.Questions) == 0 || strings.TrimSpace(calls[0].Request.Questions[0].Question) == "" {
		t.Fatalf("bridge call carried no question: %+v", calls[0].Request)
	}
	t.Logf("[bridge-live] the model asked %q through the bridge", calls[0].Request.Questions[0].Question)

	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("the card was never answered")
	}
	if cardCallID == "" {
		t.Fatal("the question never became a card")
	}

	answer := strings.TrimSpace(calls[0].Request.Questions[0].Options[0].Label)
	if !strings.Contains(strings.ToLower(finalReply), strings.ToLower(answer)) {
		t.Fatalf("the model did not use the bridged answer %q; reply was %q", answer, finalReply)
	}
	t.Logf("[bridge-live] the model used the bridged answer %q: pool, endpoint, card and answer all work", answer)
}

// curvatureBinary returns the real server binary the agent launches as its MCP
// server. The test binary itself cannot serve that role: it has no
// `mcp-ask-user` mode, so the pool must be told which executable to use.
func curvatureBinary(t *testing.T, root string) string {
	t.Helper()
	if configured := strings.TrimSpace(os.Getenv("CURVATURE_MCP_ASK_BIN")); configured != "" {
		if _, err := os.Stat(configured); err != nil {
			t.Skipf("CURVATURE_MCP_ASK_BIN is not usable: %v", err)
		}
		return configured
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go is not on PATH and CURVATURE_MCP_ASK_BIN is unset: %v", err)
	}
	binary := filepath.Join(t.TempDir(), "curvature-server")
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	build := exec.Command(goBin, "build", "-o", binary, "curvature/backend/cmd/curvature-server")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build the backend binary (%v): %s", err, strings.TrimSpace(string(output)))
	}
	return binary
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found above %s", dir)
		}
		dir = parent
	}
}

func liveTimeout(envKey string, fallback time.Duration) time.Duration {
	if raw := strings.TrimSpace(os.Getenv(envKey)); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}
