package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestOfficialGoSDKSessionThreadSurface(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)
	service := &sdkThreadService{thread: domain.SessionThread{
		ID: "sthr_primary", SessionID: "sesn_thread_sdk",
		Agent: domain.Agent{
			ID: "agent_thread_sdk", Version: 3, Name: "Coordinator",
			Model: domain.NormalizeModel(domain.Model{ID: "claude-opus-4-8"}),
			Tools: []any{}, MCPServers: []any{}, Skills: []domain.SkillReference{},
		},
		Status: domain.StatusIdle, CreatedAt: now, UpdatedAt: now,
	}}
	service.next = service.thread
	service.next.ID = "sthr_child_fixture"
	service.next.ParentThreadID = &service.thread.ID
	service.next.CreatedAt = now.Add(time.Second)
	service.next.UpdatedAt = service.next.CreatedAt
	event := domain.Event{
		ID: "sevt_thread_sdk", SessionID: service.thread.SessionID, Sequence: 1,
		Type:      domain.EvUserMessage,
		Payload:   map[string]any{"content": []any{map[string]any{"type": "text", "text": "hello"}}},
		CreatedAt: now, ProcessedAt: &now,
	}
	nextEvent := event
	nextEvent.ID = "sevt_thread_sdk_next"
	nextEvent.Sequence = 2
	nextEvent.CreatedAt = now.Add(time.Second)
	nextEvent.ProcessedAt = &nextEvent.CreatedAt
	server := httptest.NewServer(NewServer(Deps{
		Sessions: &testSessionService{sessions: map[string]domain.Session{
			service.thread.SessionID: {ID: service.thread.SessionID},
		}},
		Threads: service,
		Events:  &sdkThreadEvents{event: event, next: nextEvent},
		Stream:  &sdkThreadStream{event: event},
	}, Config{RequireBeta: true, RequireAuth: true, RequireVersion: true}).Handler())
	t.Cleanup(server.Close)
	client := anthropic.NewClient(
		option.WithBaseURL(server.URL+"/"), option.WithAPIKey("test-key"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	thread, err := client.Beta.Sessions.Threads.Get(ctx, service.thread.ID,
		anthropic.BetaSessionThreadGetParams{SessionID: service.thread.SessionID})
	if err != nil || thread.ID != service.thread.ID || thread.ParentThreadID != "" ||
		thread.Agent.ID != service.thread.Agent.ID || thread.Agent.Version != 3 ||
		thread.Status != anthropic.BetaManagedAgentsSessionThreadStatusIdle ||
		thread.Type != anthropic.BetaManagedAgentsSessionThreadTypeSessionThread {
		t.Fatalf("Get Session Thread = %+v, err=%v", thread, err)
	}
	assertRawObjectHasFields(t, thread.RawJSON(),
		"id", "agent", "archived_at", "created_at", "parent_thread_id",
		"session_id", "stats", "status", "type", "updated_at", "usage",
	)
	assertRawObjectHasFields(t, thread.Usage.RawJSON(),
		"active_seconds", "cache_creation", "cache_read_input_tokens", "input_tokens",
		"list_cost", "output_tokens", "server_tool_use")
	if strings.Contains(thread.Agent.RawJSON(), `"multiagent"`) {
		t.Fatal("thread agent repeated the coordinator multiagent roster")
	}

	page, err := client.Beta.Sessions.Threads.List(ctx, service.thread.SessionID,
		anthropic.BetaSessionThreadListParams{Limit: param.NewOpt(int64(1))})
	if err != nil || len(page.Data) != 1 || page.Data[0].ID != service.thread.ID {
		t.Fatalf("List Session Threads = %+v, err=%v", page, err)
	}
	nextPage, err := page.GetNextPage()
	if err != nil || len(nextPage.Data) != 1 || nextPage.Data[0].ID != service.next.ID {
		t.Fatalf("List next Session Threads page = %+v, err=%v", nextPage, err)
	}
	if _, err := client.Beta.Sessions.Threads.Events.List(ctx, service.next.ID,
		anthropic.BetaSessionThreadEventListParams{SessionID: service.thread.SessionID},
	); err == nil {
		t.Fatal("child Thread read leaked the primary event ledger")
	} else {
		assertAPIStatus(t, err, http.StatusUnprocessableEntity)
	}

	events, err := client.Beta.Sessions.Threads.Events.List(ctx, service.thread.ID,
		anthropic.BetaSessionThreadEventListParams{
			SessionID: service.thread.SessionID, Limit: param.NewOpt(int64(1)),
		})
	if err != nil || len(events.Data) != 1 || events.Data[0].Type != domain.EvUserMessage {
		t.Fatalf("List Session Thread Events = %+v, err=%v", events, err)
	}
	nextEvents, err := events.GetNextPage()
	if err != nil || len(nextEvents.Data) != 1 || nextEvents.Data[0].ID != nextEvent.ID {
		t.Fatalf("List next Session Thread Events page = %+v, err=%v", nextEvents, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/v1/sessions/"+service.thread.SessionID+"/events?order=asc&page="+
			url.QueryEscape(events.NextPage), nil)
	if err != nil {
		t.Fatalf("build cross-resource cursor request: %v", err)
	}
	request.Header.Set("anthropic-beta", betaValue)
	request.Header.Set("anthropic-version", anthropicVersion)
	request.Header.Set("x-api-key", "test-key")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("cross-resource cursor request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("Thread cursor on Session Event list status = %d, want 400", response.StatusCode)
	}

	stream := client.Beta.Sessions.Threads.Events.StreamEvents(ctx, service.thread.ID,
		anthropic.BetaSessionThreadEventStreamParams{SessionID: service.thread.SessionID})
	defer func() { _ = stream.Close() }()
	if !stream.Next() {
		t.Fatalf("Stream Session Thread Events yielded no event: %v", stream.Err())
	}
	if got := stream.Current(); got.ID != event.ID || got.Type != domain.EvUserMessage {
		t.Fatalf("streamed event = %+v", got)
	}

	archived, err := client.Beta.Sessions.Threads.Archive(ctx, service.thread.ID,
		anthropic.BetaSessionThreadArchiveParams{SessionID: service.thread.SessionID})
	if err != nil || archived.ArchivedAt.IsZero() ||
		archived.Status != anthropic.BetaManagedAgentsSessionThreadStatusTerminated {
		t.Fatalf("Archive Session Thread = %+v, err=%v", archived, err)
	}
}

type sdkThreadService struct {
	thread domain.SessionThread
	next   domain.SessionThread
}

func (s *sdkThreadService) Get(
	_ context.Context, sessionID, threadID string,
) (domain.SessionThread, error) {
	if sessionID != s.thread.SessionID {
		return domain.SessionThread{}, domain.NotFound("session thread not found")
	}
	switch threadID {
	case s.thread.ID:
		return s.thread, nil
	case s.next.ID:
		return s.next, nil
	default:
		return domain.SessionThread{}, domain.NotFound("session thread not found")
	}
}

func (s *sdkThreadService) List(
	_ context.Context, sessionID string, query app.SessionThreadListQuery,
) ([]domain.SessionThread, error) {
	if sessionID != s.thread.SessionID {
		return nil, domain.NotFound("session not found")
	}
	if query.Boundary != nil {
		return []domain.SessionThread{s.next}, nil
	}
	return []domain.SessionThread{s.thread, s.next}, nil
}

func (s *sdkThreadService) Archive(
	_ context.Context, sessionID, threadID string,
) (domain.SessionThread, error) {
	if _, err := s.Get(context.Background(), sessionID, threadID); err != nil {
		return domain.SessionThread{}, err
	}
	now := s.thread.UpdatedAt.Add(time.Second)
	s.thread.ArchivedAt = &now
	s.thread.TerminatedAt = &now
	s.thread.UpdatedAt = now
	s.thread.Status = domain.StatusTerminated
	return s.thread, nil
}

type sdkThreadEvents struct {
	event domain.Event
	next  domain.Event
}

func (s *sdkThreadEvents) Query(
	_ context.Context, sessionID string, query app.EventQuery,
) ([]domain.Event, error) {
	if sessionID != s.event.SessionID {
		return nil, domain.NotFound("session not found")
	}
	if query.Boundary != nil {
		return []domain.Event{s.next}, nil
	}
	return []domain.Event{s.event, s.next}, nil
}

type sdkThreadStream struct{ event domain.Event }

func (s *sdkThreadStream) SubscribeContext(
	_ context.Context, sessionID string, _ map[string]bool,
) (<-chan app.Frame, func(), error) {
	if sessionID != s.event.SessionID {
		return nil, nil, domain.NotFound("session not found")
	}
	frames := make(chan app.Frame, 1)
	frames <- app.Frame{Event: &s.event}
	close(frames)
	return frames, func() {}, nil
}
