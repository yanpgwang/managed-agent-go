package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string          `json:"name"`
		Model       json.RawMessage `json:"model"`
		System      *string         `json:"system"`
		Description *string         `json:"description"`
		Tools       json.RawMessage `json:"tools"`
		MCPServers  json.RawMessage `json:"mcp_servers"`
		Skills      json.RawMessage `json:"skills"`
		Multiagent  json.RawMessage `json:"multiagent"`
		Metadata    json.RawMessage `json:"metadata"`
	}
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	multiagent, err := parseMultiagent(in.Multiagent)
	if err != nil {
		writeError(w, err)
		return
	}
	var rawModel any
	if err := json.Unmarshal(in.Model, &rawModel); err != nil {
		writeError(w, domain.Validation("model must be a string or object"))
		return
	}
	model, err := parseModel(rawModel)
	if err != nil {
		writeError(w, err)
		return
	}
	tools, err := parseOptionalNonNullJSON[[]any](in.Tools, "tools")
	if err != nil {
		writeError(w, err)
		return
	}
	mcpServers, err := parseOptionalNonNullJSON[[]any](in.MCPServers, "mcp_servers")
	if err != nil {
		writeError(w, err)
		return
	}
	skills, err := parseOptionalNonNullSkillReferences(in.Skills, "skills")
	if err != nil {
		writeError(w, err)
		return
	}
	metadata, err := parseOptionalNonNullJSON[map[string]any](in.Metadata, "metadata")
	if err != nil {
		writeError(w, err)
		return
	}
	a := domain.Agent{
		Name: in.Name, Model: model, System: in.System, Description: in.Description,
	}
	if tools != nil {
		a.Tools = *tools
	}
	if mcpServers != nil {
		a.MCPServers = *mcpServers
	}
	if skills != nil {
		a.Skills = *skills
	}
	if metadata != nil {
		a.Metadata = *metadata
	}
	if multiagent != nil {
		a.Multiagent = *multiagent
	}
	created, err := s.deps.Agents.Create(r.Context(), a)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, agentToJSON(created))
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	a, err := s.deps.Agents.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, agentToJSON(a))
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	query, filter, err := parseAgentListParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := s.deps.Agents.List(r.Context(), query)
	if err != nil {
		writeError(w, err)
		return
	}
	var nextPage any
	if page.HasNext && len(page.Agents) > 0 {
		last := page.Agents[len(page.Agents)-1]
		nextPage = encodeResourceCursor(resourceCursor{
			Kind:      agentListCursorKind,
			CreatedAt: last.CreatedAt.UTC().Format(timeFmt),
			ID:        last.ID,
			Filter:    filter.fingerprint(),
		})
	}
	writeJSON(w, 200, map[string]any{
		"data": mapAgents(page.Agents), "next_page": nextPage,
	})
}

func parseAgentListParams(r *http.Request) (app.AgentListQuery, agentCursorFilter, error) {
	values := r.URL.Query()
	query := app.AgentListQuery{Limit: app.DefaultAgentListLimit}
	filter := agentCursorFilter{}

	if values.Has("limit") {
		limit, err := parseResourceListLimit(values.Get("limit"))
		if err != nil {
			return app.AgentListQuery{}, agentCursorFilter{}, err
		}
		if limit > app.MaxAgentListLimit {
			return app.AgentListQuery{}, agentCursorFilter{},
				domain.Validation("limit must not exceed 100")
		}
		query.Limit = limit
	}
	if values.Has("include_archived") {
		include, err := parseResourceListBool(values.Get("include_archived"), "include_archived")
		if err != nil {
			return app.AgentListQuery{}, agentCursorFilter{}, err
		}
		query.IncludeArchived = include
	}
	filter.IncludeArchived = query.IncludeArchived

	for _, bound := range []struct {
		key         string
		destination **time.Time
		normalized  *string
	}{
		{"created_at[gte]", &query.CreatedAtGte, &filter.CreatedAtGte},
		{"created_at[lte]", &query.CreatedAtLte, &filter.CreatedAtLte},
	} {
		if !values.Has(bound.key) {
			continue
		}
		parsed, ok := parseTimeParam(values.Get(bound.key))
		if !ok {
			return app.AgentListQuery{}, agentCursorFilter{},
				domain.Validation(bound.key + " must be an RFC 3339 timestamp")
		}
		utc := parsed.UTC()
		*bound.destination = &utc
		*bound.normalized = utc.Format(timeFmt)
	}

	if values.Has("page") {
		cursor, ok := decodeResourceCursor(values.Get("page"), agentListCursorKind)
		if !ok || cursor.Filter != filter.fingerprint() {
			return app.AgentListQuery{}, agentCursorFilter{}, domain.Validation("invalid page cursor")
		}
		createdAt, ok := parseTimeParam(cursor.CreatedAt)
		if !ok {
			return app.AgentListQuery{}, agentCursorFilter{}, domain.Validation("invalid page cursor")
		}
		query.After = &app.ResourcePageBoundary{CreatedAt: createdAt.UTC(), ID: cursor.ID}
	}
	return query, filter, nil
}

func (s *Server) listAgentVersions(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	query, err := parseAgentVersionListParams(r, agentID)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := s.deps.Agents.Versions(r.Context(), agentID, query)
	if err != nil {
		writeError(w, err)
		return
	}
	var nextPage any
	if page.HasNext && len(page.Versions) > 0 {
		last := page.Versions[len(page.Versions)-1]
		nextPage = encodeAgentVersionCursor(agentID, last.Version)
	}
	writeJSON(w, 200, map[string]any{
		"data": mapAgents(page.Versions), "next_page": nextPage,
	})
}

func parseAgentVersionListParams(r *http.Request, agentID string) (app.AgentVersionListQuery, error) {
	values := r.URL.Query()
	query := app.AgentVersionListQuery{Limit: app.DefaultAgentListLimit}
	if values.Has("limit") {
		limit, err := parseResourceListLimit(values.Get("limit"))
		if err != nil {
			return app.AgentVersionListQuery{}, err
		}
		if limit > app.MaxAgentListLimit {
			return app.AgentVersionListQuery{}, domain.Validation("limit must not exceed 100")
		}
		query.Limit = limit
	}
	if values.Has("page") {
		afterVersion, ok := decodeAgentVersionCursor(values.Get("page"), agentID)
		if !ok {
			return app.AgentVersionListQuery{}, domain.Validation("invalid page cursor")
		}
		query.AfterVersion = afterVersion
	}
	return query, nil
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        json.RawMessage `json:"name"`
		Model       json.RawMessage `json:"model"`
		System      json.RawMessage `json:"system"`
		Description json.RawMessage `json:"description"`
		Tools       json.RawMessage `json:"tools"`
		MCPServers  json.RawMessage `json:"mcp_servers"`
		Skills      json.RawMessage `json:"skills"`
		Multiagent  json.RawMessage `json:"multiagent"`
		Metadata    json.RawMessage `json:"metadata"`
		Version     json.RawMessage `json:"version"`
	}
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	multiagent, err := parseMultiagent(in.Multiagent)
	if err != nil {
		writeError(w, err)
		return
	}
	system, err := parseNullableStrict(in.System, "system")
	if err != nil {
		writeError(w, err)
		return
	}
	description, err := parseNullableStrict(in.Description, "description")
	if err != nil {
		writeError(w, err)
		return
	}
	tools, err := parseOptionalArray(in.Tools, "tools")
	if err != nil {
		writeError(w, err)
		return
	}
	mcpServers, err := parseOptionalArray(in.MCPServers, "mcp_servers")
	if err != nil {
		writeError(w, err)
		return
	}
	skills, err := parseOptionalSkillReferenceReplacement(in.Skills, "skills")
	if err != nil {
		writeError(w, err)
		return
	}
	name, err := parseOptionalNonNullJSON[string](in.Name, "name")
	if err != nil {
		writeError(w, err)
		return
	}
	metadata, err := parseOptionalNonNullJSON[map[string]any](in.Metadata, "metadata")
	if err != nil {
		writeError(w, err)
		return
	}
	version, err := parseOptionalNonNullJSON[int](in.Version, "version")
	if err != nil {
		writeError(w, err)
		return
	}
	if version != nil && *version < 1 {
		writeError(w, domain.Validation("version must be at least 1"))
		return
	}
	patch := domain.AgentPatch{
		Name: name, System: system, Description: description,
		Tools: tools, MCPServers: mcpServers, Skills: skills,
		Multiagent: multiagent, ExpectedVersion: version,
	}
	if metadata != nil {
		patch.Metadata = *metadata
	}
	if len(in.Model) > 0 {
		if bytes.Equal(bytes.TrimSpace(in.Model), []byte("null")) {
			writeError(w, domain.Validation("model cannot be null"))
			return
		}
		var raw any
		if err := json.Unmarshal(in.Model, &raw); err != nil {
			writeError(w, domain.Validation("model must be a string or object"))
			return
		}
		m, err := parseModel(raw)
		if err != nil {
			writeError(w, err)
			return
		}
		patch.Model = &m
	}
	up, err := s.deps.Agents.Update(r.Context(), r.PathValue("id"), patch)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, agentToJSON(up))
}

func (s *Server) archiveAgent(w http.ResponseWriter, r *http.Request) {
	a, err := s.deps.Agents.Archive(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, agentToJSON(a))
}

// parseMultiagent preserves the beta API's optional object as opaque JSON.
// Absence means "leave unset/preserve", an object replaces the current
// topology, and explicit null is represented by a pointer to a nil map so an
// update can clear it. Arrays and scalars are invalid.
func parseMultiagent(raw json.RawMessage) (*map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		var cleared map[string]any
		return &cleared, nil
	}
	var object map[string]any
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return nil, domain.Validation("multiagent must be an object")
	}
	return &object, nil
}

// parseOptionalArray preserves the update tri-state: omitted means preserve,
// explicit null means clear, and an array means full replacement.
func parseOptionalArray(raw json.RawMessage, field string) (*[]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var values []any
	if bytes.Equal(trimmed, []byte("null")) {
		return &values, nil
	}
	if err := json.Unmarshal(trimmed, &values); err != nil || values == nil {
		return nil, domain.Validation(field + " must be an array or null")
	}
	return &values, nil
}

func mapAgents(as []domain.Agent) []any {
	out := make([]any, 0, len(as))
	for _, a := range as {
		out = append(out, agentToJSON(a))
	}
	return out
}
