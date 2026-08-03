package httpapi

import (
	"context"
	_ "embed"
	"net/http"
	"sync"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

//go:embed openapi.yaml
var openapiDoc string

const timeFmt = time.RFC3339Nano

type AgentService interface {
	Create(context.Context, domain.Agent) (domain.Agent, error)
	Get(context.Context, string) (domain.Agent, error)
	List(context.Context) ([]domain.Agent, error)
	Versions(context.Context, string) ([]domain.Agent, error)
	Update(context.Context, string, domain.AgentPatch) (domain.Agent, error)
	Archive(context.Context, string) (domain.Agent, error)
}

type EnvironmentService interface {
	Create(context.Context, domain.Environment) (domain.Environment, error)
	Get(context.Context, string) (domain.Environment, error)
	List(context.Context) ([]domain.Environment, error)
	Archive(context.Context, string) (domain.Environment, error)
	Delete(context.Context, string) error
}

type SessionService interface {
	Create(context.Context, app.CreateSessionInput) (domain.Session, error)
	Get(context.Context, string) (domain.Session, error)
	List(context.Context, app.ListPage) (app.SessionListPage, error)
	SendEvent(context.Context, string, []domain.EventDraft) ([]domain.Event, error)
	UpdateTitle(context.Context, string, string) (domain.Session, error)
	Archive(context.Context, string) (domain.Session, error)
	Delete(context.Context, string) error
}

type EventService interface {
	Query(context.Context, string, app.EventQuery) ([]domain.Event, error)
}

type EventSubscriber interface {
	SubscribeContext(context.Context, string, map[string]bool) (<-chan app.Frame, func(), error)
}

type Deps struct {
	Agents   AgentService
	Envs     EnvironmentService
	Sessions SessionService
	Events   EventService
	Stream   EventSubscriber
}

type Server struct {
	deps Deps
	cfg  Config
	mux  *http.ServeMux

	// shutdown is closed once by BeginShutdown. Long-lived streaming handlers
	// select on it so a draining process ends them cleanly instead of having
	// their connections severed when the drain deadline expires.
	shutdown     chan struct{}
	shutdownOnce sync.Once
}

func NewServer(deps Deps, cfg Config) *Server {
	s := &Server{deps: deps, cfg: cfg, mux: http.NewServeMux(), shutdown: make(chan struct{})}
	s.routes()
	return s
}

// BeginShutdown signals in-flight streaming handlers that the process is
// draining. It is idempotent and safe to call concurrently.
//
// Callers invoke it immediately before http.Server.Shutdown. Without it, an
// open SSE stream keeps its connection non-idle for the whole drain window and
// is then killed at the deadline; with it, the stream handler returns, the
// response ends at a frame boundary, and Shutdown completes promptly.
//
// It deliberately does not cancel per-request contexts: ordinary requests keep
// the full drain window to finish.
func (s *Server) BeginShutdown() {
	s.shutdownOnce.Do(func() { close(s.shutdown) })
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	s.mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte(openapiDoc))
	})

	s.mux.HandleFunc("POST /v1/agents", s.createAgent)
	s.mux.HandleFunc("GET /v1/agents", s.listAgents)
	s.mux.HandleFunc("GET /v1/agents/{id}", s.getAgent)
	s.mux.HandleFunc("POST /v1/agents/{id}", s.updateAgent)
	s.mux.HandleFunc("GET /v1/agents/{id}/versions", s.listAgentVersions)
	s.mux.HandleFunc("POST /v1/agents/{id}/archive", s.archiveAgent)

	s.mux.HandleFunc("POST /v1/environments", s.createEnvironment)
	s.mux.HandleFunc("GET /v1/environments", s.listEnvironments)
	s.mux.HandleFunc("GET /v1/environments/{id}", s.getEnvironment)
	s.mux.HandleFunc("POST /v1/environments/{id}/archive", s.archiveEnvironment)
	s.mux.HandleFunc("DELETE /v1/environments/{id}", s.deleteEnvironment)

	s.registerSessionRoutes()
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, domain.NotFound("resource not found"))
	})
}

func (s *Server) Handler() http.Handler {
	return requestIDMiddleware(bodyLimitMiddleware(authMiddleware(s.cfg,
		versionMiddleware(s.cfg, contentTypeMiddleware(s.cfg, betaMiddleware(s.cfg, s.mux))))))
}
