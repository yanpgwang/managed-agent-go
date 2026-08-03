package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// vaultIDsUpdateRejectedMessage describes the Update Session contract only.
// Managed Agents accepts vault_ids when a Session is created, but documents the
// update field as reserved and rejects requests that set it.
const vaultIDsUpdateRejectedMessage = "vault_ids is reserved for future use on session update and is " +
	"rejected by the Managed Agents API; omit it"

const vaultIDsCreateUnsupportedMessage = "session vault_ids are not implemented; " +
	"the Managed Agents API accepts vault_ids when creating a session"

func (s *Server) registerSessionRoutes() {
	s.mux.HandleFunc("POST /v1/sessions", s.createSession)
	s.mux.HandleFunc("GET /v1/sessions", s.listSessions)
	s.mux.HandleFunc("GET /v1/sessions/{id}", s.getSession)
	s.mux.HandleFunc("POST /v1/sessions/{id}", s.updateSession)
	s.mux.HandleFunc("POST /v1/sessions/{id}/archive", s.archiveSession)
	s.mux.HandleFunc("DELETE /v1/sessions/{id}", s.deleteSession)
	s.mux.HandleFunc("POST /v1/sessions/{id}/events", s.sendEvents)
	s.mux.HandleFunc("GET /v1/sessions/{id}/events", s.listEvents)
	s.mux.HandleFunc("GET /v1/sessions/{id}/events/stream", s.streamEvents)
}

func sessionToJSON(s domain.Session) map[string]any {
	activeSeconds, durationSeconds := s.ObservableStats(time.Now())
	outcomes := make([]any, 0, len(s.Outcomes))
	for _, outcome := range s.Outcomes {
		item := map[string]any{
			"type": "outcome_evaluation", "outcome_id": outcome.OutcomeID,
			"description": outcome.Description, "result": outcome.Result,
			"explanation": nil, "iteration": outcome.Iteration,
		}
		if outcome.Explanation != "" {
			item["explanation"] = outcome.Explanation
		}
		if outcome.CompletedAt != nil {
			item["completed_at"] = outcome.CompletedAt.Format(timeFmt)
		} else {
			item["completed_at"] = nil
		}
		outcomes = append(outcomes, item)
	}
	out := map[string]any{
		"id": s.ID, "type": "session", "status": string(s.Status),
		"agent":          agentSnapshotJSON(s.AgentSnapshot),
		"environment_id": s.EnvironmentID, "title": s.Title, "metadata": orEmptyMap(s.Metadata),
		"created_at": s.CreatedAt.Format(timeFmt), "updated_at": s.UpdatedAt.Format(timeFmt),
		"outcome_evaluations": outcomes,
		"resources":           []any{},
		"stats": map[string]any{
			"active_seconds":   activeSeconds,
			"duration_seconds": durationSeconds,
		},
		"usage": map[string]any{
			"cache_creation": map[string]any{
				"ephemeral_1h_input_tokens": s.Usage.CacheCreation.Ephemeral1hInputTokens,
				"ephemeral_5m_input_tokens": s.Usage.CacheCreation.Ephemeral5mInputTokens,
			},
			"cache_read_input_tokens": s.Usage.CacheReadInputTokens,
			"input_tokens":            s.Usage.InputTokens,
			"output_tokens":           s.Usage.OutputTokens,
		},
		"vault_ids":     []string{},
		"deployment_id": nil,
	}
	if s.ArchivedAt != nil {
		out["archived_at"] = s.ArchivedAt.Format(timeFmt)
	} else {
		out["archived_at"] = nil
	}
	return out
}

// agentRef captures a parsed `agent` field from a create request in any of the
// three documented forms.
type agentRef struct {
	ID        string
	Version   *int
	Overrides *domain.AgentOverrides
}

// parseAgentRef decodes the `agent` field: a bare string (latest), a pinned
// {type:"agent",id,version}, or an {type:"agent_with_overrides",id,...} object.
func parseAgentRef(raw json.RawMessage) (agentRef, error) {
	var asString string
	if json.Unmarshal(raw, &asString) == nil && asString != "" {
		return agentRef{ID: asString}, nil
	}
	var obj struct {
		Type       string          `json:"type"`
		ID         string          `json:"id"`
		Version    *int            `json:"version"`
		Model      json.RawMessage `json:"model"`
		System     json.RawMessage `json:"system"`
		Tools      json.RawMessage `json:"tools"`
		MCPServers json.RawMessage `json:"mcp_servers"`
		Skills     json.RawMessage `json:"skills"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&obj); err != nil {
		return agentRef{}, domain.Validation("agent must be an agent ID or agent reference object")
	}
	if obj.Type != "agent" && obj.Type != "agent_with_overrides" {
		return agentRef{}, domain.Validation("agent reference type must be agent or agent_with_overrides")
	}
	if obj.ID == "" {
		return agentRef{}, domain.Validation("agent reference id is required")
	}
	if obj.Version != nil && *obj.Version < 1 {
		return agentRef{}, domain.Validation("agent reference version must be at least 1")
	}
	ref := agentRef{ID: obj.ID, Version: obj.Version}
	if obj.Type == "agent" &&
		(len(obj.Model) > 0 || len(obj.System) > 0 || len(obj.Tools) > 0 ||
			len(obj.MCPServers) > 0 || len(obj.Skills) > 0) {
		return agentRef{}, domain.Validation("agent references cannot include overrides")
	}
	if obj.Type == "agent_with_overrides" {
		ov := &domain.AgentOverrides{}
		if len(obj.Model) > 0 {
			if bytes.Equal(bytes.TrimSpace(obj.Model), []byte("null")) {
				return agentRef{}, domain.Validation("agent override model cannot be null")
			}
			var m any
			if err := json.Unmarshal(obj.Model, &m); err != nil {
				return agentRef{}, domain.Validation("agent override model must be a string or object")
			}
			mm, err := parseModel(m)
			if err != nil {
				return agentRef{}, err
			}
			ov.Model = &mm
		}
		system, err := parseNullableStrict(obj.System, "agent override system")
		if err != nil {
			return agentRef{}, err
		}
		ov.System = system
		if ov.Tools, err = parseOptionalArray(obj.Tools, "agent override tools"); err != nil {
			return agentRef{}, err
		}
		if ov.MCPServers, err = parseOptionalArray(obj.MCPServers, "agent override mcp_servers"); err != nil {
			return agentRef{}, err
		}
		if ov.Skills, err = parseOptionalArray(obj.Skills, "agent override skills"); err != nil {
			return agentRef{}, err
		}
		ref.Overrides = ov
	}
	return ref, nil
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Agent         json.RawMessage   `json:"agent"`
		EnvironmentID string            `json:"environment_id"`
		Title         string            `json:"title"`
		Metadata      map[string]any    `json:"metadata"`
		InitialEvents []map[string]any  `json:"initial_events"`
		Resources     []json.RawMessage `json:"resources"`
		VaultIDs      []string          `json:"vault_ids"`
	}
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if len(in.Resources) > 0 {
		writeError(w, domain.Unsupported("session resources are not implemented"))
		return
	}
	if len(in.VaultIDs) > 0 {
		writeError(w, domain.Unsupported(vaultIDsCreateUnsupportedMessage))
		return
	}

	ref, err := parseAgentRef(in.Agent)
	if err != nil {
		writeError(w, err)
		return
	}
	// initial_events accepts only user.message / user.define_outcome.
	if len(in.InitialEvents) > 50 {
		writeError(w, domain.Validation("initial_events must contain at most 50 events"))
		return
	}
	for _, it := range in.InitialEvents {
		t, _ := it["type"].(string)
		if !domain.IsInitialEventType(t) {
			writeError(w, domain.Validation("initial_events may contain only user.message or user.define_outcome"))
			return
		}
		if err := validateClientEvent(it); err != nil {
			writeError(w, err)
			return
		}
	}
	if err := validateClientEventBatch(in.InitialEvents); err != nil {
		writeError(w, err)
		return
	}
	drafts := toDrafts(in.InitialEvents)
	sess, err := s.deps.Sessions.Create(r.Context(), app.CreateSessionInput{
		AgentID: ref.ID, AgentVersion: ref.Version, Overrides: ref.Overrides,
		EnvironmentID: in.EnvironmentID, Title: in.Title, Metadata: in.Metadata,
		InitialEvents: drafts,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, sessionToJSON(sess))
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.deps.Sessions.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, sessionToJSON(sess))
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	params, filter, err := parseSessionListParams(r)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := s.deps.Sessions.List(r.Context(), params)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]any, 0, len(page.Sessions))
	for _, se := range page.Sessions {
		out = append(out, sessionToJSON(se))
	}

	var nextPage any
	var prevPage any
	fingerprint := filter.fingerprint()
	order := "asc"
	if params.Desc {
		order = "desc"
	}
	if page.HasNext && len(page.Sessions) > 0 {
		last := page.Sessions[len(page.Sessions)-1]
		nextPage = encodeSessionCursor(sessionCursor{
			Direction: "next",
			Order:     order,
			CreatedAt: last.CreatedAt.UTC().Format(timeFmt),
			ID:        last.ID,
			Filter:    fingerprint,
		})
	}
	if page.HasPrev && len(page.Sessions) > 0 {
		first := page.Sessions[0]
		prevPage = encodeSessionCursor(sessionCursor{
			Direction: "prev",
			Order:     order,
			CreatedAt: first.CreatedAt.UTC().Format(timeFmt),
			ID:        first.ID,
			Filter:    fingerprint,
		})
	}
	writeJSON(w, 200, map[string]any{"data": out, "next_page": nextPage, "prev_page": prevPage})
}

const defaultSessionLimit = 100

func parseSessionListParams(r *http.Request) (app.ListPage, sessionCursorFilter, error) {
	query := r.URL.Query()
	params := app.ListPage{Limit: defaultSessionLimit, Desc: true}
	filter := sessionCursorFilter{}

	order := query.Get("order")
	if order != "" && order != "asc" && order != "desc" {
		return app.ListPage{}, sessionCursorFilter{}, domain.Validation("order must be asc or desc")
	}
	if order == "asc" {
		params.Desc = false
	}

	if query.Has("limit") {
		limit, err := strconv.Atoi(query.Get("limit"))
		if err != nil || limit <= 0 {
			return app.ListPage{}, sessionCursorFilter{}, domain.Validation("limit must be a positive integer")
		}
		if limit > maxPageLimit {
			return app.ListPage{}, sessionCursorFilter{}, domain.Validation("limit must not exceed 1000")
		}
		params.Limit = limit
	}

	params.AgentID = query.Get("agent_id")
	filter.AgentID = params.AgentID
	if query.Has("agent_version") {
		if params.AgentID == "" {
			return app.ListPage{}, sessionCursorFilter{},
				domain.Validation("agent_version requires agent_id")
		}
		version, err := strconv.Atoi(query.Get("agent_version"))
		if err != nil || version <= 0 {
			return app.ListPage{}, sessionCursorFilter{},
				domain.Validation("agent_version must be a positive integer")
		}
		params.AgentVersion = &version
		filter.AgentVersion = &version
	}

	if query.Has("include_archived") {
		switch query.Get("include_archived") {
		case "true":
			params.IncludeArchived = true
		case "false":
			params.IncludeArchived = false
		default:
			return app.ListPage{}, sessionCursorFilter{},
				domain.Validation("include_archived must be true or false")
		}
	}
	filter.IncludeArchived = params.IncludeArchived

	if query.Has("deployment_id") {
		value := query.Get("deployment_id")
		params.DeploymentID = &value
		filter.DeploymentID = &value
	}
	if query.Has("memory_store_id") {
		value := query.Get("memory_store_id")
		params.MemoryStoreID = &value
		filter.MemoryStoreID = &value
	}

	for _, item := range []struct {
		key         string
		destination **time.Time
		normalized  *string
	}{
		{"created_at[gt]", &params.CreatedAtGt, &filter.CreatedAtGt},
		{"created_at[gte]", &params.CreatedAtGte, &filter.CreatedAtGte},
		{"created_at[lt]", &params.CreatedAtLt, &filter.CreatedAtLt},
		{"created_at[lte]", &params.CreatedAtLte, &filter.CreatedAtLte},
	} {
		if !query.Has(item.key) {
			continue
		}
		parsed, ok := parseTimeParam(query.Get(item.key))
		if !ok {
			return app.ListPage{}, sessionCursorFilter{},
				domain.Validation(item.key + " must be an RFC 3339 timestamp")
		}
		utc := parsed.UTC()
		*item.destination = &utc
		*item.normalized = utc.Format(timeFmt)
	}

	statusSet := map[string]struct{}{}
	for _, value := range query["statuses[]"] {
		switch domain.Status(value) {
		case domain.StatusRescheduling, domain.StatusRunning, domain.StatusIdle, domain.StatusTerminated:
			statusSet[value] = struct{}{}
		default:
			return app.ListPage{}, sessionCursorFilter{},
				domain.Validation("statuses[] contains an invalid session status")
		}
	}
	filter.Statuses = make([]string, 0, len(statusSet))
	for status := range statusSet {
		filter.Statuses = append(filter.Statuses, status)
	}
	sort.Strings(filter.Statuses)
	params.Statuses = make([]domain.Status, len(filter.Statuses))
	for index, status := range filter.Statuses {
		params.Statuses[index] = domain.Status(status)
	}

	if query.Has("page") {
		cursor, ok := decodeSessionCursor(query.Get("page"))
		if !ok {
			return app.ListPage{}, sessionCursorFilter{}, domain.Validation("invalid page cursor")
		}
		wantOrder := "asc"
		if params.Desc {
			wantOrder = "desc"
		}
		if cursor.Order != wantOrder {
			return app.ListPage{}, sessionCursorFilter{}, domain.Validation("page cursor order mismatch")
		}
		if cursor.Filter != filter.fingerprint() {
			return app.ListPage{}, sessionCursorFilter{}, domain.Validation("page cursor filter mismatch")
		}
		createdAt, ok := parseTimeParam(cursor.CreatedAt)
		if !ok {
			return app.ListPage{}, sessionCursorFilter{}, domain.Validation("invalid page cursor")
		}
		params.Boundary = &app.SessionPageBoundary{
			CreatedAt: createdAt.UTC(),
			ID:        cursor.ID,
			Backward:  cursor.Direction == "prev",
		}
	}

	return params, filter, nil
}

// updateSession implements the documented four-field update body: `agent`
// (mid-session tools/mcp_servers replacement), `metadata` (per-key patch),
// `title`, and `vault_ids` (rejected, as upstream rejects it too).
func (s *Server) updateSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Agent    json.RawMessage `json:"agent"`
		Metadata json.RawMessage `json:"metadata"`
		Title    *string         `json:"title"`
		VaultIDs json.RawMessage `json:"vault_ids"`
	}
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if len(bytes.TrimSpace(in.VaultIDs)) > 0 {
		writeError(w, domain.Unsupported(vaultIDsUpdateRejectedMessage))
		return
	}
	update := domain.SessionUpdate{Title: in.Title}
	metadata, err := parseSessionMetadataPatch(in.Metadata)
	if err != nil {
		writeError(w, err)
		return
	}
	update.Metadata = metadata
	if update.AgentTools, update.AgentMCPServers, err = parseSessionAgentUpdate(in.Agent); err != nil {
		writeError(w, err)
		return
	}
	var sess domain.Session
	if update.IsEmpty() {
		// A body with no update fields is a plain read; do not take the
		// session's admission lock for it.
		sess, err = s.deps.Sessions.Get(r.Context(), r.PathValue("id"))
	} else {
		sess, err = s.deps.Sessions.Update(r.Context(), r.PathValue("id"), update)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, sessionToJSON(sess))
}

// parseSessionMetadataPatch decodes the session metadata patch. Omitted
// preserves the whole bag, a string upserts one key, and an explicit null
// deletes one key. This is deliberately not shared with any replacement-style
// metadata handling: the session update contract patches per key.
func parseSessionMetadataPatch(raw json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
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

// parseSessionAgentUpdate decodes the mid-session `agent` object. Only `tools`
// and `mcp_servers` are updatable; `model`, `system`, and `skills` are fixed
// for the session's lifetime and are rejected with a pointer at the create-time
// override that does support them.
func parseSessionAgentUpdate(raw json.RawMessage) (tools, mcpServers *[]any, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return nil, nil, domain.Validation("agent must be an object")
	}
	for key := range fields {
		switch key {
		case "tools", "mcp_servers":
		case "model", "system", "skills":
			return nil, nil, domain.Validation(
				"agent " + key + " cannot be updated mid-session; " +
					"supply it as an agent_with_overrides field when creating the session",
			)
		default:
			return nil, nil, domain.Validation(
				fmt.Sprintf("unknown agent update field %q", key),
			)
		}
	}
	if tools, err = parseOptionalArray(fields["tools"], "agent tools"); err != nil {
		return nil, nil, err
	}
	if mcpServers, err = parseOptionalArray(fields["mcp_servers"], "agent mcp_servers"); err != nil {
		return nil, nil, err
	}
	return tools, mcpServers, nil
}

func (s *Server) archiveSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.deps.Sessions.Archive(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, sessionToJSON(sess))
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Sessions.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": r.PathValue("id"), "type": "session_deleted"})
}
