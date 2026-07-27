package httpapi

import (
	_ "embed"
	"net/http"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

//go:embed openapi.yaml
var openapiDoc string

const timeFmt = time.RFC3339Nano

type Deps struct {
	Agents   *app.AgentService
	Envs     *app.EnvironmentService
	Sessions *app.SessionService
	Events   *app.EventService
	Hub      *app.Hub
}

type Server struct {
	deps Deps
	cfg  Config
	mux  *http.ServeMux
}

func NewServer(deps Deps, cfg Config) *Server {
	s := &Server{deps: deps, cfg: cfg, mux: http.NewServeMux()}
	s.routes()
	return s
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

// NewServerAdapter builds a Handler from already-constructed services.
// Intended for tests that need to reopen the store to simulate a restart.
// Uses a lenient Config (no beta header or auth token required).
func NewServerAdapter(agents *app.AgentService, envs *app.EnvironmentService,
	sessions *app.SessionService, events *app.EventService, hub *app.Hub) http.Handler {
	return NewServer(Deps{Agents: agents, Envs: envs, Sessions: sessions, Events: events, Hub: hub},
		Config{}).Handler()
}
