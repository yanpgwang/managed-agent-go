package httpapi

import (
	"bytes"
	"encoding/json"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// This file holds the transport DTO <-> internal domain mappings. Internal
// fields (sequence numbers, run/lease bookkeeping) must never cross into a
// response, and the public wire shapes here are the single source used by
// send, list, and stream alike.

func parseModel(raw any) domain.Model {
	switch v := raw.(type) {
	case string:
		return domain.Model{ID: v}
	case map[string]any:
		m := domain.Model{}
		if id, ok := v["id"].(string); ok {
			m.ID = id
		}
		switch e := v["effort"].(type) {
		case string:
			m.Effort = e
		case map[string]any:
			if t, ok := e["type"].(string); ok {
				m.Effort = t
			}
		}
		if sp, ok := v["speed"].(string); ok {
			m.Speed = sp
		}
		return m
	}
	return domain.Model{}
}

func parseNullableStrict(raw json.RawMessage, field string) (*domain.NullableString, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if string(bytes.TrimSpace(raw)) == "null" {
		return &domain.NullableString{Set: true}, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, domain.Validation(field + " must be a string or null")
	}
	return &domain.NullableString{Set: true, Value: &value}, nil
}

func agentToJSON(a domain.Agent) map[string]any {
	model := map[string]any{"id": a.Model.ID}
	if a.Model.Effort != "" {
		model["effort"] = map[string]any{"type": a.Model.Effort}
	}
	if a.Model.Speed != "" {
		model["speed"] = a.Model.Speed
	}
	out := map[string]any{
		"id": a.ID, "type": "agent", "version": a.Version, "name": a.Name,
		"model": model, "metadata": orEmptyMap(a.Metadata), "multiagent": a.Multiagent,
		"tools": orEmpty(a.Tools), "mcp_servers": orEmpty(a.MCPServers), "skills": orEmpty(a.Skills),
		"created_at": a.CreatedAt.Format(timeFmt), "updated_at": a.UpdatedAt.Format(timeFmt),
	}
	out["system"] = a.System
	out["description"] = a.Description
	if a.ArchivedAt != nil {
		out["archived_at"] = a.ArchivedAt.Format(timeFmt)
	} else {
		out["archived_at"] = nil
	}
	return out
}

// agentSnapshotJSON builds the resolved public agent snapshot embedded in a
// session's `agent` field. It omits agent-resource-level bookkeeping
// (created_at/updated_at/archived_at) that the session agent object does not
// carry, matching BetaManagedAgentsSessionAgent.
func agentSnapshotJSON(a domain.Agent) map[string]any {
	model := map[string]any{"id": a.Model.ID}
	if a.Model.Effort != "" {
		model["effort"] = map[string]any{"type": a.Model.Effort}
	}
	if a.Model.Speed != "" {
		model["speed"] = a.Model.Speed
	}
	return map[string]any{
		"id": a.ID, "type": "agent", "version": a.Version, "name": a.Name,
		"model": model, "system": derefStr(a.System), "description": derefStr(a.Description),
		"multiagent": a.Multiagent,
		"tools":      orEmpty(a.Tools), "mcp_servers": orEmpty(a.MCPServers), "skills": orEmpty(a.Skills),
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func orEmpty(v []any) []any {
	if v == nil {
		return []any{}
	}
	return v
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
