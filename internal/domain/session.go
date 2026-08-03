package domain

import (
	"reflect"
	"time"
)

type Status string

const (
	StatusIdle         Status = "idle"
	StatusRunning      Status = "running"
	StatusRescheduling Status = "rescheduling"
	StatusTerminated   Status = "terminated"
)

var allowed = map[Status]map[Status]bool{
	StatusIdle:         {StatusRunning: true, StatusTerminated: true},
	StatusRunning:      {StatusIdle: true, StatusRescheduling: true, StatusTerminated: true},
	StatusRescheduling: {StatusIdle: true, StatusRunning: true, StatusTerminated: true},
	StatusTerminated:   {},
}

func (s Status) CanTransitionTo(next Status) bool { return allowed[s][next] }

type Session struct {
	ID            string
	AgentID       string
	AgentVersion  int
	EnvironmentID string
	// EnvironmentType is captured with the session so tool routing remains
	// stable even if the environment resource is later archived. Empty on older
	// sessions means cloud.
	EnvironmentType string
	// EnvironmentConfig is the immutable sandbox configuration captured when
	// the Session is created. It is internal execution state and is not exposed
	// in the public Session response.
	EnvironmentConfig map[string]any
	Status            Status
	Title             string
	Metadata          map[string]any
	// AgentSnapshot is the resolved agent definition captured at session
	// creation time, after version pinning and any per-session overrides. It is
	// the immutable public projection returned as the session's `agent` field.
	// Later updates or archival of the underlying agent must never mutate it.
	AgentSnapshot Agent
	Usage         TokenUsage
	Outcomes      []OutcomeEvaluation
	// ActiveSeconds is the completed running time. RunningSince contributes a
	// live suffix while the Session is running. TerminatedAt freezes duration.
	ActiveSeconds float64
	RunningSince  *time.Time
	TerminatedAt  *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ArchivedAt    *time.Time
}

// SessionUpdate is the domain form of the documented `POST
// /v1/sessions/{session_id}` body. Each field carries its own tri-state so the
// transport layer can distinguish "omitted" from "explicitly cleared" without
// the state machine re-parsing JSON.
//
// `vault_ids` is intentionally absent: the official API documents it as
// rejected, so it never reaches the domain.
type SessionUpdate struct {
	// Title replaces the human-readable title when non-nil.
	Title *string
	// Metadata is a per-key patch, not a replacement: a present key with a nil
	// value deletes it and any other value upserts it. A nil map preserves the
	// whole bag. This differs from the create-time metadata bag on purpose.
	Metadata map[string]any
	// AgentTools and AgentMCPServers are session-local full replacements of the
	// resolved snapshot's lists. A non-nil pointer replaces (an empty or nil
	// slice clears); a nil pointer preserves. They never touch the underlying
	// Agent resource or its version.
	AgentTools      *[]any
	AgentMCPServers *[]any
}

// TouchesAgent reports whether the update carries a mid-session agent
// configuration change, which the official contract only permits while the
// Session is idle.
func (u SessionUpdate) TouchesAgent() bool {
	return u.AgentTools != nil || u.AgentMCPServers != nil
}

// IsEmpty reports whether the request carried no update fields at all. Such a
// request is a plain read and never needs the session's admission lock.
func (u SessionUpdate) IsEmpty() bool {
	return u.Title == nil && u.Metadata == nil && !u.TouchesAgent()
}

// SessionChange records which public fields an update actually changed. The
// `session.updated` event carries only those fields.
type SessionChange struct {
	Title    bool
	Metadata bool
	Agent    bool
}

func (c SessionChange) Any() bool { return c.Title || c.Metadata || c.Agent }

// ApplyUpdate returns the Session that results from an update request together
// with the set of fields it changed. It is pure so the storage layer can run it
// inside the same transaction that holds the per-session admission lock.
//
// The agent portion changes only the Session's resolved snapshot. The stored
// Agent version it was resolved from is never mutated, matching the documented
// rule that session updates do not propagate back to the agent.
func (s Session) ApplyUpdate(u SessionUpdate) (Session, SessionChange, error) {
	next := s
	var change SessionChange

	if u.Title != nil && *u.Title != s.Title {
		next.Title = *u.Title
		change.Title = true
	}

	if u.Metadata != nil {
		merged := make(map[string]any, len(s.Metadata)+len(u.Metadata))
		for key, value := range s.Metadata {
			merged[key] = value
		}
		for key, value := range u.Metadata {
			if value == nil {
				delete(merged, key)
				continue
			}
			merged[key] = value
		}
		if err := ValidateMetadata(merged); err != nil {
			return Session{}, SessionChange{}, err
		}
		if !metadataEqual(s.Metadata, merged) {
			next.Metadata = merged
			change.Metadata = true
		}
	}

	if u.TouchesAgent() {
		snapshot := s.AgentSnapshot
		if u.AgentTools != nil {
			snapshot.Tools = *u.AgentTools
		}
		if u.AgentMCPServers != nil {
			snapshot.MCPServers = *u.AgentMCPServers
		}
		if err := ValidateToolConfiguration(snapshot.Tools, snapshot.MCPServers); err != nil {
			return Session{}, SessionChange{}, Validation(
				"invalid agent tool configuration: " + err.Error(),
			)
		}
		if !reflect.DeepEqual(s.AgentSnapshot, snapshot) {
			next.AgentSnapshot = snapshot
			change.Agent = true
		}
	}

	return next, change, nil
}

func metadataEqual(current, next map[string]any) bool {
	if len(current) != len(next) {
		return false
	}
	for key, value := range next {
		existing, ok := current[key]
		if !ok || existing != value {
			return false
		}
	}
	return true
}

// SessionUpdatedPayload builds the `session.updated` event payload for an
// already-applied update. The documented event carries only the fields the
// request changed; metadata is additionally omitted when the resulting bag is
// empty.
func SessionUpdatedPayload(s Session, change SessionChange) map[string]any {
	payload := map[string]any{}
	if change.Agent {
		payload["agent"] = s.AgentSnapshot.SessionSnapshotJSON()
	}
	if change.Metadata && len(s.Metadata) > 0 {
		metadata := make(map[string]any, len(s.Metadata))
		for key, value := range s.Metadata {
			metadata[key] = value
		}
		payload["metadata"] = metadata
	}
	if change.Title {
		payload["title"] = s.Title
	}
	return payload
}

// OutcomeEvaluation is the Session-level projection for one define_outcome
// event. Span events remain the detailed history; this projection supports
// polling the Session resource.
type OutcomeEvaluation struct {
	OutcomeID   string
	Description string
	Result      string
	Explanation string
	Iteration   int
	CompletedAt *time.Time
}

type OutcomeSpec struct {
	OutcomeID     string
	Description   string
	Rubric        map[string]any
	MaxIterations int
}

func (s Session) ActiveOutcome() *OutcomeEvaluation {
	if len(s.Outcomes) == 0 {
		return nil
	}
	latest := s.Outcomes[len(s.Outcomes)-1]
	switch latest.Result {
	case "pending", "running", "evaluating":
		return &latest
	default:
		return nil
	}
}

func (s *Session) StartOutcome(spec OutcomeSpec) error {
	if s.ActiveOutcome() != nil {
		return Conflict("a session may have only one active outcome")
	}
	s.Outcomes = append(s.Outcomes, OutcomeEvaluation{
		OutcomeID: spec.OutcomeID, Description: spec.Description, Result: "pending",
	})
	return nil
}

func (s *Session) MarkActiveOutcomeRunning() {
	if len(s.Outcomes) == 0 {
		return
	}
	latest := &s.Outcomes[len(s.Outcomes)-1]
	if latest.Result == "pending" {
		latest.Result = "running"
	}
}

func (s *Session) ApplyOutcomeResult(
	outcomeID string,
	result string,
	explanation string,
	iteration int,
	now time.Time,
) {
	for index := len(s.Outcomes) - 1; index >= 0; index-- {
		outcome := &s.Outcomes[index]
		if outcome.OutcomeID != outcomeID {
			continue
		}
		outcome.Result = result
		outcome.Explanation = explanation
		outcome.Iteration = iteration
		switch result {
		case "satisfied", "max_iterations_reached", "failed", "interrupted":
			completedAt := now.UTC()
			outcome.CompletedAt = &completedAt
		default:
			outcome.CompletedAt = nil
		}
		return
	}
}

// TransitionStatus updates the Session's timing projection at the same
// linearization point as the status change.
func (s *Session) TransitionStatus(next Status, now time.Time) {
	now = now.UTC()
	if s.Status == StatusRunning && next != StatusRunning && s.RunningSince != nil {
		s.ActiveSeconds += max(0, now.Sub(*s.RunningSince).Seconds())
		s.RunningSince = nil
	}
	if s.Status != StatusRunning && next == StatusRunning {
		runningSince := now
		s.RunningSince = &runningSince
	}
	if next == StatusTerminated && s.TerminatedAt == nil {
		terminatedAt := now
		s.TerminatedAt = &terminatedAt
	}
	s.Status = next
	s.UpdatedAt = now
}

// ObservableStats returns the current public timing values. Duration continues
// while a Session is idle and freezes only when it terminates.
func (s Session) ObservableStats(now time.Time) (activeSeconds, durationSeconds float64) {
	now = now.UTC()
	activeSeconds = s.ActiveSeconds
	if s.Status == StatusRunning && s.RunningSince != nil {
		activeSeconds += max(0, now.Sub(*s.RunningSince).Seconds())
	}
	end := now
	if s.TerminatedAt != nil {
		end = *s.TerminatedAt
	}
	return activeSeconds, max(0, end.Sub(s.CreatedAt).Seconds())
}
