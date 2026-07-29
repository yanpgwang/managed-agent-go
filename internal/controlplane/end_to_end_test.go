package controlplane

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/httpapi"
	"github.com/yanpgwang/managed-agent-go/internal/live"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	temporalpkg "github.com/yanpgwang/managed-agent-go/internal/temporal"
	enumspb "go.temporal.io/api/enums/v1"
)

func TestHTTPPostgresTemporalNATSEndToEnd(t *testing.T) {
	temporalAddress := os.Getenv("MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT")
	natsURL := os.Getenv("MANAGED_AGENT_TEST_NATS_URL")
	if os.Getenv(testDatabaseURLEnv) == "" || temporalAddress == "" || natsURL == "" {
		t.Skip("PostgreSQL/Temporal/NATS integration environment is not configured")
	}
	fixture := newPostgresFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	broker, err := live.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	fixture.store.SetEventNotifier(broker)
	temporalClient, err := temporalpkg.Dial(temporalpkg.ClientConfig{
		HostPort: temporalAddress,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer temporalClient.Close()
	runtime := temporalpkg.NewRuntimeOnTaskQueue(
		temporalClient,
		fixture.store,
		model.NewFake(),
		sandbox.NewLocalProvider(),
		fixture.ids,
		temporalpkg.RelayConfig{PollInterval: 20 * time.Millisecond},
		"managed-agent-test-"+domain.NewRandomIDGen().NewID(""),
		broker,
	)
	if err := runtime.Worker.Start(); err != nil {
		t.Fatal(err)
	}
	defer runtime.Worker.Stop()
	relayErrors := make(chan error, 1)
	go func() { relayErrors <- runtime.Relay.Run(ctx) }()

	sessions := NewSessionService(
		fixture.store,
		fixture.agentRepo,
		fixture.environmentRepo,
		runtime.Orchestrator(),
		fixture.ids,
		fixture.clock,
	)
	handler := httpapi.NewServer(httpapi.Deps{
		Agents: app.NewAgentService(
			fixture.agentRepo,
			fixture.ids,
			fixture.clock,
		),
		Envs: app.NewEnvironmentService(
			fixture.environmentRepo,
			fixture.ids,
			fixture.clock,
		),
		Sessions: sessions,
		Events:   NewEventService(fixture.store),
		Stream: live.NewStream(
			fixture.store,
			broker,
			fixture.ids,
			fixture.clock,
			50*time.Millisecond,
		),
	}, httpapi.Config{}).Handler()

	agentID := createResource(t, handler, "/v1/agents",
		`{"name":"coder","model":"claude-test"}`)
	environmentID := createResource(t, handler, "/v1/environments",
		`{"name":"cloud","config":{"type":"cloud"}}`)
	sessionID := createResource(t, handler, "/v1/sessions",
		`{"agent":"`+agentID+`","environment_id":"`+environmentID+`"}`)
	defer func() {
		_ = temporalClient.TerminateWorkflow(
			context.Background(),
			sessionID,
			"",
			"integration test cleanup",
		)
	}()

	server := httptest.NewServer(handler)
	defer server.Close()
	streamURL := server.URL + "/v1/sessions/" + sessionID + "/events/stream?" +
		url.Values{"event_deltas[]": {domain.EvAgentMessage}}.Encode()
	streamRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	streamResponse, err := server.Client().Do(streamRequest)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer streamResponse.Body.Close()
	if streamResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(streamResponse.Body)
		t.Fatalf("stream status = %d: %s", streamResponse.StatusCode, body)
	}

	lines := make(chan string, 64)
	go func() {
		scanner := bufio.NewScanner(streamResponse.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	response := request(
		t,
		handler,
		http.MethodPost,
		"/v1/sessions/"+sessionID+"/events",
		`{"events":[{"type":"user.message","content":[{"type":"text","text":"hello"}]}]}`,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("send user.message -> %d: %s", response.Code, response.Body.String())
	}

	wanted := map[string]bool{
		"event: " + domain.PreviewEventStart:   false,
		"event: " + domain.PreviewEventDelta:   false,
		"event: " + domain.EvAgentMessage:      false,
		"event: " + domain.EvSessionStatusIdle: false,
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for !allSeen(wanted) {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatalf("stream closed early; seen=%v", wanted)
			}
			if _, tracked := wanted[line]; tracked {
				wanted[line] = true
			}
		case err := <-relayErrors:
			t.Fatalf("relay stopped: %v", err)
		case <-deadline.C:
			t.Fatalf("timed out waiting for complete stream; seen=%v", wanted)
		}
	}

	session, err := fixture.store.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != domain.StatusIdle {
		t.Fatalf("session status = %s, want idle", session.Status)
	}
	events, err := fixture.store.QueryEvents(ctx, sessionID, app.EventQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !containsEventTypes(events,
		domain.EvUserMessage,
		domain.EvSessionStatusRunning,
		domain.EvAgentMessage,
		domain.EvSessionStatusIdle,
	) {
		t.Fatalf("event types = %v", eventTypes(events))
	}

	response = request(t, handler, http.MethodDelete, "/v1/sessions/"+sessionID, "")
	if response.Code != http.StatusOK {
		t.Fatalf("delete session -> %d: %s", response.Code, response.Body.String())
	}
	if _, err := fixture.store.GetSession(ctx, sessionID); err == nil {
		t.Fatal("session projection still exists after delete")
	}
	description, err := temporalClient.DescribeWorkflowExecution(ctx, sessionID, "")
	if err != nil {
		t.Fatalf("describe deleted workflow: %v", err)
	}
	if got := description.WorkflowExecutionInfo.Status; got != enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED {
		t.Fatalf("workflow status = %s, want TERMINATED", got)
	}
}

func allSeen(values map[string]bool) bool {
	for _, seen := range values {
		if !seen {
			return false
		}
	}
	return true
}

func containsEventTypes(events []domain.Event, types ...string) bool {
	found := make(map[string]bool, len(events))
	for _, event := range events {
		found[event.Type] = true
	}
	for _, eventType := range types {
		if !found[eventType] {
			return false
		}
	}
	return true
}

func eventTypes(events []domain.Event) string {
	types := make([]string, len(events))
	for i, event := range events {
		types[i] = event.Type
	}
	return strings.Join(types, ",")
}
