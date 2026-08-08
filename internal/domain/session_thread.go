package domain

import "time"

// SessionThread is the durable execution identity exposed below a Session.
// The primary thread reuses the Session's agent, status, timing, and usage
// projections; child threads will add independent projections when the
// multi-agent runtime is implemented.
type SessionThread struct {
	ID             string
	SessionID      string
	ParentThreadID *string
	Agent          Agent
	Status         Status
	Usage          TokenUsage
	ActiveSeconds  float64
	RunningSince   *time.Time
	TerminatedAt   *time.Time
	StartupSeconds float64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     *time.Time
}

// ObservableStats returns the live timing projection for a thread. Duration
// freezes when a thread is archived or terminated.
func (t SessionThread) ObservableStats(now time.Time) (activeSeconds, durationSeconds float64) {
	now = now.UTC()
	activeSeconds = t.ActiveSeconds
	if t.Status == StatusRunning && t.RunningSince != nil {
		activeSeconds += max(0, now.Sub(*t.RunningSince).Seconds())
	}
	end := now
	if t.TerminatedAt != nil {
		end = *t.TerminatedAt
	} else if t.ArchivedAt != nil {
		end = *t.ArchivedAt
	}
	return activeSeconds, max(0, end.Sub(t.CreatedAt).Seconds())
}
