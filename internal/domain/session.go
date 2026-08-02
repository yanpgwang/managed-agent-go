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
	// EnvironmentType is captured with the session so tool routing remains
	// stable even if the environment resource is later archived. Empty on older
	// sessions means cloud.
	EnvironmentType string
	Status          Status
	Title           string
	Metadata        map[string]any
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
