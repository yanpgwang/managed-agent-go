package domain

import "time"

// SessionThread is a durable execution identity exposed below a Session. Every
// Thread owns its Agent snapshot, status, timing, and usage projection. A
// Session aggregates those projections; it is not the storage backing for the
// primary Thread.
type SessionThread struct {
	ID             string
	SessionID      string
	ParentThreadID *string
	Agent          Agent
	// Advisor is non-nil only for a platform-spawned consultation Thread. Such a
	// Thread never executes the ordinary child Workflow or exposes Agent fields.
	Advisor              *Advisor
	Status               Status
	Usage                TokenUsage
	ModelListCostNanoUSD int64
	ListCostKnown        bool
	BudgetPaused         bool
	ActiveSeconds        float64
	RunningSince         *time.Time
	TerminatedAt         *time.Time
	StartupSeconds       float64
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ArchivedAt           *time.Time
}

// NewAdvisorSessionThread returns the final projection of one Mango-managed
// consultation. The Thread is born and terminates within one private client-tool
// execution; its lifecycle remains observable through the independent ledger.
func NewAdvisorSessionThread(
	id string,
	sessionID string,
	parentThreadID string,
	advisor Advisor,
	usage TokenUsage,
	listCostNanoUSD int64,
	listCostKnown bool,
	now time.Time,
) SessionThread {
	now = now.UTC().Truncate(time.Microsecond)
	parent := parentThreadID
	terminatedAt := now
	return SessionThread{
		ID: id, SessionID: sessionID, ParentThreadID: &parent,
		Advisor: &advisor, Status: StatusTerminated, Usage: usage,
		ModelListCostNanoUSD: listCostNanoUSD, ListCostKnown: listCostKnown,
		TerminatedAt: &terminatedAt, CreatedAt: now, UpdatedAt: now,
	}
}

func (t SessionThread) AgentName() string {
	if t.Advisor != nil {
		return AdvisorAgentName
	}
	return t.Agent.Name
}

// NewPrimarySessionThread captures the execution fields of a newly created
// Session in its primary Thread. Identity and creation time remain stable after
// this point even as the Session aggregate changes.
func NewPrimarySessionThread(id string, session Session) SessionThread {
	thread := SessionThread{
		ID: id, SessionID: session.ID, CreatedAt: session.CreatedAt.UTC(),
	}
	thread.ApplyPrimarySessionProjection(session)
	return thread
}

// NewChildSessionThread captures one callable roster member for independent
// execution inside a Session. The Agent is already a full Session-owned
// snapshot; no Agent resource lookup belongs on this path.
func NewChildSessionThread(
	id string,
	sessionID string,
	parentThreadID string,
	agent Agent,
	now time.Time,
) SessionThread {
	now = now.UTC().Truncate(time.Microsecond)
	parent := parentThreadID
	agent.Multiagent = nil
	return SessionThread{
		ID: id, SessionID: sessionID, ParentThreadID: &parent,
		Agent: agent, Status: StatusIdle, ListCostKnown: true,
		CreatedAt: now, UpdatedAt: now,
	}
}

// ApplyPrimarySessionProjection synchronizes the existing single-Thread
// runtime into its independent primary projection. Once child execution is
// enabled, child writers update their own Thread first and the Session layer
// recomputes its aggregate separately; Thread reads remain unchanged.
func (t *SessionThread) ApplyPrimarySessionProjection(session Session) {
	t.Agent = session.AgentSnapshot
	t.Status = session.Status
	t.Usage = session.Usage
	t.ModelListCostNanoUSD = session.ModelListCostNanoUSD
	t.ListCostKnown = session.ListCostKnown
	t.ActiveSeconds = session.ActiveSeconds
	t.RunningSince = utcTimePtr(session.RunningSince)
	t.TerminatedAt = utcTimePtr(session.TerminatedAt)
	t.UpdatedAt = session.UpdatedAt.UTC()
	t.ArchivedAt = utcTimePtr(session.ArchivedAt)
	if t.ArchivedAt != nil {
		t.Status = StatusTerminated
		t.TerminatedAt = utcTimePtr(t.ArchivedAt)
	}
}

// ApplyIndependentPrimarySessionProjection synchronizes Session-owned control
// fields without replacing the primary Thread's execution projection. In a
// multi-agent Session, usage, list cost, status, and timing are owned by each
// Thread and must not be copied back from the Session aggregate.
func (t *SessionThread) ApplyIndependentPrimarySessionProjection(session Session) {
	t.Agent = session.AgentSnapshot
	t.UpdatedAt = session.UpdatedAt.UTC()
	if session.ArchivedAt == nil {
		return
	}
	archivedAt := session.ArchivedAt.UTC()
	t.TransitionStatus(StatusTerminated, archivedAt)
	t.ArchivedAt = &archivedAt
	t.UpdatedAt = session.UpdatedAt.UTC()
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

// TransitionStatus updates a Thread's independent timing projection. Session
// aggregation is performed by the storage transaction after the owning Thread
// moves; it must never be copied back into sibling Thread projections.
func (t *SessionThread) TransitionStatus(next Status, now time.Time) {
	now = now.UTC()
	if t.Status == StatusRunning && next != StatusRunning && t.RunningSince != nil {
		t.ActiveSeconds += max(0, now.Sub(*t.RunningSince).Seconds())
		t.RunningSince = nil
	}
	if t.Status != StatusRunning && next == StatusRunning {
		runningSince := now
		t.RunningSince = &runningSince
	}
	if next == StatusTerminated && t.TerminatedAt == nil {
		terminatedAt := now
		t.TerminatedAt = &terminatedAt
	}
	t.Status = next
	t.UpdatedAt = now
}

func utcTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}
