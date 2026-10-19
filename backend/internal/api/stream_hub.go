package api

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	agenttypes "curvature/backend/internal/agent/types"
	"curvature/backend/internal/api/usecase"
	"curvature/backend/internal/e2ee"
	"curvature/backend/internal/notify"
	"curvature/backend/internal/session"

	"github.com/gorilla/websocket"
)

type StreamHub struct {
	mu              sync.RWMutex
	e2eeManager     *e2ee.Manager
	clients         map[string]*websocket.Conn
	connLocks       map[*websocket.Conn]*sync.Mutex
	sessionClients  map[string]map[string]struct{}
	pendingSessions map[string]*SessionPendingState
	replayStates    map[string]*ClientReplayState
	completed       map[string]*CompletedSessionState
	nextTurnID      uint64
}

type PendingUserMessage struct {
	Agent       string    `json:"agent,omitempty"`
	Model       string    `json:"model,omitempty"`
	Mode        string    `json:"mode,omitempty"`
	Effort      string    `json:"effort,omitempty"`
	FastService string    `json:"fast_service,omitempty"`
	PlanMode    bool      `json:"plan_mode,omitempty"`
	Content     string    `json:"content"`
	Timestamp   time.Time `json:"timestamp"`
}

type QueuedUserMessage struct {
	ID string `json:"id"`
	PendingUserMessage
	ClientCtx usecase.ClientContext `json:"-"`
}

type SessionPendingState struct {
	RootID          string
	SessionTitle    string
	Active          bool
	QueueFrozen     bool
	User            *PendingUserMessage
	Queue           []QueuedUserMessage
	ReplyingList    []StreamEvent
	BaseExchangeSeq int
	NextEventSeq    uint64
	// TurnID identifies this turn uniquely and for the lifetime of the process.
	// BaseExchangeSeq alone is not enough: two consecutive turns can compute the
	// same value when the previous turn's exchanges were not persisted yet (a
	// queued message, or a resend after a reconnect), and a stale event cursor
	// would then be mistaken for a cursor of the current turn.
	TurnID    uint64
	Summary   string
	UpdatedAt time.Time
}

type ClientStreamStatus string

const (
	ClientStreamStatusReplay ClientStreamStatus = "replay"
	ClientStreamStatusLive   ClientStreamStatus = "live"
)

type ClientReplayState struct {
	Status       ClientStreamStatus
	LastEventSeq uint64
}

type CompletedSessionState struct {
	RequestID string
	Completed time.Time
}

type ReplyingSessionState struct {
	RootID       string    `json:"rootId"`
	SessionKey   string    `json:"sessionKey"`
	SessionTitle string    `json:"sessionTitle"`
	Status       string    `json:"status"`
	Summary      string    `json:"summary"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type PendingSessionSnapshot struct {
	RootID       string
	SessionTitle string
	Summary      string
	UpdatedAt    time.Time
}

type replayStep struct {
	events []StreamEvent
	live   bool
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func NewStreamHub(e2eeManager *e2ee.Manager) *StreamHub {
	return &StreamHub{
		e2eeManager:     e2eeManager,
		clients:         make(map[string]*websocket.Conn),
		connLocks:       make(map[*websocket.Conn]*sync.Mutex),
		sessionClients:  make(map[string]map[string]struct{}),
		pendingSessions: make(map[string]*SessionPendingState),
		replayStates:    make(map[string]*ClientReplayState),
		completed:       make(map[string]*CompletedSessionState),
	}
}

func pendingClientKey(clientID, sessionKey string) string {
	return clientID + "::" + sessionKey
}

func cloneEvent(ev StreamEvent) StreamEvent {
	return StreamEvent{Type: ev.Type, Data: ev.Data, EventCursor: ev.EventCursor}
}

func cloneUserExchange(msg *PendingUserMessage) *session.Exchange {
	if msg == nil {
		return nil
	}
	return &session.Exchange{
		Role:        "user",
		Agent:       msg.Agent,
		Model:       msg.Model,
		Mode:        msg.Mode,
		Effort:      msg.Effort,
		FastService: msg.FastService,
		Content:     msg.Content,
		Timestamp:   msg.Timestamp,
	}
}

func buildSessionStreamResponse(rootID, sessionKey string, event *StreamEvent) WSResponse {
	return WSResponse{
		Type: "session.stream",
		Payload: map[string]any{
			"root_id":     rootID,
			"session_key": sessionKey,
			"event":       event,
		},
	}
}

func buildSessionDoneResponse(rootID, sessionKey, requestID string, replay bool) WSResponse {
	payload := map[string]any{
		"root_id":     rootID,
		"session_key": sessionKey,
	}
	if replay {
		payload["replay"] = true
	}
	return WSResponse{
		ID:      requestID,
		Type:    "session.done",
		Payload: payload,
	}
}

func buildSlashCommandStreamResponse(rootID, sessionKey, command, requestID string, event *StreamEvent) WSResponse {
	return WSResponse{
		ID:   requestID,
		Type: "session.slash_command.stream",
		Payload: map[string]any{
			"root_id":     rootID,
			"session_key": sessionKey,
			"command":     command,
			"request_id":  requestID,
			"event":       event,
		},
	}
}

func buildSlashCommandDoneResponse(rootID, sessionKey, command, requestID string) WSResponse {
	return WSResponse{
		ID:   requestID,
		Type: "session.slash_command.done",
		Payload: map[string]any{
			"root_id":     rootID,
			"session_key": sessionKey,
			"command":     command,
			"request_id":  requestID,
		},
	}
}

func buildSessionUserMessageResponse(rootID, sessionKey, sessionType, sessionName, agentName, model, mode, effort, fastService string, planMode bool, content string, timestamp time.Time, queued bool) WSResponse {
	queueState := "active"
	if queued {
		queueState = "dequeued"
	}
	sessionPayload := map[string]any{
		"key":          sessionKey,
		"type":         sessionType,
		"agent":        agentName,
		"model":        model,
		"mode":         mode,
		"effort":       effort,
		"fast_service": fastService,
		"plan_mode":    planMode,
		"created_at":   timestamp,
		"updated_at":   timestamp,
	}
	if strings.TrimSpace(sessionName) != "" {
		sessionPayload["name"] = sessionName
	}
	return WSResponse{
		Type: "session.user_message",
		Payload: map[string]any{
			"root_id":     rootID,
			"session_key": sessionKey,
			"session":     sessionPayload,
			"exchange": map[string]any{
				"role":         "user",
				"agent":        agentName,
				"model":        model,
				"mode":         mode,
				"effort":       effort,
				"fast_service": fastService,
				"content":      content,
				"timestamp":    timestamp,
				"queued":       queued,
				"queue_state":  queueState,
			},
		},
	}
}

func buildSessionQueueUpdatedResponse(rootID, sessionKey string, queue []QueuedUserMessage, frozen bool) WSResponse {
	return WSResponse{
		Type: "session.queue.updated",
		Payload: map[string]any{
			"root_id":      rootID,
			"session_key":  sessionKey,
			"queue":        queue,
			"queue_frozen": frozen,
		},
	}
}

func (h *StreamHub) ensurePendingSessionLocked(sessionKey string) *SessionPendingState {
	state := h.pendingSessions[sessionKey]
	if state == nil {
		state = &SessionPendingState{}
		h.pendingSessions[sessionKey] = state
	}
	return state
}

func (h *StreamHub) clearReplayStatesForSessionLocked(sessionKey string) {
	for _, replayKey := range h.getReplayKeyListLocked(sessionKey, "") {
		delete(h.replayStates, replayKey)
	}
}

func (h *StreamHub) RegisterClient(clientID string, conn *websocket.Conn) {
	if blank(clientID) || conn == nil {
		return
	}
	h.mu.Lock()
	h.clients[clientID] = conn
	if _, ok := h.connLocks[conn]; !ok {
		h.connLocks[conn] = &sync.Mutex{}
	}
	h.mu.Unlock()
}

func (h *StreamHub) UnregisterClient(clientID string, conn *websocket.Conn) {
	if blank(clientID) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	existing := h.clients[clientID]
	if existing != conn {
		return
	}
	delete(h.clients, clientID)
	delete(h.connLocks, conn)
	for sessionKey, clientSet := range h.sessionClients {
		delete(clientSet, clientID)
		if len(clientSet) == 0 {
			delete(h.sessionClients, sessionKey)
		}
	}
	for _, replayKey := range h.getReplayKeyListLocked("", clientID) {
		delete(h.replayStates, replayKey)
	}
}

func (h *StreamHub) BindSessionClient(sessionKey, clientID string) {
	if blank(sessionKey) || blank(clientID) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[clientID]; !ok {
		return
	}
	clientSet := h.sessionClients[sessionKey]
	if clientSet == nil {
		clientSet = make(map[string]struct{})
		h.sessionClients[sessionKey] = clientSet
	}
	clientSet[clientID] = struct{}{}
}

func (h *StreamHub) GetSessionClientIDs(sessionKey string, liveOnly bool) []string {
	if blank(sessionKey) {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	clientSet := h.sessionClients[sessionKey]
	if len(clientSet) == 0 {
		return nil
	}
	out := make([]string, 0, len(clientSet))
	for clientID := range clientSet {
		if h.clients[clientID] == nil {
			continue
		}
		if liveOnly && h.isReplayClientLocked(clientID, sessionKey) {
			continue
		}
		out = append(out, clientID)
	}
	return out
}

func (h *StreamHub) getAllClientIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.clients) == 0 {
		return nil
	}
	clientIDs := make([]string, 0, len(h.clients))
	for clientID, conn := range h.clients {
		if conn != nil {
			clientIDs = append(clientIDs, clientID)
		}
	}
	return clientIDs
}

func (h *StreamHub) SetPendingUser(rootID, sessionKey, sessionTitle, agent, model, mode, effort, fastService string, planMode bool, content string) *PendingUserMessage {
	return h.SetPendingUserAt(rootID, sessionKey, sessionTitle, agent, model, mode, effort, fastService, planMode, content, time.Now().UTC())
}

func (h *StreamHub) SetPendingUserAt(rootID, sessionKey, sessionTitle, agent, model, mode, effort, fastService string, planMode bool, content string, timestamp time.Time, baseExchangeSeq ...int) *PendingUserMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	} else {
		timestamp = timestamp.UTC()
	}
	state := h.ensurePendingSessionLocked(sessionKey)
	delete(h.completed, sessionKey)
	state.RootID = rootID
	state.SessionTitle = strings.TrimSpace(sessionTitle)
	state.Active = true
	state.User = &PendingUserMessage{
		Agent:       agent,
		Model:       model,
		Mode:        mode,
		Effort:      effort,
		FastService: fastService,
		PlanMode:    planMode,
		Content:     content,
		Timestamp:   timestamp,
	}
	state.ReplyingList = nil
	state.BaseExchangeSeq = 0
	if len(baseExchangeSeq) > 0 && baseExchangeSeq[0] > 0 {
		state.BaseExchangeSeq = baseExchangeSeq[0]
	}
	h.assignTurnIDLocked(state)
	state.NextEventSeq = 0
	state.Summary = ""
	state.UpdatedAt = state.User.Timestamp
	h.clearReplayStatesForSessionLocked(sessionKey)
	return &PendingUserMessage{
		Agent:       state.User.Agent,
		Model:       state.User.Model,
		Mode:        state.User.Mode,
		Effort:      state.User.Effort,
		FastService: state.User.FastService,
		PlanMode:    state.User.PlanMode,
		Content:     state.User.Content,
		Timestamp:   state.User.Timestamp,
	}
}

func (h *StreamHub) IsSessionReplying(sessionKey string) bool {
	if blank(sessionKey) {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	state := h.pendingSessions[sessionKey]
	return state != nil && state.Active
}

func cloneQueue(queue []QueuedUserMessage) []QueuedUserMessage {
	if len(queue) == 0 {
		return nil
	}
	out := make([]QueuedUserMessage, len(queue))
	copy(out, queue)
	return out
}

func (h *StreamHub) queueSnapshot(sessionKey string) (string, []QueuedUserMessage, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	state := h.pendingSessions[sessionKey]
	if state == nil {
		return "", nil, false
	}
	return state.RootID, cloneQueue(state.Queue), state.QueueFrozen
}

func (h *StreamHub) EnqueueSessionMessage(rootID, sessionKey, sessionTitle string, item QueuedUserMessage) []QueuedUserMessage {
	if item.ID == "" {
		item.ID = time.Now().UTC().Format("20060102150405.000000000")
	}
	if item.Timestamp.IsZero() {
		item.Timestamp = time.Now().UTC()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.ensurePendingSessionLocked(sessionKey)
	delete(h.completed, sessionKey)
	state.RootID = rootID
	if strings.TrimSpace(sessionTitle) != "" {
		state.SessionTitle = strings.TrimSpace(sessionTitle)
	}
	state.Queue = append(state.Queue, item)
	state.UpdatedAt = item.Timestamp
	h.clearReplayStatesForSessionLocked(sessionKey)
	return cloneQueue(state.Queue)
}

func (h *StreamHub) RemoveQueuedSessionMessage(sessionKey, queueID string) []QueuedUserMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil || queueID == "" {
		return nil
	}
	next := state.Queue[:0]
	for _, item := range state.Queue {
		if item.ID == queueID {
			continue
		}
		next = append(next, item)
	}
	state.Queue = next
	state.UpdatedAt = time.Now().UTC()
	h.clearReplayStatesForSessionLocked(sessionKey)
	return cloneQueue(state.Queue)
}

func (h *StreamHub) UpdateQueuedSessionMessage(sessionKey, queueID, content string) []QueuedUserMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil || queueID == "" {
		return nil
	}
	for i := range state.Queue {
		if state.Queue[i].ID == queueID {
			state.Queue[i].Content = content
			break
		}
	}
	state.UpdatedAt = time.Now().UTC()
	h.clearReplayStatesForSessionLocked(sessionKey)
	return cloneQueue(state.Queue)
}

func (h *StreamHub) FreezeQueuedSessionMessages(sessionKey string) ([]QueuedUserMessage, bool) {
	if blank(sessionKey) {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil || len(state.Queue) == 0 {
		return nil, false
	}
	state.QueueFrozen = true
	state.UpdatedAt = time.Now().UTC()
	return cloneQueue(state.Queue), true
}

func (h *StreamHub) UnfreezeQueuedSessionMessages(sessionKey string) ([]QueuedUserMessage, bool) {
	if blank(sessionKey) {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil || !state.QueueFrozen {
		if state == nil {
			return nil, false
		}
		return cloneQueue(state.Queue), false
	}
	state.QueueFrozen = false
	state.UpdatedAt = time.Now().UTC()
	return cloneQueue(state.Queue), true
}

func (h *StreamHub) PopQueuedSessionMessage(sessionKey, queueID string) (QueuedUserMessage, []QueuedUserMessage, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil || len(state.Queue) == 0 {
		return QueuedUserMessage{}, nil, false
	}
	index := 0
	if trimmedQueueID := strings.TrimSpace(queueID); trimmedQueueID != "" {
		index = -1
		for i := range state.Queue {
			if state.Queue[i].ID == trimmedQueueID {
				index = i
				break
			}
		}
		if index < 0 {
			return QueuedUserMessage{}, cloneQueue(state.Queue), false
		}
	} else if state.QueueFrozen {
		return QueuedUserMessage{}, cloneQueue(state.Queue), false
	}
	item := state.Queue[index]
	state.Queue = append(state.Queue[:index], state.Queue[index+1:]...)
	state.UpdatedAt = time.Now().UTC()
	h.clearReplayStatesForSessionLocked(sessionKey)
	return item, cloneQueue(state.Queue), true
}

func (h *StreamHub) PromoteQueuedSessionMessage(sessionKey, queueID string) ([]QueuedUserMessage, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil || len(state.Queue) == 0 || strings.TrimSpace(queueID) == "" {
		return nil, false
	}
	index := -1
	for i := range state.Queue {
		if state.Queue[i].ID == queueID {
			index = i
			break
		}
	}
	if index < 0 {
		return cloneQueue(state.Queue), false
	}
	if index > 0 {
		item := state.Queue[index]
		copy(state.Queue[1:index+1], state.Queue[0:index])
		state.Queue[0] = item
	}
	state.QueueFrozen = false
	state.UpdatedAt = time.Now().UTC()
	h.clearReplayStatesForSessionLocked(sessionKey)
	return cloneQueue(state.Queue), true
}

func (h *StreamHub) SetPendingReply(rootID, sessionKey, sessionTitle string) {
	if blank(sessionKey) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.ensurePendingSessionLocked(sessionKey)
	delete(h.completed, sessionKey)
	state.RootID = rootID
	state.SessionTitle = strings.TrimSpace(sessionTitle)
	state.Active = true
	// Sub-agent sessions start a turn here instead of via SetPendingUserAt, so
	// they need their own turn identity too.
	h.assignTurnIDLocked(state)
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
}

// assignTurnIDLocked stamps the session with a fresh turn identity. The caller
// must hold h.mu.
func (h *StreamHub) assignTurnIDLocked(state *SessionPendingState) {
	if state == nil {
		return
	}
	h.nextTurnID++
	state.TurnID = h.nextTurnID
}

func (h *StreamHub) GetPendingUserExchange(sessionKey string) *session.Exchange {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil {
		return nil
	}
	return cloneUserExchange(state.User)
}

func (h *StreamHub) PendingSessionSnapshot(sessionKey string) PendingSessionSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	state := h.pendingSessions[sessionKey]
	if state == nil {
		return PendingSessionSnapshot{}
	}
	return PendingSessionSnapshot{
		RootID:       state.RootID,
		SessionTitle: state.SessionTitle,
		Summary:      state.Summary,
		UpdatedAt:    state.UpdatedAt,
	}
}

func (h *StreamHub) AppendReplyEvent(sessionKey string, event StreamEvent) StreamEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.ensurePendingSessionLocked(sessionKey)
	if state.TurnID == 0 {
		// Defensive: a turn that started without an explicit turn-start call still
		// needs a unique identity, otherwise its cursors could be confused with a
		// later turn's.
		h.assignTurnIDLocked(state)
	}
	state.NextEventSeq++
	event.EventCursor = formatEventCursor(state.TurnID, state.BaseExchangeSeq, state.NextEventSeq)
	if coalesceUserShellStreamEvent(state, event) {
		state.UpdatedAt = time.Now().UTC()
		return event
	}
	state.ReplyingList = append(state.ReplyingList, cloneEvent(event))
	state.UpdatedAt = time.Now().UTC()
	if event.Type == "message_chunk" {
		if chunk, ok := event.Data.(agenttypes.MessageChunk); ok {
			state.Summary = lastRunes(state.Summary+chunk.Content, notify.BodyMaxRunes)
		}
	} else if isAuxiliarySummaryBoundary(event.Type) {
		state.Summary = ""
	}
	return event
}

// formatEventCursor encodes everything needed to place an event: the turn it
// belongs to, the exchange seq that turn started from, and the event seq within
// the turn. The turn id is what makes a cursor unambiguous across turns.
func formatEventCursor(turnID uint64, baseExchangeSeq int, eventSeq uint64) string {
	return fmt.Sprintf("%d:%d:%d", turnID, baseExchangeSeq, eventSeq)
}

// parseEventCursor decodes a turn id, base exchange seq and event seq. Cursors
// that do not carry a turn id (legacy two-part values) are rejected, which makes
// the caller fall back to replaying the whole turn instead of trusting them.
func parseEventCursor(value string) (uint64, int, uint64, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	turnID, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || turnID == 0 {
		return 0, 0, 0, false
	}
	baseExchangeSeq, err := strconv.Atoi(parts[1])
	if err != nil || baseExchangeSeq < 0 {
		return 0, 0, 0, false
	}
	eventSeq, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	return turnID, baseExchangeSeq, eventSeq, true
}

// resumeEventSeq decides how much of the buffered turn a reconnecting client has
// already seen. It only trusts a cursor that belongs to the turn currently in the
// buffer: a cursor from an earlier turn must never be read as "caught up", or the
// client would silently miss the whole turn (the relay-reconnect incident).
func resumeEventSeq(state *SessionPendingState, eventCursor string) uint64 {
	if state == nil {
		return 0
	}
	turnID, baseExchangeSeq, eventSeq, ok := parseEventCursor(eventCursor)
	if !ok || turnID != state.TurnID || baseExchangeSeq != state.BaseExchangeSeq || eventSeq > state.NextEventSeq {
		return 0
	}
	return eventSeq
}

func isAuxiliarySummaryBoundary(eventType string) bool {
	switch agenttypes.EventType(eventType) {
	case agenttypes.EventTypeThoughtChunk,
		agenttypes.EventTypeToolCall,
		agenttypes.EventTypeToolUpdate,
		agenttypes.EventTypeTodoUpdate,
		agenttypes.EventTypePlanUpdate,
		agenttypes.EventTypeCompact:
		return true
	default:
		return false
	}
}

const maxReplayUserShellStreamBytes = 256 * 1024

func coalesceUserShellStreamEvent(state *SessionPendingState, event StreamEvent) bool {
	if state == nil || event.Type != string(agenttypes.EventTypeToolUpdate) {
		return false
	}
	next, ok := event.Data.(agenttypes.ToolCall)
	if !ok || next.Meta == nil || next.Meta["source"] != "userShell" || next.Meta["phase"] != "stream" || next.CallID == "" {
		return false
	}
	for i := len(state.ReplyingList) - 1; i >= 0; i-- {
		prevEvent := state.ReplyingList[i]
		if prevEvent.Type != event.Type {
			continue
		}
		prev, ok := prevEvent.Data.(agenttypes.ToolCall)
		if !ok || prev.CallID != next.CallID || prev.Meta == nil || prev.Meta["source"] != "userShell" || prev.Meta["phase"] != "stream" {
			continue
		}
		merged := prev
		merged.Status = next.Status
		merged.Meta = map[string]any{}
		for key, value := range prev.Meta {
			merged.Meta[key] = value
		}
		for key, value := range next.Meta {
			merged.Meta[key] = value
		}
		merged.Meta["replaySnapshot"] = true
		text := toolCallText(prev.Content) + toolCallText(next.Content)
		if len(text) > maxReplayUserShellStreamBytes {
			text = text[len(text)-maxReplayUserShellStreamBytes:]
			merged.Meta["replayTruncated"] = true
			merged.Meta["replayTruncation"] = "tail"
		}
		merged.Content = []agenttypes.ToolCallContentItem{{Type: "text", Text: text}}
		state.ReplyingList[i] = StreamEvent{Type: event.Type, Data: merged, EventCursor: event.EventCursor}
		return true
	}
	return false
}

func toolCallText(items []agenttypes.ToolCallContentItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, item := range items {
		b.WriteString(item.Text)
	}
	return b.String()
}

func (h *StreamHub) ListReplyingSessions() []ReplyingSessionState {
	h.mu.RLock()
	defer h.mu.RUnlock()
	items := make([]ReplyingSessionState, 0, len(h.pendingSessions))
	for sessionKey, state := range h.pendingSessions {
		if state == nil || !state.Active || blank(sessionKey) || blank(state.RootID) {
			continue
		}
		items = append(items, ReplyingSessionState{
			RootID:       state.RootID,
			SessionKey:   sessionKey,
			SessionTitle: state.SessionTitle,
			Status:       "replying",
			Summary:      state.Summary,
			UpdatedAt:    state.UpdatedAt,
		})
	}
	return items
}

func (h *StreamHub) ReplayPending(rootID, clientID, sessionKey, eventCursor string) {
	h.mu.Lock()
	lastEventSeq := resumeEventSeq(h.pendingSessions[sessionKey], eventCursor)
	h.replayStates[pendingClientKey(clientID, sessionKey)] = &ClientReplayState{
		Status:       ClientStreamStatusReplay,
		LastEventSeq: lastEventSeq,
	}
	h.mu.Unlock()

	h.replayQueueToClient(rootID, clientID, sessionKey)
	for {
		step := h.collectReplayStep(clientID, sessionKey)
		h.replayStepToClient(rootID, clientID, sessionKey, step.events)
		if step.live {
			h.replayCompletionToClient(rootID, clientID, sessionKey)
			return
		}
	}
}

func (h *StreamHub) HasReplayClients(rootID, sessionKey string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.hasReplayClientsLocked(sessionKey)
}

func (h *StreamHub) hasReplayClientsLocked(sessionKey string) bool {
	for _, replayKey := range h.getReplayKeyListLocked(sessionKey, "") {
		replay := h.replayStates[replayKey]
		if replay != nil && replay.Status == ClientStreamStatusReplay {
			return true
		}
	}
	return false
}

// pendingReplayCleanupTimeout bounds how long a completed turn keeps its replay
// buffer for clients that are still reconnecting. It must never block the turn:
// a stale client otherwise keeps the session marked as replying forever and all
// later messages are queued behind a turn that already finished.
const pendingReplayCleanupTimeout = 2 * time.Minute

func (h *StreamHub) ClearSessionPending(sessionKey string) {
	if blank(sessionKey) {
		return
	}
	h.mu.Lock()
	state := h.pendingSessions[sessionKey]
	if state == nil {
		h.mu.Unlock()
		return
	}
	// Mark the turn finished before touching the replay buffer. IsSessionReplying
	// only reads Active, so a slow or dead replay client can no longer make the
	// session look busy.
	state.Active = false
	state.User = nil
	state.Summary = ""
	state.UpdatedAt = time.Now().UTC()
	hasReplay := h.hasReplayClientsLocked(sessionKey)
	if !hasReplay {
		state.ReplyingList = nil
	}
	if len(state.Queue) == 0 && !hasReplay {
		delete(h.pendingSessions, sessionKey)
		h.clearReplayStatesForSessionLocked(sessionKey)
	}
	h.mu.Unlock()

	if hasReplay {
		go h.clearPendingAfterReplay(sessionKey)
	}
}

// clearPendingAfterReplay drops the buffer once the reconnecting clients have
// caught up. It runs in the background so a stuck client cannot pin the session
// in the replying state or block the next queued message.
func (h *StreamHub) clearPendingAfterReplay(sessionKey string) {
	deadline := time.Now().Add(pendingReplayCleanupTimeout)
	for time.Now().Before(deadline) {
		if !h.HasReplayClients("", sessionKey) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.pendingSessions[sessionKey]
	if state == nil || state.Active || len(state.Queue) > 0 {
		return
	}
	delete(h.pendingSessions, sessionKey)
	h.clearReplayStatesForSessionLocked(sessionKey)
}

func (h *StreamHub) SendToClient(clientID string, resp WSResponse) {
	if blank(clientID) {
		return
	}
	h.mu.RLock()
	conn := h.clients[clientID]
	h.mu.RUnlock()
	if conn == nil {
		return
	}
	_ = h.WriteJSON(clientID, conn, resp)
}

func (h *StreamHub) BroadcastAll(resp WSResponse) {
	for _, clientID := range h.getAllClientIDs() {
		h.SendToClient(clientID, resp)
	}
}

func (h *StreamHub) BroadcastSessionStream(rootID, sessionKey string, event *StreamEvent) {
	if event == nil {
		return
	}
	stored := h.AppendReplyEvent(sessionKey, *event)
	for _, clientID := range h.GetSessionClientIDs(sessionKey, true) {
		resp := buildSessionStreamResponse(rootID, sessionKey, &stored)
		h.SendToClient(clientID, resp)
	}
}

func (h *StreamHub) BroadcastSessionDone(rootID, sessionKey, requestID string) {
	h.mu.Lock()
	h.completed[sessionKey] = &CompletedSessionState{
		RequestID: requestID,
		Completed: time.Now().UTC(),
	}
	h.mu.Unlock()
	// Replay clients receive their completion from replayCompletionToClient once
	// they catch up. Sending it here as well would duplicate session.done for a
	// client that is still replaying when the turn finishes.
	resp := buildSessionDoneResponse(rootID, sessionKey, requestID, false)
	for _, clientID := range h.GetSessionClientIDs(sessionKey, true) {
		h.SendToClient(clientID, resp)
	}
}

func (h *StreamHub) BroadcastSessionUserMessage(
	rootID string,
	sessionKey string,
	sessionType string,
	sessionName string,
	agentName string,
	model string,
	mode string,
	effort string,
	fastService string,
	planMode bool,
	content string,
	excludeClientID string,
	queued bool,
) {
	h.BroadcastSessionUserMessageAt(rootID, sessionKey, sessionType, sessionName, agentName, model, mode, effort, fastService, planMode, content, time.Now().UTC(), excludeClientID, queued)
}

func (h *StreamHub) BroadcastSessionUserMessageAt(
	rootID string,
	sessionKey string,
	sessionType string,
	sessionName string,
	agentName string,
	model string,
	mode string,
	effort string,
	fastService string,
	planMode bool,
	content string,
	timestamp time.Time,
	excludeClientID string,
	queued bool,
	baseExchangeSeq ...int,
) {
	pendingUser := h.SetPendingUserAt(rootID, sessionKey, sessionName, agentName, model, mode, effort, fastService, planMode, content, timestamp, baseExchangeSeq...)
	resp := buildSessionUserMessageResponse(rootID, sessionKey, sessionType, sessionName, agentName, model, mode, effort, fastService, planMode, content, pendingUser.Timestamp, queued)
	for _, clientID := range h.GetSessionClientIDs(sessionKey, false) {
		if clientID == excludeClientID {
			continue
		}
		h.SendToClient(clientID, resp)
	}
}

func (h *StreamHub) BroadcastSessionQueueUpdated(rootID, sessionKey string, queue []QueuedUserMessage) {
	_, _, frozen := h.queueSnapshot(sessionKey)
	resp := buildSessionQueueUpdatedResponse(rootID, sessionKey, queue, frozen)
	for _, clientID := range h.GetSessionClientIDs(sessionKey, false) {
		h.SendToClient(clientID, resp)
	}
}

func lastRunes(value string, max int) string {
	if max <= 0 || value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return "..." + string(runes[len(runes)-max:])
}

func (h *StreamHub) WriteJSON(clientID string, conn *websocket.Conn, value any) error {
	if conn == nil {
		return nil
	}
	lock := h.getConnLock(conn)
	lock.Lock()
	defer lock.Unlock()
	if h.e2eeManager != nil && h.e2eeManager.Enabled() {
		if resp, ok := value.(WSResponse); ok && resp.Type == "e2ee.error" {
			return conn.WriteJSON(resp)
		}
		sess, err := h.e2eeManager.SessionForClient(clientID)
		if err != nil {
			return nil
		}
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		envelope, err := e2ee.EncryptBytes(sess.Key, payload)
		if err != nil {
			return err
		}
		return conn.WriteJSON(envelope)
	}
	return conn.WriteJSON(value)
}

func (h *StreamHub) getConnLock(conn *websocket.Conn) *sync.Mutex {
	h.mu.RLock()
	lock := h.connLocks[conn]
	h.mu.RUnlock()
	if lock != nil {
		return lock
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing := h.connLocks[conn]; existing != nil {
		return existing
	}
	created := &sync.Mutex{}
	h.connLocks[conn] = created
	return created
}

func (h *StreamHub) collectReplayStep(clientID, sessionKey string) replayStep {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nextReplayStepLocked(clientID, sessionKey)
}

func (h *StreamHub) nextReplayStepLocked(clientID, sessionKey string) replayStep {
	clientKey := pendingClientKey(clientID, sessionKey)
	replay := h.replayStates[clientKey]
	if replay == nil {
		return replayStep{live: true}
	}
	state := h.pendingSessions[sessionKey]
	if state == nil {
		replay.Status = ClientStreamStatusLive
		return replayStep{live: true}
	}
	events := make([]StreamEvent, 0, len(state.ReplyingList))
	maxEventSeq := replay.LastEventSeq
	for _, event := range state.ReplyingList {
		turnID, baseExchangeSeq, eventSeq, ok := parseEventCursor(event.EventCursor)
		if !ok || turnID != state.TurnID || baseExchangeSeq != state.BaseExchangeSeq || eventSeq <= replay.LastEventSeq {
			continue
		}
		events = append(events, cloneEvent(event))
		if eventSeq > maxEventSeq {
			maxEventSeq = eventSeq
		}
	}
	if len(events) == 0 {
		replay.Status = ClientStreamStatusLive
		return replayStep{live: true}
	}
	sort.SliceStable(events, func(i, j int) bool {
		_, _, left, _ := parseEventCursor(events[i].EventCursor)
		_, _, right, _ := parseEventCursor(events[j].EventCursor)
		return left < right
	})
	replay.LastEventSeq = maxEventSeq
	return replayStep{events: events}
}

func (h *StreamHub) replayStepToClient(rootID, clientID, sessionKey string, events []StreamEvent) {
	for i := range events {
		h.SendToClient(clientID, buildSessionStreamResponse(rootID, sessionKey, &events[i]))
	}
}

func (h *StreamHub) replayQueueToClient(rootID, clientID, sessionKey string) {
	stateRoot, queue, frozen := h.queueSnapshot(sessionKey)
	if rootID == "" {
		rootID = stateRoot
	}
	if rootID == "" || len(queue) == 0 {
		return
	}
	h.SendToClient(clientID, buildSessionQueueUpdatedResponse(rootID, sessionKey, queue, frozen))
}

func (h *StreamHub) replayCompletionToClient(rootID, clientID, sessionKey string) {
	if blank(rootID) || blank(clientID) || blank(sessionKey) {
		return
	}
	h.mu.Lock()
	completed := h.completed[sessionKey]
	if completed == nil {
		h.mu.Unlock()
		return
	}
	requestID := completed.RequestID
	h.mu.Unlock()
	h.SendToClient(clientID, buildSessionDoneResponse(rootID, sessionKey, requestID, true))
}

func (h *StreamHub) isReplayClientLocked(clientID, sessionKey string) bool {
	for _, replayKey := range h.getReplayKeyListLocked(sessionKey, clientID) {
		state := h.replayStates[replayKey]
		return state != nil && state.Status != ClientStreamStatusLive
	}
	return false
}

func (h *StreamHub) getReplayKeyListLocked(sessionKey, clientID string) []string {
	if len(h.replayStates) == 0 {
		return nil
	}
	keys := make([]string, 0, len(h.replayStates))
	for replayKey := range h.replayStates {
		if sessionKey != "" && !strings.HasSuffix(replayKey, "::"+sessionKey) {
			continue
		}
		if clientID != "" && !strings.HasPrefix(replayKey, clientID+"::") {
			continue
		}
		keys = append(keys, replayKey)
	}
	return keys
}
