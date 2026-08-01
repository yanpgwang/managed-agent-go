package pg

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestCompleteWorkflowTurn_CommitsLosslessTranscriptAtomically(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sess_transcript")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatal(err)
	}
	admission, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "search"},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	triggerID := admission.Events[0].ID
	opaque := json.RawMessage(
		`{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[{"type":"web_search_result","encrypted_content":"opaque"}]}`,
	)
	delta := []domain.Message{
		{
			Role: domain.RoleUser,
			Content: []domain.ContentBlock{{
				Type: "text", Text: "search",
			}},
		},
		{
			Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{
				Type: "web_search_tool_result", Raw: opaque,
			}},
		},
	}
	mappings := []domain.ProviderToolUseMapping{{
		PublicEventID:     "sevt_public",
		ProviderToolUseID: "toolu_provider",
		ToolName:          "read",
	}}
	_, err = store.CompleteWorkflowTurnWithTranscript(
		ctx,
		session.ID,
		triggerID,
		[]domain.EventDraft{{
			Type: domain.EvSessionStatusIdle,
			Payload: map[string]any{
				"stop_reason": map[string]any{"type": "end_turn"},
			},
		}},
		domain.StatusIdle,
		"",
		"",
		nil,
		nil,
		nil,
		delta,
		mappings,
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.LoadProviderTranscript(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TriggerEventIDs) != 1 || got.TriggerEventIDs[0] != triggerID {
		t.Fatalf("trigger ids = %#v", got.TriggerEventIDs)
	}
	if len(got.Messages) != 2 || len(got.Messages[1].Content) != 1 ||
		!equivalentJSON(got.Messages[1].Content[0].Raw, opaque) {
		t.Fatalf("transcript = %#v", got.Messages)
	}
	if len(got.ToolUseMappings) != 1 ||
		got.ToolUseMappings[0] != mappings[0] {
		t.Fatalf("mappings = %#v", got.ToolUseMappings)
	}
}

// PostgreSQL jsonb intentionally normalizes insignificant whitespace and key
// order. Provider-native blocks are lossless at the JSON value level, not at
// the original byte-serialization level.
func equivalentJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func TestCloseInterruptedProviderTranscript_PairsDanglingTools(t *testing.T) {
	messages := []domain.Message{
		{
			Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{
				{Type: "tool_use", ToolUseID: "provider_done"},
				{Type: "tool_use", ToolUseID: "provider_pending"},
				{Type: "server_tool_use", ToolUseID: "server_native"},
			},
		},
		{
			Role: domain.RoleUser,
			Content: []domain.ContentBlock{{
				Type: "tool_result", ToolResultFor: "provider_done",
				Text: "done",
			}},
		},
	}

	got := closeInterruptedProviderTranscript(messages)

	if len(got) != 2 || got[1].Role != domain.RoleUser {
		t.Fatalf("closed transcript = %#v", got)
	}
	if len(got[1].Content) != 2 {
		t.Fatalf("result blocks = %#v", got[1].Content)
	}
	synthetic := got[1].Content[1]
	if synthetic.ToolResultFor != "provider_pending" ||
		!synthetic.IsError {
		t.Fatalf("synthetic result = %#v", synthetic)
	}
	if len(messages[1].Content) != 1 {
		t.Fatal("helper mutated its input transcript")
	}
	if got := closeInterruptedProviderTranscript(nil); got != nil {
		t.Fatalf("nil transcript became represented: %#v", got)
	}
}

func TestRetainCommittedProviderMappings_DropsInterruptedActions(t *testing.T) {
	mappings := []domain.ProviderToolUseMapping{
		{
			PublicEventID:     "public_done",
			ProviderToolUseID: "provider_done",
		},
		{
			PublicEventID:     "public_pending",
			ProviderToolUseID: "provider_pending",
		},
	}
	got := retainCommittedProviderMappings(
		mappings,
		[]domain.EventDraft{
			{ID: "public_done", Type: domain.EvAgentToolUse},
			{Type: domain.EvAgentToolResult},
		},
	)
	if len(got) != 1 || got[0] != mappings[0] {
		t.Fatalf("retained mappings = %#v", got)
	}
}
