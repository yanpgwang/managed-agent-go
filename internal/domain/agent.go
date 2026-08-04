package domain

import (
	"reflect"
	"time"
)

type Model struct {
	ID     string
	Effort string
	Speed  string
	// EffortExplicit and SpeedExplicit distinguish an explicit Agent setting
	// from the Managed Agents defaults echoed in the resolved resource. The
	// Messages adapter uses this distinction to avoid sending preview fields to
	// endpoints when the caller only accepted the platform default.
	EffortExplicit bool
	SpeedExplicit  bool
}

const (
	DefaultModelEffort = "high"
	DefaultModelSpeed  = "standard"
)

// NormalizeModel fills the defaults the Managed Agents API exposes on a
// resolved Agent while preserving whether the user supplied each value.
func NormalizeModel(model Model) Model {
	if model.Effort == "" {
		model.Effort = DefaultModelEffort
	}
	if model.Speed == "" {
		model.Speed = DefaultModelSpeed
	}
	return model
}

// ValidateModel enforces the public model configuration enums. Model support
// for a particular effort/speed combination remains a provider concern.
func ValidateModel(model Model) error {
	if model.ID == "" {
		return Validation("model is required")
	}
	switch model.Effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return Validation("model effort must be low, medium, high, xhigh, or max")
	}
	switch model.Speed {
	case "", "standard", "fast":
	default:
		return Validation("model speed must be standard or fast")
	}
	return nil
}

type Agent struct {
	ID          string
	Version     int
	Name        string
	Model       Model
	System      *string
	Description *string
	Tools       []any
	MCPServers  []any
	Skills      []SkillReference
	Multiagent  map[string]any
	Metadata    map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  *time.Time
}

type NullableString struct {
	Set   bool
	Value *string
}

type AgentPatch struct {
	Name            *string
	Model           *Model
	System          *NullableString
	Description     *NullableString
	Tools           *[]any
	MCPServers      *[]any
	Skills          *[]SkillReference
	Multiagent      *map[string]any
	Metadata        map[string]any
	ExpectedVersion *int
}

func (a Agent) Apply(p AgentPatch) (Agent, bool, error) {
	if p.ExpectedVersion != nil && *p.ExpectedVersion != a.Version {
		return a, false, Conflict("agent version mismatch")
	}
	next := a
	// deep-copy source metadata only if present
	if a.Metadata != nil {
		next.Metadata = make(map[string]any, len(a.Metadata))
		for k, v := range a.Metadata {
			next.Metadata[k] = v
		}
	}
	if p.Name != nil {
		next.Name = *p.Name
	}
	if p.Model != nil {
		model := *p.Model
		// Managed Agents treats effort as the one sticky model field: updating
		// the same model id without effort preserves the stored level. Changing
		// ids resets an omitted effort to that model's default. Other omitted
		// model fields, including speed, take their defaults.
		if model.ID == a.Model.ID && !model.EffortExplicit {
			model.Effort = a.Model.Effort
			model.EffortExplicit = a.Model.EffortExplicit
		}
		next.Model = NormalizeModel(model)
	}
	if p.System != nil {
		next.System = p.System.Value
	}
	if p.Description != nil {
		next.Description = p.Description.Value
	}
	if p.Tools != nil {
		next.Tools = *p.Tools
	}
	if p.MCPServers != nil {
		next.MCPServers = *p.MCPServers
	}
	if p.Skills != nil {
		next.Skills = *p.Skills
	}
	if p.Multiagent != nil {
		next.Multiagent = *p.Multiagent
	}
	// apply metadata patch (allocate lazily if source was nil)
	for k, v := range p.Metadata {
		if v == nil {
			delete(next.Metadata, k) // safe on nil map
		} else {
			if next.Metadata == nil {
				next.Metadata = map[string]any{}
			}
			next.Metadata[k] = v
		}
	}
	// normalize: empty non-nil map != nil under DeepEqual
	if len(next.Metadata) == 0 {
		next.Metadata = nil
	}
	changed := !agentFieldsEqual(a, next)
	return next, changed, nil
}

func agentFieldsEqual(a, b Agent) bool {
	a.Version, b.Version = 0, 0
	a.CreatedAt, b.CreatedAt = time.Time{}, time.Time{}
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

// SessionSnapshotJSON is the public resolved-agent projection embedded in a
// Session (`session.agent`) and in a `session.updated` event. It omits the
// agent-resource bookkeeping (created_at/updated_at/archived_at) that the
// session agent object does not carry. It lives here because the HTTP layer and
// the durable event ledger must publish exactly the same shape.
func (a Agent) SessionSnapshotJSON() map[string]any {
	model := map[string]any{"id": a.Model.ID}
	if a.Model.Effort != "" {
		model["effort"] = map[string]any{"type": a.Model.Effort}
	}
	if a.Model.Speed != "" {
		model["speed"] = a.Model.Speed
	}
	system, description := "", ""
	if a.System != nil {
		system = *a.System
	}
	if a.Description != nil {
		description = *a.Description
	}
	return map[string]any{
		"id": a.ID, "type": "agent", "version": a.Version, "name": a.Name,
		"model": model, "system": system, "description": description,
		"multiagent": a.Multiagent,
		"tools":      orEmptyList(a.Tools), "mcp_servers": orEmptyList(a.MCPServers),
		"skills": orEmptyList(a.Skills),
	}
}

func orEmptyList[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

// AgentOverrides expresses per-session agent configuration overrides
// (agent_with_overrides). Each field is applied only when Set is true. For list
// fields, a nil slice with Set=true clears the field; model is never clearable.
type AgentOverrides struct {
	Model      *Model
	System     *NullableString
	Tools      *[]any
	MCPServers *[]any
	Skills     *[]SkillReference
}

// WithOverrides returns a copy of the agent with session-local overrides
// applied. It does not change version or identity; the returned snapshot still
// reports the base agent's id and version so a session traces back to it. Each
// provided field replaces (never merges) the agent's value.
func (a Agent) WithOverrides(o AgentOverrides) Agent {
	next := a
	if o.Model != nil {
		model := *o.Model
		// Per-session model overrides may select a model and speed, but the
		// official API does not apply effort supplied inside the override. The
		// Agent's resolved effort remains authoritative for the Session.
		model.Effort = a.Model.Effort
		model.EffortExplicit = a.Model.EffortExplicit
		next.Model = NormalizeModel(model)
	}
	if o.System != nil {
		next.System = o.System.Value
	}
	if o.Tools != nil {
		next.Tools = *o.Tools
	}
	if o.MCPServers != nil {
		next.MCPServers = *o.MCPServers
	}
	if o.Skills != nil {
		next.Skills = *o.Skills
	}
	return next
}
