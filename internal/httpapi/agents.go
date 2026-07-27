package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string          `json:"name"`
		Model       json.RawMessage `json:"model"`
		System      *string         `json:"system"`
		Description *string         `json:"description"`
		Tools       []any           `json:"tools"`
		MCPServers  []any           `json:"mcp_servers"`
		Skills      []any           `json:"skills"`
		Multiagent  json.RawMessage `json:"multiagent"`
		Metadata    map[string]any  `json:"metadata"`
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
	_ = json.Unmarshal(in.Model, &rawModel)
	a := domain.Agent{
		Name: in.Name, Model: parseModel(rawModel), System: in.System, Description: in.Description,
		Tools: in.Tools, MCPServers: in.MCPServers, Skills: in.Skills, Metadata: in.Metadata,
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
	as, err := s.deps.Agents.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": mapAgents(as), "next_page": nil})
}

func (s *Server) listAgentVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := s.deps.Agents.Versions(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": mapAgents(vs)})
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        *string         `json:"name"`
		Model       json.RawMessage `json:"model"`
		System      json.RawMessage `json:"system"`
		Description json.RawMessage `json:"description"`
		Tools       json.RawMessage `json:"tools"`
		MCPServers  json.RawMessage `json:"mcp_servers"`
		Skills      json.RawMessage `json:"skills"`
		Multiagent  json.RawMessage `json:"multiagent"`
		Metadata    map[string]any  `json:"metadata"`
		Version     *int            `json:"version"`
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
	skills, err := parseOptionalArray(in.Skills, "skills")
	if err != nil {
		writeError(w, err)
		return
	}
	if in.Version != nil && *in.Version < 1 {
		writeError(w, domain.Validation("version must be at least 1"))
		return
	}
	patch := domain.AgentPatch{
		Name: in.Name, System: system, Description: description,
		Tools: tools, MCPServers: mcpServers, Skills: skills,
		Multiagent: multiagent, Metadata: in.Metadata, ExpectedVersion: in.Version,
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
		m := parseModel(raw)
		if m.ID == "" {
			writeError(w, domain.Validation("model id is required"))
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
