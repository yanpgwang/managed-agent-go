package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/httpapi"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
	temporalpkg "github.com/yanpgwang/managed-agent-go/internal/temporal"
)

const testDatabaseURLEnv = "MANAGED_AGENT_TEST_DATABASE_URL"

var testSchemaSequence atomic.Int64

type postgresFixture struct {
	store           *pg.Store
	agentRepo       *pg.AgentRepository
	environmentRepo *pg.EnvironmentRepository
	ids             domain.IDGenerator
	clock           domain.Clock
}

func newPostgresFixture(t *testing.T) postgresFixture {
	t.Helper()
	databaseURL := os.Getenv(testDatabaseURLEnv)
	if databaseURL == "" {
		t.Skipf("%s not set; skipping PostgreSQL HTTP integration test", testDatabaseURLEnv)
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database url: %v", err)
	}
	fixtureSequence := testSchemaSequence.Add(1)
	fixtureSuffix := domain.NewRandomIDGen().NewID("")
	schema := "controlplane_" + safeName(t.Name()) + "_" +
		intString(fixtureSequence) + "_" + fixtureSuffix
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		pool.Close()
		t.Fatalf("create schema: %v", err)
	}
	if err := pg.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		pool.Close()
	})

	ids := &testIDs{suffix: fixtureSuffix}
	clock := &testClock{}
	store := pg.NewStore(pool, ids, clock)
	agentRepo := pg.NewAgentRepository(store)
	environmentRepo := pg.NewEnvironmentRepository(store)
	return postgresFixture{
		store: store, agentRepo: agentRepo, environmentRepo: environmentRepo,
		ids: ids, clock: clock,
	}
}

func postgresHandler(t *testing.T) http.Handler {
	t.Helper()
	fixture := newPostgresFixture(t)
	ids := fixture.ids
	clock := fixture.clock
	store := fixture.store
	agentRepo := fixture.agentRepo
	environmentRepo := fixture.environmentRepo
	orchestrator := temporalpkg.NewOrchestrator(store, nil)
	sessions := NewSessionService(
		store, agentRepo, environmentRepo, orchestrator, ids, clock,
	)
	server := httpapi.NewServer(httpapi.Deps{
		Agents:   app.NewAgentService(agentRepo, ids, clock),
		Envs:     app.NewEnvironmentService(environmentRepo, ids, clock),
		Sessions: sessions,
		Events:   NewEventService(store),
		Stream:   app.NewHub(64),
	}, httpapi.Config{})
	return server.Handler()
}

func TestPostgresHTTPResourceSessionAndEventPath(t *testing.T) {
	handler := postgresHandler(t)
	agentID := createResource(t, handler, "/v1/agents",
		`{"name":"coder","model":"claude-test"}`)
	environmentID := createResource(t, handler, "/v1/environments",
		`{"name":"cloud","config":{"type":"cloud"}}`)
	sessionID := createResource(t, handler, "/v1/sessions",
		`{"agent":"`+agentID+`","environment_id":"`+environmentID+`"}`)

	response := request(t, handler, http.MethodPost, "/v1/sessions/"+sessionID+"/events",
		`{"events":[{"type":"user.message","content":[{"type":"text","text":"hello"}]}]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("send event -> %d: %s", response.Code, response.Body.String())
	}
	response = request(t, handler, http.MethodGet, "/v1/sessions/"+sessionID+"/events", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list events -> %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(envelope.Data) != 2 ||
		envelope.Data[0]["type"] != domain.EvUserMessage ||
		envelope.Data[1]["type"] != domain.EvSessionStatusRunning {
		t.Fatalf("event order = %#v", envelope.Data)
	}

	unsupported := request(
		t,
		handler,
		http.MethodPost,
		"/v1/sessions/"+sessionID+"/events",
		`{"events":[{"type":"user.interrupt"}]}`,
	)
	if unsupported.Code != http.StatusUnprocessableEntity {
		t.Fatalf("interrupt status = %d, want 422: %s", unsupported.Code, unsupported.Body.String())
	}
}

func TestDeleteFencesAdmissionBeforeWorkflowTermination(t *testing.T) {
	fixture := newPostgresFixture(t)
	ctx := context.Background()
	session := domain.Session{
		ID: "sesn_delete_running", Status: domain.StatusIdle,
		Metadata: map[string]any{}, CreatedAt: fixture.clock.Now(),
		UpdatedAt: fixture.clock.Now(),
	}
	if _, err := fixture.store.CreateSession(ctx, session, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "running"}},
		},
	}}); err != nil {
		t.Fatalf("create running session: %v", err)
	}
	orchestrator := &recordingOrchestrator{}
	service := NewSessionService(
		fixture.store, fixture.agentRepo, fixture.environmentRepo,
		orchestrator, fixture.ids, fixture.clock,
	)
	if err := service.Delete(ctx, session.ID); err == nil {
		t.Fatal("delete running session succeeded")
	}
	if orchestrator.terminationCalls != 0 {
		t.Fatalf("workflow terminated before running conflict: calls=%d", orchestrator.terminationCalls)
	}
}

func TestDeleteTerminationFailureKeepsFenceAndRetryCompletes(t *testing.T) {
	fixture := newPostgresFixture(t)
	ctx := context.Background()
	session := domain.Session{
		ID: "sesn_delete_retry", Status: domain.StatusIdle,
		Metadata: map[string]any{}, CreatedAt: fixture.clock.Now(),
		UpdatedAt: fixture.clock.Now(),
	}
	if _, err := fixture.store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	orchestrator := &recordingOrchestrator{terminateErr: errors.New("temporal unavailable")}
	service := NewSessionService(
		fixture.store, fixture.agentRepo, fixture.environmentRepo,
		orchestrator, fixture.ids, fixture.clock,
	)
	if err := service.Delete(ctx, session.ID); err == nil {
		t.Fatal("delete succeeded despite termination failure")
	}
	if _, err := fixture.store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserDefineOutcome,
		Payload: map[string]any{"description": "must remain fenced"},
	}}); err == nil {
		t.Fatal("admission reopened after ambiguous termination result")
	}
	orchestrator.terminateErr = nil
	if err := service.Delete(ctx, session.ID); err != nil {
		t.Fatalf("retry delete: %v", err)
	}
	if _, err := fixture.store.GetSession(ctx, session.ID); err == nil {
		t.Fatal("session still exists after successful delete retry")
	}
}

type recordingOrchestrator struct {
	terminationCalls int
	terminateErr     error
}

func (o *recordingOrchestrator) CreateAPISession(
	context.Context,
	domain.Session,
	[]domain.EventDraft,
) (domain.Session, []domain.Event, error) {
	panic("not used")
}

func (o *recordingOrchestrator) Admit(
	context.Context,
	string,
	[]domain.EventDraft,
) ([]domain.Event, error) {
	panic("not used")
}

func (o *recordingOrchestrator) TerminateSession(context.Context, string) error {
	o.terminationCalls++
	return o.terminateErr
}

func createResource(t *testing.T, handler http.Handler, path, body string) string {
	t.Helper()
	response := request(t, handler, http.MethodPost, path, body)
	if response.Code != http.StatusOK {
		t.Fatalf("POST %s -> %d: %s", path, response.Code, response.Body.String())
	}
	var object map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &object); err != nil {
		t.Fatalf("decode POST %s: %v", path, err)
	}
	id, _ := object["id"].(string)
	if id == "" {
		t.Fatalf("POST %s returned no id: %v", path, object)
	}
	return id
}

func request(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

type testIDs struct {
	mu     sync.Mutex
	n      int
	suffix string
}

func (g *testIDs) NewID(prefix string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return prefix + intString(int64(g.n)) + "_" + g.suffix
}

type testClock struct {
	n atomic.Int64
}

func (c *testClock) Now() time.Time {
	return time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC).
		Add(time.Duration(c.n.Add(1)) * time.Millisecond)
}

func safeName(value string) string {
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9':
			builder.WriteRune(character)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func intString(value int64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}
