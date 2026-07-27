package domain

import "time"

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
	StatusRescheduling: {StatusRunning: true, StatusTerminated: true},
	StatusTerminated:   {},
}

func (s Status) CanTransitionTo(next Status) bool { return allowed[s][next] }

type Session struct {
	ID            string
	AgentID       string
	AgentVersion  int
	EnvironmentID string
	Status        Status
	Title         string
	Metadata      map[string]any
	// AgentSnapshot is the resolved agent definition captured at session
	// creation time, after version pinning and any per-session overrides. It is
	// the immutable public projection returned as the session's `agent` field.
	// Later updates or archival of the underlying agent must never mutate it.
	AgentSnapshot Agent
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ArchivedAt    *time.Time
}
