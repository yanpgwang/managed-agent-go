package httpapi

import (
	"net/http"

	"github.com/yanpgwang/managed-agent-go/internal/app"
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
	query, filter, err := parseEnvironmentListParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := s.deps.Envs.List(r.Context(), query)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]any, 0, len(page.Environments))
	for _, e := range page.Environments {
		out = append(out, envToJSON(e))
	}
	var nextPage any
	if page.HasNext && len(page.Environments) > 0 {
		last := page.Environments[len(page.Environments)-1]
		nextPage = encodeResourceCursor(resourceCursor{
			Kind:      environmentListCursorKind,
			CreatedAt: last.CreatedAt.UTC().Format(timeFmt),
			ID:        last.ID,
			Filter:    filter.fingerprint(),
		})
	}
	writeJSON(w, 200, map[string]any{"data": out, "next_page": nextPage})
}

func parseEnvironmentListParams(
	r *http.Request,
) (app.EnvironmentListQuery, environmentCursorFilter, error) {
	values := r.URL.Query()
	query := app.EnvironmentListQuery{Limit: app.DefaultEnvironmentListLimit}
	filter := environmentCursorFilter{}

	if values.Has("limit") {
		limit, err := parseResourceListLimit(values.Get("limit"))
		if err != nil {
			return app.EnvironmentListQuery{}, environmentCursorFilter{}, err
		}
		if limit > app.MaxEnvironmentListLimit {
			return app.EnvironmentListQuery{}, environmentCursorFilter{},
				domain.Validation("limit must not exceed 1000")
		}
		query.Limit = limit
	}
	if values.Has("include_archived") {
		include, err := parseResourceListBool(values.Get("include_archived"), "include_archived")
		if err != nil {
			return app.EnvironmentListQuery{}, environmentCursorFilter{}, err
		}
		query.IncludeArchived = include
	}
	filter.IncludeArchived = query.IncludeArchived

	if values.Has("page") {
		cursor, ok := decodeResourceCursor(values.Get("page"), environmentListCursorKind)
		if !ok || cursor.Filter != filter.fingerprint() {
			return app.EnvironmentListQuery{}, environmentCursorFilter{},
				domain.Validation("invalid page cursor")
		}
		createdAt, ok := parseTimeParam(cursor.CreatedAt)
		if !ok {
			return app.EnvironmentListQuery{}, environmentCursorFilter{},
				domain.Validation("invalid page cursor")
		}
		query.After = &app.ResourcePageBoundary{CreatedAt: createdAt.UTC(), ID: cursor.ID}
	}
	return query, filter, nil
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
