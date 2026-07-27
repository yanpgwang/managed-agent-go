package domain

import (
	"reflect"
	"time"
)

type Model struct {
	ID     string
	Effort string
	Speed  string
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
	Skills      []any
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
	Skills          *[]any
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
		next.Model = *p.Model
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

// AgentOverrides expresses per-session agent configuration overrides
// (agent_with_overrides). Each field is applied only when Set is true. For list
// fields, a nil slice with Set=true clears the field; model is never clearable.
type AgentOverrides struct {
	Model      *Model
	System     *NullableString
	Tools      *[]any
	MCPServers *[]any
	Skills     *[]any
}

// WithOverrides returns a copy of the agent with session-local overrides
// applied. It does not change version or identity; the returned snapshot still
// reports the base agent's id and version so a session traces back to it. Each
// provided field replaces (never merges) the agent's value.
func (a Agent) WithOverrides(o AgentOverrides) Agent {
	next := a
	if o.Model != nil {
		next.Model = *o.Model
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
