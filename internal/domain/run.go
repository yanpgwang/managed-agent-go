package domain

import "time"

type RunState string

const (
	RunQueued    RunState = "queued"
	RunRunning   RunState = "running"
	RunCompleted RunState = "completed"
	RunFailed    RunState = "failed"
)

// SessionRun is an internal durable work item. It is never serialized onto the
// Managed Agents public API.
type SessionRun struct {
	ID              string
	SessionID       string
	AdmissionSeq    int64
	TriggerEventIDs []string
	State           RunState
	Error           *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
