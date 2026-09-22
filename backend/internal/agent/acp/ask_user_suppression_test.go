package acp

import (
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestBridgedAskUserToolUpdatesAreSuppressed(t *testing.T) {
	bridged := "mcp__curvature-ask-user__ask_user_question"
	cases := []struct {
		name   string
		update SessionUpdate
		want   bool
	}{
		{
			name: "the bridged tool call",
			update: SessionUpdate{
				Type: UpdateTypeToolCall,
				Raw:  acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{Title: bridged}},
			},
			want: true,
		},
		{
			name: "the bridged tool status update",
			update: SessionUpdate{
				Type: UpdateTypeToolUpdate,
				Raw: acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
					Title: stringPtr(bridged),
				}},
			},
			want: true,
		},
		{
			name: "the bridged tool named only in the raw input",
			update: SessionUpdate{
				Type: UpdateTypeToolCall,
				Raw: acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
					RawInput: map[string]any{"name": bridged},
				}},
			},
			want: true,
		},
		{
			name: "an ordinary tool call",
			update: SessionUpdate{
				Type: UpdateTypeToolCall,
				Raw:  acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{Title: "Read file"}},
			},
		},
		{
			name: "another tool from the same server is still shown",
			update: SessionUpdate{
				Type: UpdateTypeToolCall,
				Raw: acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
					Title: "mcp__curvature-ask-user__some_other_tool",
				}},
			},
		},
		{
			name: "another server's ask_user_question is still shown",
			update: SessionUpdate{
				Type: UpdateTypeToolCall,
				Raw: acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
					Title: "mcp__some-other-server__ask_user_question",
				}},
			},
		},
		{
			name: "message chunks are never touched",
			update: SessionUpdate{
				Type: UpdateTypeMessageChunk,
				Raw:  acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{Title: bridged}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBridgedAskUserToolUpdate(tc.update); got != tc.want {
				t.Fatalf("isBridgedAskUserToolUpdate = %v, want %v", got, tc.want)
			}
		})
	}
}

func stringPtr(value string) *string { return &value }
