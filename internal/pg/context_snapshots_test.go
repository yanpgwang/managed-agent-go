package pg

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/yanpgwang/mango/internal/app"
	"github.com/yanpgwang/mango/internal/domain"
)

func TestThreadContextSnapshotIsImmutableAndChainsWithinThread(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_context_snapshot")
	session.AgentSnapshot = domain.Agent{
		ID: session.AgentID, Version: session.AgentVersion, Name: "coordinator",
		Model: domain.NormalizeModel(domain.Model{ID: "claude-test"}),
		Multiagent: &domain.Multiagent{Type: "coordinator", Agents: []domain.AgentReference{{
			Type: "agent", ID: "agent_peer", Version: 2,
		}},
		},
	}
	session.MultiagentRoster = []domain.Agent{{
		ID: "agent_peer", Version: 2, Name: "reviewer",
		Model: domain.NormalizeModel(domain.Model{ID: "claude-test"}),
	}}
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatal(err)
	}
	threads, err := store.ListSessionThreads(
		ctx, session.ID, app.SessionThreadListQuery{Limit: 1},
	)
	if err != nil || len(threads) != 1 {
		t.Fatalf("primary Thread = %+v, err=%v", threads, err)
	}
	child, _, err := store.CreateChildSessionThread(
		ctx, session.ID, threads[0].ID, "reviewer",
	)
	if err != nil {
		t.Fatal(err)
	}
	appendTrigger := func(text string) domain.Event {
		events, err := store.AppendThreadEvents(
			ctx,
			session.ID,
			child.ID,
			[]domain.EventDraft{{
				Type: domain.EvAgentThreadMessageReceived,
				Payload: map[string]any{
					"from_session_thread_id": threads[0].ID,
					"from_agent_name":        nil,
					"content": []any{
						map[string]any{"type": "text", "text": text},
					},
				},
			}},
		)
		if err != nil || len(events) != 1 {
			t.Fatalf("append child trigger = %+v, err=%v", events, err)
		}
		return events[0]
	}

	firstTrigger := appendTrigger("first")
	firstMessages := []domain.Message{{
		Role: domain.RoleUser,
		Content: []domain.ContentBlock{{
			Type: "text", Text: "Earlier session context was compacted.\nfirst",
		}},
	}}
	firstProjection := domain.ContextProjection{
		Compacted: true, OriginalEstimatedTokens: 20_000,
		ProjectedEstimatedTokens: 4_000, DroppedMessages: 12,
	}
	first, err := store.PutThreadContextSnapshot(
		ctx,
		session.ID,
		child.ID,
		firstTrigger.ID,
		[]string{"sevt_prior", firstTrigger.ID},
		firstMessages,
		firstProjection,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.ID, domain.PrefixContextSnapshot) ||
		first.ParentSnapshotID != nil ||
		first.ContextPolicyVersion != domain.ContextPolicyVersion {
		t.Fatalf("first snapshot = %+v", first)
	}
	loaded, found, err := store.GetThreadContextSnapshotForTrigger(
		ctx, session.ID, child.ID, firstTrigger.ID,
	)
	if err != nil || !found || !reflect.DeepEqual(loaded, first) {
		t.Fatalf("load first snapshot = %+v, found=%v, err=%v", loaded, found, err)
	}

	// The same trigger may be retried by Temporal on a newer worker. Its first
	// committed projection remains authoritative rather than being overwritten.
	retried, err := store.PutThreadContextSnapshot(
		ctx,
		session.ID,
		child.ID,
		firstTrigger.ID,
		[]string{"sevt_changed"},
		[]domain.Message{{
			Role:    domain.RoleUser,
			Content: []domain.ContentBlock{{Type: "text", Text: "changed"}},
		}},
		domain.ContextProjection{
			Compacted: true, OriginalEstimatedTokens: 30_000,
			ProjectedEstimatedTokens: 3_000, DroppedMessages: 20,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if retried.ID != first.ID ||
		!reflect.DeepEqual(retried.Messages, first.Messages) ||
		!reflect.DeepEqual(retried.Projection, first.Projection) ||
		!reflect.DeepEqual(
			retried.TranscriptTriggerEventIDs,
			first.TranscriptTriggerEventIDs,
		) {
		t.Fatalf("retry replaced immutable snapshot: first=%+v retry=%+v", first, retried)
	}

	secondTrigger := appendTrigger("second")
	second, err := store.PutThreadContextSnapshot(
		ctx,
		session.ID,
		child.ID,
		secondTrigger.ID,
		[]string{firstTrigger.ID, secondTrigger.ID},
		firstMessages,
		firstProjection,
	)
	if err != nil {
		t.Fatal(err)
	}
	if second.ParentSnapshotID == nil || *second.ParentSnapshotID != first.ID {
		t.Fatalf("second snapshot parent = %v, want %s", second.ParentSnapshotID, first.ID)
	}
}

func TestPrimaryThreadContextSnapshotUsesPrimaryLedger(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_primary_context_snapshot")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatal(err)
	}
	threads, err := store.ListSessionThreads(
		ctx, session.ID, app.SessionThreadListQuery{Limit: 1},
	)
	if err != nil || len(threads) != 1 {
		t.Fatalf("primary Thread = %+v, err=%v", threads, err)
	}
	primary := threads[0]
	admission, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "compact this turn"},
		}},
	}})
	if err != nil || len(admission.SubmittedEvents) != 1 {
		t.Fatalf("primary trigger = %+v, err=%v", admission.SubmittedEvents, err)
	}
	trigger := admission.SubmittedEvents[0]
	if trigger.ThreadID != primary.ID {
		t.Fatalf("trigger Thread = %q, want %q", trigger.ThreadID, primary.ID)
	}

	projection := domain.ContextProjection{
		Compacted: true, OriginalEstimatedTokens: 20_000,
		ProjectedEstimatedTokens: 4_000, DroppedMessages: 12,
	}
	messages := []domain.Message{{
		Role: domain.RoleUser,
		Content: []domain.ContentBlock{{
			Type: "text", Text: "Earlier session context was compacted.",
		}},
	}}
	snapshot, err := store.PutThreadContextSnapshot(
		ctx,
		session.ID,
		primary.ID,
		trigger.ID,
		[]string{trigger.ID},
		messages,
		projection,
	)
	if err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.GetThreadContextSnapshotForTrigger(
		ctx, session.ID, primary.ID, trigger.ID,
	)
	if err != nil || !found || !reflect.DeepEqual(snapshot, loaded) {
		t.Fatalf("primary snapshot = %+v, found=%v, err=%v", loaded, found, err)
	}
}
