package domain

import "time"

const (
	DeploymentStatusActive = "active"
	DeploymentStatusPaused = "paused"

	DeploymentTriggerManual   = "manual"
	DeploymentTriggerSchedule = "schedule"
)

// Deployment is the durable autonomous-session template. AgentVersion is
// always resolved when the deployment is created or explicitly re-pinned so a
// later Agent update cannot silently alter scheduled work.
type Deployment struct {
	ID                string
	AgentID           string
	AgentVersion      int
	EnvironmentID     string
	Name              string
	Description       string
	InitialEvents     []EventDraft
	Resources         []DeploymentResource
	VaultIDs          []string
	Budget            *SessionBudget
	Metadata          map[string]string
	Schedule          *DeploymentSchedule
	Status            string
	PausedReason      *DeploymentPausedReason
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ArchivedAt        *time.Time
	ScheduleClaimedAt *time.Time
}

// DeploymentResource is the write-safe subset of a Session resource retained
// by a deployment. Create-time Git repository snapshots are not Deployment
// templates in the current product slice.
type DeploymentResource struct {
	Type          string
	FileID        string
	MountPath     *string
	MemoryStoreID string
	Access        string
	Instructions  string
}

type DeploymentSchedule struct {
	Expression     string
	Timezone       string
	LastRunAt      *time.Time
	UpcomingRunsAt []time.Time
}

type DeploymentPausedReason struct {
	Type      string
	ErrorType string
}

type DeploymentPatch struct {
	AgentID       *string
	AgentVersion  *int
	EnvironmentID *string
	Name          *string
	Description   *string
	InitialEvents *[]EventDraft
	Resources     *[]DeploymentResource
	VaultIDs      *[]string
	Metadata      map[string]*string
	ScheduleSet   bool
	Schedule      *DeploymentSchedule
	BudgetSet     bool
	Budget        *SessionBudget
}

type DeploymentRun struct {
	ID           string
	DeploymentID string
	AgentID      string
	AgentVersion int
	SessionID    *string
	ErrorType    string
	ErrorMessage string
	TriggerType  string
	ScheduledAt  *time.Time
	CreatedAt    time.Time
}
