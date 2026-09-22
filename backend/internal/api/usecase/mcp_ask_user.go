package usecase

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	agenttypes "curvature/backend/internal/agent/types"
)

// AskUserViaMCPInput is a question that reached Curvature over the ask-user MCP
// server the client attached to a session.
type AskUserViaMCPInput struct {
	SessionKey string
	Agent      string
	Request    agenttypes.MCPAskUserRequest
	Timeout    time.Duration
}

// AskUserViaMCP surfaces an MCP tool call as an ask-user card and blocks until
// the human answers. It is the same card the native ask-user paths use, so the
// bridge needs no UI of its own.
func (s *Service) AskUserViaMCP(ctx context.Context, in AskUserViaMCPInput) (agenttypes.MCPAskUserAnswer, error) {
	if err := s.ensureRegistry(); err != nil {
		return agenttypes.MCPAskUserAnswer{}, err
	}
	sessionKey := strings.TrimSpace(in.SessionKey)
	if sessionKey == "" {
		return agenttypes.MCPAskUserAnswer{}, errors.New("session key required")
	}
	agentName := strings.TrimSpace(in.Agent)
	if agentName == "" {
		return agenttypes.MCPAskUserAnswer{}, errors.New("agent required")
	}
	if len(in.Request.Questions) == 0 {
		return agenttypes.MCPAskUserAnswer{}, errors.New("at least one question is required")
	}
	pool := s.Registry.GetAgentPool()
	if pool == nil {
		return agenttypes.MCPAskUserAnswer{}, errors.New("agent pool unavailable")
	}
	// sessionKey is the pool session key the bridge process was given in its
	// environment, which is exactly the key the pool stores the session under
	// (callers open sessions with agentPoolSessionKey). Look it up verbatim:
	// re-prefixing it here would look for an agent-prefixed pool key.
	sess, ok := pool.Get(sessionKey)
	if !ok || sess == nil {
		// A mismatch here means the bridge process was handed a session key the
		// pool does not know, which is worth naming explicitly.
		log.Printf("[mcp-ask-user] session.not_found key=%q agent=%q known=%v", sessionKey, agentName, pool.SessionKeys())
		return agenttypes.MCPAskUserAnswer{}, errors.New("agent session not found")
	}
	asker, ok := sess.(agenttypes.MCPAskUserSession)
	if !ok {
		return agenttypes.MCPAskUserAnswer{}, errors.New("session cannot ask the user")
	}
	if in.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, in.Timeout)
		defer cancel()
	}
	return asker.AskUserQuestion(ctx, in.Request)
}
