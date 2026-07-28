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
	// OutputEventIDs are the exact committed event ids this run appended when it
	// closed (agent output plus the run's terminal/status events), persisted in
	// the same transaction that closes the run. Empty until completion. This is
	// internal-only durable state and is never serialized onto the public API.
	OutputEventIDs []string
	State          RunState
	Error          *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
