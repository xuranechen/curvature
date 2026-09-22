package mcpaskuser

import (
	"strings"
	"testing"

	agenttypes "curvature/backend/internal/agent/types"
)

func testQuestions() []agenttypes.MCPAskUserQuestion {
	return []agenttypes.MCPAskUserQuestion{
		{
			ID:       "color",
			Question: "Which color?",
			Header:   "Color",
			Options: []agenttypes.AskUserQuestionOption{
				{Label: "Red"},
				{Label: "Blue"},
			},
		},
		{
			ID:          "extras",
			Question:    "Which extras?",
			MultiSelect: true,
			Options: []agenttypes.AskUserQuestionOption{
				{Label: "Milk"},
				{Label: "Sugar"},
			},
		},
	}
}

func TestAnswersMapsPositionalKeysOntoQuestionIDs(t *testing.T) {
	// The card submits answers keyed by question position; the tool call needs
	// them keyed by the question id the model supplied.
	answer, err := Answers(testQuestions(), map[string]string{"q_0": "Blue", "q_1": "Milk"})
	if err != nil {
		t.Fatalf("Answers: %v", err)
	}
	if len(answer.Answers) != 2 {
		t.Fatalf("answers = %d, want 2: %+v", len(answer.Answers), answer.Answers)
	}
	if answer.Answers[0].ID != "color" || len(answer.Answers[0].Selected) != 1 || answer.Answers[0].Selected[0] != "Blue" {
		t.Fatalf("first answer = %+v, want color=[Blue]", answer.Answers[0])
	}
	if answer.Answers[1].ID != "extras" || answer.Answers[1].Selected[0] != "Milk" {
		t.Fatalf("second answer = %+v, want extras=[Milk]", answer.Answers[1])
	}
}

func TestAnswersAcceptsQuestionIDKeys(t *testing.T) {
	answer, err := Answers(testQuestions(), map[string]string{"color": "Red", "extras": "Sugar"})
	if err != nil {
		t.Fatalf("Answers: %v", err)
	}
	if answer.Answers[0].Selected[0] != "Red" || answer.Answers[1].Selected[0] != "Sugar" {
		t.Fatalf("answers = %+v", answer.Answers)
	}
}

func TestAnswersSplitsMultiSelect(t *testing.T) {
	answer, err := Answers(testQuestions(), map[string]string{"q_0": "Red", "q_1": "Milk, Sugar"})
	if err != nil {
		t.Fatalf("Answers: %v", err)
	}
	selected := answer.Answers[1].Selected
	if len(selected) != 2 || selected[0] != "Milk" || selected[1] != "Sugar" {
		t.Fatalf("multi-select answer = %+v, want [Milk Sugar]", selected)
	}
}

func TestAnswersReturnsFreeFormTextAsCustom(t *testing.T) {
	questions := []agenttypes.MCPAskUserQuestion{{ID: "name", Question: "What should I call you?"}}
	answer, err := Answers(questions, map[string]string{"q_0": "  Ada  "})
	if err != nil {
		t.Fatalf("Answers: %v", err)
	}
	if answer.Answers[0].Custom != "Ada" {
		t.Fatalf("custom = %q, want the trimmed free-form answer", answer.Answers[0].Custom)
	}
	if len(answer.Answers[0].Selected) != 0 {
		t.Fatalf("selected = %+v, want empty for a free-form answer", answer.Answers[0].Selected)
	}
}

func TestAnswersRequiresEveryQuestion(t *testing.T) {
	if _, err := Answers(testQuestions(), map[string]string{"q_0": "Red"}); err == nil {
		t.Fatal("Answers accepted a partial answer set")
	}
	if _, err := Answers(nil, map[string]string{"q_0": "Red"}); err == nil {
		t.Fatal("Answers accepted an empty question set")
	}
}

func TestAnswerMetaRecordsPositionalAnswers(t *testing.T) {
	meta := AnswerMeta(testQuestions(), map[string]string{"q_1": "Sugar"})
	if meta["q_1"] != "Sugar" {
		t.Fatalf("meta = %+v, want q_1=Sugar", meta)
	}
	if _, ok := meta["q_0"]; ok {
		t.Fatalf("meta recorded an unanswered question: %+v", meta)
	}
}

func TestCardLooksLikeANativeAskUserCard(t *testing.T) {
	card := Card("mcp-ask-user:7", testQuestions())
	if card.Kind != agenttypes.ToolKindAskUser {
		t.Fatalf("kind = %q, want %q", card.Kind, agenttypes.ToolKindAskUser)
	}
	if card.Status != "running" {
		t.Fatalf("status = %q, want running", card.Status)
	}
	if got := card.Meta["toolUseId"]; got != "mcp-ask-user:7" {
		t.Fatalf("toolUseId = %v", got)
	}
	questions, ok := card.Meta["questions"].([]agenttypes.AskUserQuestionItem)
	if !ok || len(questions) != 2 {
		t.Fatalf("questions meta = %#v", card.Meta["questions"])
	}
	if questions[0].Question != "Which color?" || len(questions[0].Options) != 2 {
		t.Fatalf("first question = %+v", questions[0])
	}
	if !questions[1].MultiSelect {
		t.Fatalf("multi select flag lost: %+v", questions[1])
	}
	if card.Meta["transport"] != "mcp" {
		t.Fatalf("transport marker = %v", card.Meta["transport"])
	}
}

func TestCardTitleFallsBackWhenHeaderMissing(t *testing.T) {
	card := Card("id", []agenttypes.MCPAskUserQuestion{{ID: "q", Question: "Which?"}})
	if strings.TrimSpace(card.Title) == "" {
		t.Fatal("card has no title")
	}
}
