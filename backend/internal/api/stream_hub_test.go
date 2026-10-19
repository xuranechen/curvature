package api

import (
	"testing"
	"time"
)

// turnEvents simulates a running turn by appending n message_chunk events and
// returns the event cursors in order.
func turnEvents(hub *StreamHub, sessionKey string, n int) []string {
	cursors := make([]string, 0, n)
	for i := 0; i < n; i++ {
		event := hub.AppendReplyEvent(sessionKey, StreamEvent{Type: "message_chunk"})
		cursors = append(cursors, event.EventCursor)
	}
	return cursors
}

// replayFor simulates a client that reconnects and sends session.ready with the
// given cursor, returning the events the hub decides to replay. It mirrors
// ReplayPending using the same production helper for the resume decision.
func replayFor(hub *StreamHub, clientID, sessionKey, cursor string) []StreamEvent {
	hub.mu.Lock()
	lastEventSeq := resumeEventSeq(hub.pendingSessions[sessionKey], cursor)
	hub.replayStates[pendingClientKey(clientID, sessionKey)] = &ClientReplayState{
		Status:       ClientStreamStatusReplay,
		LastEventSeq: lastEventSeq,
	}
	hub.mu.Unlock()

	var out []StreamEvent
	for {
		step := hub.collectReplayStep(clientID, sessionKey)
		out = append(out, step.events...)
		if step.live {
			return out
		}
	}
}

func startTurn(hub *StreamHub, sessionKey string, baseExchangeSeq int) {
	base := []int{}
	if baseExchangeSeq > 0 {
		base = append(base, baseExchangeSeq)
	}
	hub.SetPendingUserAt("root", sessionKey, "title", "codex", "gpt", "", "", "", false, "hi", time.Now().UTC(), base...)
}

// A reconnecting client that already saw part of the current turn must receive
// exactly the suffix it missed.
func TestReplayPendingResumesFromCursorInSameTurn(t *testing.T) {
	hub := NewStreamHub(nil)
	startTurn(hub, "sess", 8)
	cursors := turnEvents(hub, "sess", 5)

	replayed := replayFor(hub, "client-1", "sess", cursors[1]) // saw events 1 and 2
	if len(replayed) != 3 {
		t.Fatalf("replayed %d events, want 3: %#v", len(replayed), replayed)
	}
	for i, event := range replayed {
		want := cursors[i+2]
		if event.EventCursor != want {
			t.Fatalf("replayed[%d] cursor = %q, want %q", i, event.EventCursor, want)
		}
	}
}

// A client with no cursor receives the whole buffered turn.
func TestReplayPendingWithoutCursorReplaysWholeTurn(t *testing.T) {
	hub := NewStreamHub(nil)
	startTurn(hub, "sess", 8)
	turnEvents(hub, "sess", 4)

	replayed := replayFor(hub, "client-1", "sess", "")
	if len(replayed) != 4 {
		t.Fatalf("replayed %d events, want 4", len(replayed))
	}
}

// A cursor issued in an earlier turn must never make the hub believe the client
// is caught up in the current one.
func TestReplayPendingReplaysWholeTurnWhenCursorFromAnotherTurn(t *testing.T) {
	hub := NewStreamHub(nil)
	startTurn(hub, "sess", 3)
	stale := turnEvents(hub, "sess", 6)[4] // cursor from turn A

	// Turn A ends and turn B starts with a different base exchange seq.
	hub.ClearSessionPending("sess")
	startTurn(hub, "sess", 5)
	turnEvents(hub, "sess", 4)

	replayed := replayFor(hub, "client-1", "sess", stale)
	if len(replayed) != 4 {
		t.Fatalf("replayed %d events, want the whole new turn (4)", len(replayed))
	}
}

// The regression behind the relay-reconnect incident: when two consecutive turns
// share the same BaseExchangeSeq (the previous turn's exchanges were not
// persisted yet, e.g. a queued message or a resend after reconnect), an event
// cursor from the first turn is indistinguishable from a cursor of the second.
// The hub then reports the reconnecting client as caught up, replays nothing,
// and the user sees a streaming turn fall back to "sent, waiting".
func TestReplayPendingReplaysWholeTurnWhenBaseExchangeSeqRepeats(t *testing.T) {
	hub := NewStreamHub(nil)
	startTurn(hub, "sess", 0)
	stale := turnEvents(hub, "sess", 6)[4] // "0:5" issued during turn A

	// Turn A ends; turn B starts before turn A's exchanges were persisted, so it
	// computes the same base exchange seq (0).
	hub.ClearSessionPending("sess")
	startTurn(hub, "sess", 0)
	turnEvents(hub, "sess", 8)

	replayed := replayFor(hub, "client-1", "sess", stale)
	if len(replayed) != 8 {
		t.Fatalf("replayed %d events, want the whole new turn (8): a stale cursor from the previous turn suppressed the replay", len(replayed))
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

// A replay client that never reaches live must not keep the session marked as
// replying, otherwise every later message is queued behind a turn that has
// already finished.
func TestClearSessionPendingDoesNotBlockOnStaleReplayClient(t *testing.T) {
	hub := NewStreamHub(nil)
	startTurn(hub, "sess", 0)
	turnEvents(hub, "sess", 3)
	hub.mu.Lock()
	hub.replayStates[pendingClientKey("stale-client", "sess")] = &ClientReplayState{
		Status: ClientStreamStatusReplay,
	}
	hub.mu.Unlock()

	done := make(chan struct{})
	go func() {
		hub.ClearSessionPending("sess")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ClearSessionPending blocked on a stale replay client")
	}

	if hub.IsSessionReplying("sess") {
		t.Fatal("session still reports replying after the turn finished")
	}
	hub.mu.RLock()
	state := hub.pendingSessions["sess"]
	hub.mu.RUnlock()
	if state == nil || len(state.ReplyingList) != 3 {
		t.Fatalf("replay buffer was not preserved for the reconnecting client: %#v", state)
	}

	hub.mu.Lock()
	delete(hub.replayStates, pendingClientKey("stale-client", "sess"))
	hub.mu.Unlock()
	waitForCondition(t, time.Second, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		_, ok := hub.pendingSessions["sess"]
		return !ok
	})
}

// A completed turn with a queued message stays available for the queue but is
// no longer considered replying, so the queued message can start immediately.
func TestClearSessionPendingReleasesReplyStateWithQueuedMessage(t *testing.T) {
	hub := NewStreamHub(nil)
	startTurn(hub, "sess", 0)
	turnEvents(hub, "sess", 2)
	hub.EnqueueSessionMessage("root", "sess", "title", QueuedUserMessage{
		ID: "queued-1",
		PendingUserMessage: PendingUserMessage{
			Agent:   "codex",
			Content: "next",
		},
	})

	hub.ClearSessionPending("sess")

	if hub.IsSessionReplying("sess") {
		t.Fatal("session still reports replying with a queued message")
	}
	hub.mu.RLock()
	state := hub.pendingSessions["sess"]
	hub.mu.RUnlock()
	if state == nil || state.Active || len(state.Queue) != 1 {
		t.Fatalf("queued state = %#v, want inactive with one queued message", state)
	}
	if len(state.ReplyingList) != 0 {
		t.Fatalf("replay buffer kept without a replay client: %d events", len(state.ReplyingList))
	}
}

// An unknown session has nothing to replay and must go straight to live.
func TestReplayPendingWithoutPendingStateGoesLive(t *testing.T) {
	hub := NewStreamHub(nil)
	replayed := replayFor(hub, "client-1", "missing", "")
	if len(replayed) != 0 {
		t.Fatalf("replayed %d events for an unknown session, want 0", len(replayed))
	}
	hub.mu.RLock()
	status := hub.replayStates[pendingClientKey("client-1", "missing")].Status
	hub.mu.RUnlock()
	if status != ClientStreamStatusLive {
		t.Fatalf("status = %q, want live", status)
	}
}

// Event cursors must identify their turn, so that a cursor can never be
// attributed to a different turn of the same session.
func TestEventCursorIdentifiesTurn(t *testing.T) {
	hub := NewStreamHub(nil)
	startTurn(hub, "sess", 0)
	first := turnEvents(hub, "sess", 1)[0]
	hub.ClearSessionPending("sess")
	startTurn(hub, "sess", 0)
	second := turnEvents(hub, "sess", 5)

	seen := map[string]bool{first: true}
	for _, cursor := range second {
		if seen[cursor] {
			t.Fatalf("cursor %q was reused across turns", cursor)
		}
		seen[cursor] = true
	}
}

// The captured cursor from a completed turn must not resume a later turn.
func TestReplayPendingCompletedTurnCursorDoesNotResumeNextTurn(t *testing.T) {
	hub := NewStreamHub(nil)
	startTurn(hub, "sess", 0)
	cursors := turnEvents(hub, "sess", 20)
	last := cursors[len(cursors)-1]

	hub.ClearSessionPending("sess")
	startTurn(hub, "sess", 0)
	want := turnEvents(hub, "sess", 30)

	replayed := replayFor(hub, "client-1", "sess", last)
	if len(replayed) != len(want) {
		t.Fatalf("replayed %d events, want %d", len(replayed), len(want))
	}
	for i, event := range replayed {
		if event.EventCursor != want[i] {
			t.Fatalf("replayed[%d] = %q, want %q", i, event.EventCursor, want[i])
		}
	}
}
