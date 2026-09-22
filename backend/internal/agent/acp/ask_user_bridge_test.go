package acp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	types "curvature/backend/internal/agent/types"
)

// newQuestionTestSession builds a session that can host the ask-user bridge
// without a live agent process: the card is emitted to the test.
func newQuestionTestSession() (*session, *eventRecorder) {
	recorder := &eventRecorder{events: make(chan types.Event, 8)}
	sess := &session{
		proc:       &Process{sessions: map[string]*sessionState{}},
		sessionKey: "question-test",
		emit:       recorder.record,
	}
	return sess, recorder
}

type eventRecorder struct {
	mu     sync.Mutex
	events chan types.Event
}

func (r *eventRecorder) record(event types.Event) {
	select {
	case r.events <- event:
	default:
	}
}

func (r *eventRecorder) next(t *testing.T) types.Event {
	t.Helper()
	select {
	case event := <-r.events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
		return types.Event{}
	}
}

func bridgeQuestions() []types.MCPAskUserQuestion {
	return []types.MCPAskUserQuestion{{
		ID:       "color",
		Question: "Which color?",
		Header:   "Color",
		Options: []types.AskUserQuestionOption{
			{Label: "Red"},
			{Label: "Blue"},
		},
	}}
}

func TestAskUserQuestionEmitsACardAndReturnsTheAnswer(t *testing.T) {
	sess, recorder := newQuestionTestSession()

	type outcome struct {
		answer types.MCPAskUserAnswer
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		answer, err := sess.AskUserQuestion(context.Background(), types.MCPAskUserRequest{
			CallID:    "3",
			Questions: bridgeQuestions(),
		})
		done <- outcome{answer: answer, err: err}
	}()

	cardEvent := recorder.next(t)
	if cardEvent.Type != types.EventTypeToolCall {
		t.Fatalf("event type = %q, want %q", cardEvent.Type, types.EventTypeToolCall)
	}
	card, ok := cardEvent.Data.(types.ToolCall)
	if !ok {
		t.Fatalf("event data = %#v", cardEvent.Data)
	}
	if card.Kind != types.ToolKindAskUser || card.Status != "running" {
		t.Fatalf("card = %+v", card)
	}
	callID, _ := card.Meta["toolUseId"].(string)
	if !strings.HasPrefix(callID, "mcp-ask-user:") {
		t.Fatalf("call id = %q", callID)
	}
	questions, _ := card.Meta["questions"].([]types.AskUserQuestionItem)
	if len(questions) != 1 || questions[0].Question != "Which color?" {
		t.Fatalf("card questions = %+v", questions)
	}

	// The human clicks Blue.
	if err := sess.AnswerQuestion(context.Background(), types.AskUserAnswer{
		ToolUseID: callID,
		Answers:   map[string]string{"q_0": "Blue"},
	}); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("AskUserQuestion: %v", result.err)
		}
		if len(result.answer.Answers) != 1 {
			t.Fatalf("answer = %+v", result.answer)
		}
		if result.answer.Answers[0].ID != "color" || result.answer.Answers[0].Selected[0] != "Blue" {
			t.Fatalf("answer item = %+v, want color=[Blue]", result.answer.Answers[0])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AskUserQuestion did not return")
	}

	updateEvent := recorder.next(t)
	if updateEvent.Type != types.EventTypeToolUpdate {
		t.Fatalf("event type = %q, want %q", updateEvent.Type, types.EventTypeToolUpdate)
	}
	updated, _ := updateEvent.Data.(types.ToolCall)
	if updated.Status != "complete" {
		t.Fatalf("completed card status = %q", updated.Status)
	}
	if updated.Meta["answered"] != true {
		t.Fatalf("answered flag = %v", updated.Meta["answered"])
	}
	answers, _ := updated.Meta["answers"].(map[string]string)
	if answers["q_0"] != "Blue" {
		t.Fatalf("recorded answers = %+v", updated.Meta["answers"])
	}
}

func TestAskUserQuestionTimesOutAndClosesTheCard(t *testing.T) {
	sess, recorder := newQuestionTestSession()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := sess.AskUserQuestion(ctx, types.MCPAskUserRequest{CallID: "9", Questions: bridgeQuestions()})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	card := recorder.next(t)
	if card.Type != types.EventTypeToolCall {
		t.Fatalf("first event = %q", card.Type)
	}
	update := recorder.next(t)
	toolCall, _ := update.Data.(types.ToolCall)
	if toolCall.Status != "complete" {
		t.Fatalf("timed-out card must stop looking answerable, status = %q", toolCall.Status)
	}
	if toolCall.Meta["answered"] != false {
		t.Fatalf("answered flag = %v", toolCall.Meta["answered"])
	}
	if _, ok := toolCall.Meta["error"]; !ok {
		t.Fatalf("timed-out card should explain itself: %+v", toolCall.Meta)
	}
}

func TestAnswerQuestionRejectsUnknownAndRepeatedAnswers(t *testing.T) {
	sess, _ := newQuestionTestSession()
	if err := sess.AnswerQuestion(context.Background(), types.AskUserAnswer{ToolUseID: "nope", Answers: map[string]string{"q_0": "Red"}}); err == nil {
		t.Fatal("AnswerQuestion accepted an unknown tool use id")
	}
	if err := sess.AnswerQuestion(context.Background(), types.AskUserAnswer{ToolUseID: "x"}); err == nil {
		t.Fatal("AnswerQuestion accepted an empty answer map")
	}
	if err := sess.AnswerQuestion(context.Background(), types.AskUserAnswer{}); err == nil {
		t.Fatal("AnswerQuestion accepted a missing tool use id")
	}

	done := make(chan error, 1)
	go func() {
		_, err := sess.AskUserQuestion(context.Background(), types.MCPAskUserRequest{CallID: "1", Questions: bridgeQuestions()})
		done <- err
	}()
	// Wait until the question is registered, then answer twice.
	var callID string
	deadline := time.Now().Add(2 * time.Second)
	for callID == "" && time.Now().Before(deadline) {
		sess.questionMu.Lock()
		for id := range sess.questionWait {
			callID = id
		}
		sess.questionMu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	if callID == "" {
		t.Fatal("the question was never registered")
	}
	if err := sess.AnswerQuestion(context.Background(), types.AskUserAnswer{ToolUseID: callID, Answers: map[string]string{"q_0": "Red"}}); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	if err := sess.AnswerQuestion(context.Background(), types.AskUserAnswer{ToolUseID: callID, Answers: map[string]string{"q_0": "Blue"}}); err == nil {
		t.Fatal("AnswerQuestion accepted a second answer for the same question")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("AskUserQuestion: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AskUserQuestion did not return")
	}
}

func TestAskUserQuestionRejectsAnEmptyQuestionSet(t *testing.T) {
	sess, _ := newQuestionTestSession()
	if _, err := sess.AskUserQuestion(context.Background(), types.MCPAskUserRequest{}); err == nil {
		t.Fatal("AskUserQuestion accepted an empty question set")
	}
}

func TestAskUserQuestionWithoutQuestionsKeepsCardsUnique(t *testing.T) {
	sess, recorder := newQuestionTestSession()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = sess.AskUserQuestion(context.Background(), types.MCPAskUserRequest{Questions: bridgeQuestions()})
		done <- struct{}{}
	}()
	first := recorder.next(t)
	firstCard, _ := first.Data.(types.ToolCall)

	go func() {
		_, _ = sess.AskUserQuestion(context.Background(), types.MCPAskUserRequest{Questions: bridgeQuestions()})
		done <- struct{}{}
	}()
	second := recorder.next(t)
	secondCard, _ := second.Data.(types.ToolCall)

	if firstCard.CallID == secondCard.CallID {
		t.Fatalf("two questions shared the call id %q", firstCard.CallID)
	}
	sess.questionMu.Lock()
	pending := len(sess.questionWait)
	sess.questionMu.Unlock()
	if pending != 2 {
		t.Fatalf("pending questions = %d, want 2", pending)
	}
	// Release both waiters so the goroutines do not leak into other tests.
	for id := range map[string]struct{}{firstCard.CallID: {}, secondCard.CallID: {}} {
		_ = sess.AnswerQuestion(context.Background(), types.AskUserAnswer{ToolUseID: id, Answers: map[string]string{"q_0": "Red"}})
	}
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("AskUserQuestion did not return")
		}
	}
}
