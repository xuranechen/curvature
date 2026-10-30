package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agenttypes "curvature/backend/internal/agent/types"
	"curvature/backend/internal/session"
)

// PrepareRemovedSession is the edit path without the replacement turn: it drops
// the last user message and everything after it, then leaves the session idle.
//
// It runs after the HTTP layer has stopped the current job and reserved this
// session against queued sends, exactly like PrepareEditedSession, so the two
// operations share one locking and validation story and differ only in whether
// a new message follows.
func (s *Service) PrepareRemovedSession(ctx context.Context, in EditSessionMessageInput) (*session.Session, session.Exchange, error) {
	lock := getSessionSendLock(in.Key)
	lock.Lock()
	defer lock.Unlock()
	syncLock := externalSessionSyncLock(in.RootID, in.Key)
	syncLock.Lock()
	defer syncLock.Unlock()
	m, err := s.Registry.GetSessionManager(in.RootID)
	if err != nil {
		return nil, session.Exchange{}, err
	}
	current, err := m.EditSnapshot(in.Key)
	if err != nil {
		return nil, session.Exchange{}, err
	}
	target, err := ValidateEditTarget(current, in)
	if err != nil {
		return nil, target, err
	}
	agentName := strings.TrimSpace(target.Agent)
	if agentName == "" {
		agentName = session.InferAgentFromSession(current)
	}
	pool := s.Registry.GetAgentPool()
	if pool == nil || agentName == "" {
		return nil, target, errors.New("agent unavailable")
	}
	if _, ok := pool.Config().GetAgent(agentName); !ok {
		return nil, target, errors.New("agent not configured")
	}
	root, err := s.Registry.GetRoot(in.RootID)
	if err != nil {
		return nil, target, err
	}
	runtimeRoot := root.RootPath
	if current.RelatedWorktree != nil && current.RelatedWorktree.Path != "" {
		runtimeRoot = current.RelatedWorktree.Path
	}
	// The next turn must not remember the deleted text. Native agents continue
	// from the previous completed turn (or start fresh when there is none); ACP
	// agents start a new session, which is also how an edit resets them.
	var nextBinding *session.AgentBinding
	if !isACPAgent(pool, agentName) {
		previousSeq := 0
		for _, e := range current.Exchanges {
			if e.Seq < target.Seq && isAgentExchangeRole(e.Role) && (e.Agent == agentName || e.Agent == "") {
				previousSeq = e.Seq
			}
		}
		if previousSeq > 0 {
			_, turnIndex, err := resolveForkTarget(current, previousSeq)
			if err != nil {
				return nil, target, err
			}
			binding, err := m.FindAgentBinding(ctx, in.Key, agentName)
			if err != nil {
				return nil, target, err
			}
			if binding == nil {
				return nil, target, errors.New("agent session binding missing")
			}
			importer, err := s.resolveExternalSessionImporter(agentName)
			if err != nil {
				return nil, target, err
			}
			resolver, ok := importer.(agenttypes.ForkPointResolver)
			if !ok {
				return nil, target, errors.New("agent does not support fork points")
			}
			point, err := resolver.ResolveForkPointByAgentTurnIndex(ctx, agenttypes.ResolveForkPointInput{RootPath: runtimeRoot, AgentSessionID: binding.AgentSessionID, AgentTurnIndex: turnIndex})
			if err != nil {
				return nil, target, err
			}
			stagingKey := fmt.Sprintf("remove-%s-%d", in.Key, time.Now().UnixNano())
			staged, err := pool.GetOrCreate(ctx, agenttypes.OpenSessionInput{SessionKey: stagingKey, AgentName: agentName, RootPath: runtimeRoot, Model: target.Model, Mode: target.Mode, Effort: target.Effort, FastService: target.FastService, PlanMode: current.PlanMode, ForkPoint: point})
			if err != nil {
				return nil, target, err
			}
			defer pool.Close(stagingKey)
			id := strings.TrimSpace(staged.SessionID())
			if id == "" {
				return nil, target, errors.New("fork returned no session id")
			}
			nextBinding = &session.AgentBinding{Agent: agentName, AgentSessionID: id, AgentCtxSeq: previousSeq}
		}
	} else {
		stagingKey := fmt.Sprintf("remove-%s-%d", in.Key, time.Now().UnixNano())
		staged, err := pool.GetOrCreate(ctx, agenttypes.OpenSessionInput{SessionKey: stagingKey, AgentName: agentName, RootPath: runtimeRoot, Model: target.Model, Mode: target.Mode, Effort: target.Effort, PlanMode: current.PlanMode})
		if err != nil {
			return nil, target, err
		}
		defer pool.Close(stagingKey)
		if staged.SessionID() == "" {
			return nil, target, errors.New("new ACP session returned no id")
		}
		nextBinding = &session.AgentBinding{Agent: agentName, AgentSessionID: staged.SessionID(), AgentCtxSeq: 0}
	}
	updated, err := m.TruncateHistoryBefore(ctx, in.Key, target, nextBinding)
	if err != nil {
		return nil, target, err
	}
	for name := range current.AgentCtxSeq {
		if old, ok := pool.Get(agentPoolSessionKey(in.Key, name)); ok {
			old.OnUpdate(nil)
		}
		pool.Close(agentPoolSessionKey(in.Key, name))
	}
	pool.Close(agentPoolSessionKey(in.Key, agentName))
	target.Agent = agentName
	return updated, target, nil
}
