package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func envToJSON(e domain.Environment) map[string]any {
	out := map[string]any{
		"id": e.ID, "type": "environment", "name": e.Name,
		"description": e.Description,
		"metadata":    orEmptyMap(e.Metadata),
		"config":      map[string]any{"type": e.ConfigType},
		"created_at":  e.CreatedAt.Format(timeFmt), "updated_at": e.UpdatedAt.Format(timeFmt),
	}
	if len(e.Config) > 0 {
		out["config"] = e.Config
	}
	if e.Scope != "" {
		out["scope"] = e.Scope
	} else {
		out["scope"] = nil
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
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Config      map[string]any `json:"config"`
		Metadata    map[string]any `json:"metadata"`
		Scope       string         `json:"scope"`
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
	e, err := s.deps.Envs.Create(r.Context(), domain.Environment{
		Name: in.Name, Description: in.Description, ConfigType: ct, Config: in.Config,
		Metadata: in.Metadata, Scope: in.Scope,
	})
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

// listEnvironments implements the documented List Environments query surface:
// exactly include_archived, limit, and page. Unlike List Agents there are no
// created_at filters here, and the `limit` reference documents neither a
// default nor a maximum, so the bounds applied are Mango's own.
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

	// List Environments documents no created_at filters and no `order`. Reject
	// them rather than returning a page that silently ignores the caller's
	// intent. Unknown parameters remain tolerated because the official SDK
	// appends its own (for example `beta=true`).
	if err := rejectUnsupportedListParams(values,
		"created_at[gt]", "created_at[gte]", "created_at[lt]", "created_at[lte]",
		"order"); err != nil {
		return app.EnvironmentListQuery{}, environmentCursorFilter{}, err
	}

	if values.Has("limit") {
		limit, err := parsePositiveLimit(values.Get("limit"))
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
		include, err := parseBoolParam(values.Get("include_archived"), "include_archived")
		if err != nil {
			return app.EnvironmentListQuery{}, environmentCursorFilter{}, err
		}
		query.IncludeArchived = include
	}
	filter.IncludeArchived = query.IncludeArchived

	if values.Has("page") {
		cursor, ok := decodeResourceCursor(values.Get("page"), environmentListCursorKind)
		if !ok {
			return app.EnvironmentListQuery{}, environmentCursorFilter{},
				domain.Validation("invalid page cursor")
		}
		if cursor.Filter != filter.fingerprint() {
			return app.EnvironmentListQuery{}, environmentCursorFilter{},
				domain.Validation("page cursor filter mismatch")
		}
		createdAt, ok := parseTimeParam(cursor.CreatedAt)
		if !ok {
			return app.EnvironmentListQuery{}, environmentCursorFilter{},
				domain.Validation("invalid page cursor")
		}
		query.After = &app.EnvironmentPageBoundary{CreatedAt: createdAt.UTC(), ID: cursor.ID}
	}

	return query, filter, nil
}

// updateEnvironment implements POST /v1/environments/{environment_id}. The
// documented body is exactly config, description, metadata, name, and scope;
// every one of them preserves the stored value when omitted.
func (s *Server) updateEnvironment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Config      json.RawMessage `json:"config"`
		Description *string         `json:"description"`
		Metadata    json.RawMessage `json:"metadata"`
		Name        *string         `json:"name"`
		Scope       *string         `json:"scope"`
	}
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	patch := domain.EnvironmentPatch{
		Name: in.Name, Description: in.Description, Scope: in.Scope,
	}
	metadata, err := parseEnvironmentMetadataPatch(in.Metadata)
	if err != nil {
		writeError(w, err)
		return
	}
	patch.Metadata = metadata
	config, err := parseEnvironmentConfigPatch(in.Config)
	if err != nil {
		writeError(w, err)
		return
	}
	patch.Config = config

	updated, err := s.deps.Envs.Update(r.Context(), r.PathValue("id"), patch)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, envToJSON(updated))
}

// parseEnvironmentMetadataPatch keeps the null-vs-absent distinction that the
// documented delete rule depends on. An absent field preserves every key; a
// present object is a patch whose null or empty-string values delete keys.
//
// Session metadata deletes only on null, so this parser is intentionally not
// shared with the session update path.
func parseEnvironmentMetadataPatch(raw json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		// A null metadata field is an absent patch, not "delete everything":
		// upstream documents deletion per key, never for the whole map.
		return nil, nil
	}
	var patch map[string]any
	if err := json.Unmarshal(trimmed, &patch); err != nil || patch == nil {
		return nil, domain.Validation("metadata must be an object")
	}
	for _, value := range patch {
		if value == nil {
			continue
		}
		if _, ok := value.(string); !ok {
			return nil, domain.Validation("metadata values must be strings or null")
		}
	}
	return patch, nil
}

func parseEnvironmentConfigPatch(raw json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var config map[string]any
	if err := json.Unmarshal(trimmed, &config); err != nil || config == nil {
		return nil, domain.Validation("config must be an object")
	}
	return config, nil
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
