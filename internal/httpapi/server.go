package httpapi

import (
	"context"
	_ "embed"
	"log/slog"
	"net/http"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/health"
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

	// Readiness probes the process's required dependencies for GET /readyz.
	// Nil means "no dependencies to probe", which keeps in-memory embedders and
	// the HTTP test suite unconditionally ready.
	Readiness health.Prober
	// Logger receives operational records (access log, readiness failures). Nil
	// selects slog.Default().
	Logger *slog.Logger
}

type Server struct {
	deps   Deps
	cfg    Config
	mux    *http.ServeMux
	logger *slog.Logger
}

func NewServer(deps Deps, cfg Config) *Server {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{deps: deps, cfg: cfg, mux: http.NewServeMux(), logger: logger}
	s.routes()
	return s
}

func (s *Server) routes() {
	// Health endpoints are a local operational choice: the Claude Managed
	// Agents API documents no health, readiness, or status endpoint, so they
	// live outside /v1 and outside the OpenAPI document. /healthz is liveness
	// only and never probes a dependency; /readyz fails closed when PostgreSQL,
	// Temporal, or NATS is unreachable.
	s.mux.Handle("GET /healthz", health.LiveHandler())
	s.mux.Handle("GET /readyz", health.ReadyHandler(s.deps.Readiness, s.logger))
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
	return requestIDMiddleware(logMiddleware(s.logger, bodyLimitMiddleware(authMiddleware(s.cfg,
		versionMiddleware(s.cfg, contentTypeMiddleware(s.cfg, betaMiddleware(s.cfg, s.mux)))))))
}
