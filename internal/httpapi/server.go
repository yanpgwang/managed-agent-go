package httpapi

import (
	"context"
	_ "embed"
	"net/http"
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
	List(context.Context, app.AgentListQuery) (app.AgentListPage, error)
	Versions(context.Context, string, app.AgentVersionListQuery) (app.AgentVersionListPage, error)
	Update(context.Context, string, domain.AgentPatch) (domain.Agent, error)
	Archive(context.Context, string) (domain.Agent, error)
}

type EnvironmentService interface {
	Create(context.Context, domain.Environment) (domain.Environment, error)
	Update(context.Context, string, domain.EnvironmentPatch) (domain.Environment, error)
	Get(context.Context, string) (domain.Environment, error)
	List(context.Context, app.EnvironmentListQuery) (app.EnvironmentListPage, error)
	Archive(context.Context, string) (domain.Environment, error)
	Delete(context.Context, string) error
}

type SessionService interface {
	Create(context.Context, app.CreateSessionInput) (domain.Session, error)
	Get(context.Context, string) (domain.Session, error)
	List(context.Context, app.ListPage) (app.SessionListPage, error)
	SendEvent(context.Context, string, []domain.EventDraft) ([]domain.Event, error)
	Update(context.Context, string, domain.SessionUpdate) (domain.Session, error)
	Archive(context.Context, string) (domain.Session, error)
	Delete(context.Context, string) error
}

type EventService interface {
	Query(context.Context, string, app.EventQuery) ([]domain.Event, error)
}

type EventSubscriber interface {
	SubscribeContext(context.Context, string, map[string]bool) (<-chan app.Frame, func(), error)
}

type FileService interface {
	Upload(context.Context, app.FileUploadInput) (domain.File, error)
	Get(context.Context, string) (domain.File, error)
	List(context.Context, app.FileListQuery) (app.FileListPage, error)
	Download(context.Context, string) (app.FileDownload, error)
	Delete(context.Context, string) (domain.File, error)
}

type SkillService interface {
	Create(context.Context, app.SkillCreateInput) (domain.Skill, error)
	Get(context.Context, string) (domain.Skill, error)
	List(context.Context, app.SkillListQuery) (app.SkillListPage, error)
	Delete(context.Context, string) (domain.Skill, error)
	CreateVersion(context.Context, string, []app.SkillUploadFile) (domain.SkillVersion, error)
	GetVersion(context.Context, string, string) (domain.SkillVersion, error)
	ListVersions(context.Context, string, app.SkillVersionListQuery) (app.SkillVersionListPage, error)
	DeleteVersion(context.Context, string, string) (domain.SkillVersion, error)
	Download(context.Context, string, string) (app.SkillVersionDownload, error)
}

type MemoryService interface {
	CreateStore(context.Context, app.MemoryStoreCreateInput) (domain.MemoryStore, error)
	GetStore(context.Context, string) (domain.MemoryStore, error)
	UpdateStore(context.Context, string, app.MemoryStoreUpdateInput) (domain.MemoryStore, error)
	ListStores(context.Context, app.MemoryStoreListQuery) (app.MemoryStoreListPage, error)
	ArchiveStore(context.Context, string) (domain.MemoryStore, error)
	DeleteStore(context.Context, string) error
	CreateMemory(context.Context, string, app.MemoryCreateInput) (domain.Memory, error)
	GetMemory(context.Context, string, string) (domain.Memory, error)
	ListMemories(context.Context, string, app.MemoryListQuery) (app.MemoryListPage, error)
	UpdateMemory(context.Context, string, string, app.MemoryUpdateInput) (domain.Memory, error)
	DeleteMemory(context.Context, string, string, *string, domain.MemoryActor) (domain.Memory, error)
	GetMemoryVersion(context.Context, string, string) (domain.MemoryVersion, error)
	ListMemoryVersions(context.Context, string, app.MemoryVersionListQuery) (app.MemoryVersionListPage, error)
	RedactMemoryVersion(context.Context, string, string, domain.MemoryActor) (domain.MemoryVersion, error)
}

type SessionResourceService interface {
	Add(context.Context, string, app.FileSessionResourceInput) (domain.SessionResource, error)
	Get(context.Context, string, string) (domain.SessionResource, error)
	List(context.Context, string, app.SessionResourceListQuery) (app.SessionResourceListPage, error)
	Update(context.Context, string, string, string) (domain.SessionResource, error)
	Delete(context.Context, string, string) (domain.SessionResource, error)
}

type Deps struct {
	Agents           AgentService
	Envs             EnvironmentService
	Sessions         SessionService
	Events           EventService
	Stream           EventSubscriber
	Files            FileService
	Skills           SkillService
	Memory           MemoryService
	SessionResources SessionResourceService
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
	s.mux.HandleFunc("POST /v1/environments/{id}", s.updateEnvironment)
	s.mux.HandleFunc("POST /v1/environments/{id}/archive", s.archiveEnvironment)
	s.mux.HandleFunc("DELETE /v1/environments/{id}", s.deleteEnvironment)

	s.mux.HandleFunc("POST /v1/files", s.uploadFile)
	s.mux.HandleFunc("GET /v1/files", s.listFiles)
	s.mux.HandleFunc("GET /v1/files/{id}", s.getFile)
	s.mux.HandleFunc("GET /v1/files/{id}/content", s.downloadFile)
	s.mux.HandleFunc("DELETE /v1/files/{id}", s.deleteFile)

	s.mux.HandleFunc("POST /v1/skills", s.createSkill)
	s.mux.HandleFunc("GET /v1/skills", s.listSkills)
	s.mux.HandleFunc("GET /v1/skills/{id}", s.getSkill)
	s.mux.HandleFunc("DELETE /v1/skills/{id}", s.deleteSkill)
	s.mux.HandleFunc("POST /v1/skills/{id}/versions", s.createSkillVersion)
	s.mux.HandleFunc("GET /v1/skills/{id}/versions", s.listSkillVersions)
	s.mux.HandleFunc("GET /v1/skills/{id}/versions/{version}", s.getSkillVersion)
	s.mux.HandleFunc("DELETE /v1/skills/{id}/versions/{version}", s.deleteSkillVersion)
	s.mux.HandleFunc("GET /v1/skills/{id}/versions/{version}/content", s.downloadSkillVersion)

	s.mux.HandleFunc("POST /v1/memory_stores", s.createMemoryStore)
	s.mux.HandleFunc("GET /v1/memory_stores", s.listMemoryStores)
	s.mux.HandleFunc("GET /v1/memory_stores/{store_id}", s.getMemoryStore)
	s.mux.HandleFunc("POST /v1/memory_stores/{store_id}", s.updateMemoryStore)
	s.mux.HandleFunc("DELETE /v1/memory_stores/{store_id}", s.deleteMemoryStore)
	s.mux.HandleFunc("POST /v1/memory_stores/{store_id}/archive", s.archiveMemoryStore)
	s.mux.HandleFunc("POST /v1/memory_stores/{store_id}/memories", s.createMemory)
	s.mux.HandleFunc("GET /v1/memory_stores/{store_id}/memories", s.listMemories)
	s.mux.HandleFunc("GET /v1/memory_stores/{store_id}/memories/{memory_id}", s.getMemory)
	s.mux.HandleFunc("POST /v1/memory_stores/{store_id}/memories/{memory_id}", s.updateMemory)
	s.mux.HandleFunc("DELETE /v1/memory_stores/{store_id}/memories/{memory_id}", s.deleteMemory)
	s.mux.HandleFunc("GET /v1/memory_stores/{store_id}/memory_versions", s.listMemoryVersions)
	s.mux.HandleFunc("GET /v1/memory_stores/{store_id}/memory_versions/{version_id}", s.getMemoryVersion)
	s.mux.HandleFunc("POST /v1/memory_stores/{store_id}/memory_versions/{version_id}/redact", s.redactMemoryVersion)

	s.registerSessionRoutes()
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, domain.NotFound("resource not found"))
	})
}

func (s *Server) Handler() http.Handler {
	return requestIDMiddleware(bodyLimitMiddleware(authMiddleware(s.cfg,
		versionMiddleware(s.cfg, contentTypeMiddleware(s.cfg, betaMiddleware(s.cfg, s.mux))))))
}
