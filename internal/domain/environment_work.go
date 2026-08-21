package domain

import "time"

type EnvironmentWorkState string

const (
	EnvironmentWorkQueued   EnvironmentWorkState = "queued"
	EnvironmentWorkStarting EnvironmentWorkState = "starting"
	EnvironmentWorkActive   EnvironmentWorkState = "active"
	EnvironmentWorkStopping EnvironmentWorkState = "stopping"
	EnvironmentWorkStopped  EnvironmentWorkState = "stopped"
)

// EnvironmentWork is Mango's durable control-plane lease for a self-hosted
// Session worker. The current Anthropic EnvironmentWorker helper can consume
// it as optional interoperability evidence. It activates the existing Session
// event/tool-result protocol; it is not a second runtime.
type EnvironmentWork struct {
	ID                string
	EnvironmentID     string
	SessionID         string
	State             EnvironmentWorkState
	Metadata          map[string]string
	CreatedAt         time.Time
	AcknowledgedAt    *time.Time
	StartedAt         *time.Time
	LatestHeartbeatAt *time.Time
	StopRequestedAt   *time.Time
	StoppedAt         *time.Time
	TTLSeconds        int64
}

type EnvironmentWorkHeartbeat struct {
	LastHeartbeat time.Time
	LeaseExtended bool
	State         EnvironmentWorkState
	TTLSeconds    int64
}

type EnvironmentWorkQueueStats struct {
	Depth          int64
	Pending        int64
	OldestQueuedAt *time.Time
	WorkersPolling int64
}
