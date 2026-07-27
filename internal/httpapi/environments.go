package httpapi

import (
	"net/http"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func envToJSON(e domain.Environment) map[string]any {
	out := map[string]any{
		"id": e.ID, "type": "environment", "name": e.Name,
		"config":     map[string]any{"type": e.ConfigType},
		"created_at": e.CreatedAt.Format(timeFmt), "updated_at": e.UpdatedAt.Format(timeFmt),
	}
	if len(e.Config) > 0 {
		out["config"] = e.Config
	}
	if e.ArchivedAt != nil {
		out["archived_at"] = e.ArchivedAt.Format(timeFmt)
	} else {
		out["archived_at"] = nil
	}
	return out
}

func (s *Server) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string         `json:"name"`
		Config map[string]any `json:"config"`
	}
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	var ct string
	if in.Config != nil {
		if t, ok := in.Config["type"].(string); ok {
			ct = t
		}
	}
	e, err := s.deps.Envs.Create(r.Context(), domain.Environment{Name: in.Name, ConfigType: ct, Config: in.Config})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, envToJSON(e))
}

func (s *Server) getEnvironment(w http.ResponseWriter, r *http.Request) {
	e, err := s.deps.Envs.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, envToJSON(e))
}

func (s *Server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	es, err := s.deps.Envs.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]any, 0, len(es))
	for _, e := range es {
		out = append(out, envToJSON(e))
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) archiveEnvironment(w http.ResponseWriter, r *http.Request) {
	e, err := s.deps.Envs.Archive(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, envToJSON(e))
}

func (s *Server) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Envs.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": r.PathValue("id"), "deleted": true})
}
