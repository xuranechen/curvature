package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agenttypes "curvature/backend/internal/agent/types"
)

// EditSnapshot can be inspected while a turn is still appending its result.
func (m *Manager) EditSnapshot(key string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.getSessionUnsafe(key, 0)
	if err != nil {
		return nil, err
	}
	snapshot := *s
	snapshot.Exchanges = append([]Exchange{}, s.Exchanges...)
	snapshot.AgentCtxSeq = make(map[string]int, len(s.AgentCtxSeq))
	for name, seq := range s.AgentCtxSeq {
		snapshot.AgentCtxSeq[name] = seq
	}
	return &snapshot, nil
}

// TruncateHistoryBefore drops the last user message and everything after it
// without sending a replacement. It shares the edit path — same validation,
// journal and rollback — so a crash mid-way recovers the same way an edit does.
func (m *Manager) TruncateHistoryBefore(ctx context.Context, key string, target Exchange, binding *AgentBinding) (*Session, error) {
	return m.ReplaceHistoryBefore(ctx, key, target, binding)
}

// LatestUserExchange returns the last user exchange, or nil when the session has
// none. Edits and deletions only ever target this one.
func LatestUserExchange(exchanges []Exchange) *Exchange {
	for i := len(exchanges) - 1; i >= 0; i-- {
		if exchanges[i].Role == "user" {
			return &exchanges[i]
		}
	}
	return nil
}

// MatchesExchange reports whether target identifies want. The timestamp is the
// identity — message content can repeat — while seq is only compared when both
// sides know it: a request that says seq 0 (a message still in flight, or one
// whose seq a resend already bumped) must not look like a mismatch.
func MatchesExchange(want *Exchange, target Exchange) bool {
	if want == nil {
		return false
	}
	if target.Seq > 0 && want.Seq != target.Seq {
		return false
	}
	return want.Content == target.Content && want.Timestamp.Equal(target.Timestamp)
}

// ReplaceHistoryBefore keeps the session identity while discarding the edited
// message and its reply. Callers must exclude sends and external-history imports.
// All old agent bindings are invalidated: another agent may also know the old text.
func (m *Manager) ReplaceHistoryBefore(ctx context.Context, key string, target Exchange, binding *AgentBinding) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.getSessionUnsafe(key, 0)
	if err != nil {
		return nil, err
	}
	copySession := *s
	s = &copySession
	latest := LatestUserExchange(s.Exchanges)
	if !MatchesExchange(latest, target) {
		return nil, errors.New("message changed; reload the session before editing")
	}
	path, err := m.exchangePath(key)
	if err != nil {
		return nil, err
	}
	auxPath, err := m.auxPath(key)
	if err != nil {
		return nil, err
	}
	aux, err := m.loadExchangeAuxEntries(key, 0)
	if err != nil {
		return nil, err
	}
	encode := func(v any) ([]byte, error) { return json.Marshal(v) }
	var oldHistory, newHistory, oldAux, newAux []byte
	for _, e := range s.Exchanges {
		line, err := encode(e)
		if err != nil {
			return nil, err
		}
		oldHistory = append(oldHistory, append(line, '\n')...)
		if e.Seq < target.Seq {
			newHistory = append(newHistory, append(line, '\n')...)
		}
	}
	for _, e := range aux {
		line, err := encode(e)
		if err != nil {
			return nil, err
		}
		oldAux = append(oldAux, append(line, '\n')...)
		if e.Seq < target.Seq {
			newAux = append(newAux, append(line, '\n')...)
		}
	}
	db, err := m.ensureSessionMetaDBUnsafe()
	if err != nil {
		return nil, err
	}
	if _, err = db.ExecContext(ctx, "INSERT INTO session_edit_journal(session_key, history, auxiliary) VALUES (?, ?, ?)", key, oldHistory, oldAux); err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	updatedAt := m.now().UTC()
	if _, err = tx.ExecContext(ctx, "UPDATE sessions SET history_revision = history_revision + 1, last_context_window_total_tokens = 0, last_context_window_model_context_window = 0, updated_at = ? WHERE key = ?", updatedAt.Format(time.RFC3339Nano), key); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM session_agent_bindings WHERE session_key = ?", key); err != nil {
		return nil, err
	}
	if binding != nil {
		if _, err = tx.ExecContext(ctx, upsertAgentBindingSQL, key, binding.Agent, binding.AgentSessionID, binding.AgentCtxSeq); err != nil {
			return nil, err
		}
	}
	restore := func(cause error) (*Session, error) {
		_ = tx.Rollback()
		return nil, errors.Join(cause, m.recoverHistoryEdit(key))
	}
	if err = m.root.WriteMetaFile(auxPath, newAux); err != nil {
		return nil, err
	}
	if err = m.root.WriteMetaFile(path, newHistory); err != nil {
		return restore(err)
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM session_edit_journal WHERE session_key = ?", key); err != nil {
		return restore(err)
	}
	if err = tx.Commit(); err != nil {
		return restore(fmt.Errorf("commit edited history: %w", err))
	}
	kept := make([]Exchange, 0, len(s.Exchanges))
	for _, e := range s.Exchanges {
		if e.Seq < target.Seq {
			kept = append(kept, e)
		}
	}
	s.Exchanges = kept
	s.AgentCtxSeq = map[string]int{}
	if binding != nil {
		s.AgentCtxSeq[binding.Agent] = binding.AgentCtxSeq
	}
	s.LastContextWindow = agenttypes.ContextWindow{}
	s.HistoryRevision++
	s.UpdatedAt = updatedAt
	m.sessions[key] = s
	delete(m.pendingToolCalls, key)
	return s, nil
}

// A journal entry survives a process crash between file replacement and the
// binding transaction. Its deletion is committed together with the new binding.
func (m *Manager) recoverHistoryEdit(key string) error {
	db, err := m.ensureSessionMetaDBUnsafe()
	if err != nil {
		return err
	}
	var history, auxiliary []byte
	err = db.QueryRow("SELECT history, auxiliary FROM session_edit_journal WHERE session_key = ?", key).Scan(&history, &auxiliary)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	path, err := m.exchangePath(key)
	if err != nil {
		return err
	}
	auxPath, err := m.auxPath(key)
	if err != nil {
		return err
	}
	if err = m.root.WriteMetaFile(path, history); err != nil {
		return err
	}
	if err = m.root.WriteMetaFile(auxPath, auxiliary); err != nil {
		return err
	}
	_, err = db.Exec("DELETE FROM session_edit_journal WHERE session_key = ?", key)
	delete(m.sessions, key)
	return err
}
