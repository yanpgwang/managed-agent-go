package domain

import "time"

const (
	PrefixRunAttempt = "ratm_"
	PrefixToolStep   = "tstep_"
)

// RunAttemptState is the durable lifecycle of one execution attempt for a
// logical SessionRun. A retry creates another attempt; it never rewrites the
// facts recorded by an earlier attempt.
type RunAttemptState string

const (
	RunAttemptActive      RunAttemptState = "active"
	RunAttemptCompleted   RunAttemptState = "completed"
	RunAttemptFailed      RunAttemptState = "failed"
	RunAttemptInterrupted RunAttemptState = "interrupted"
)

// RunAttempt is internal execution bookkeeping and is never serialized onto
// the Managed Agents public API.
type RunAttempt struct {
	ID         string
	RunID      string
	AttemptNo  int
	State      RunAttemptState
	Error      *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time
}

// ToolStepState makes the side-effect uncertainty boundary explicit.
//
//	prepared -> started -> completed
//	                   \-> ambiguous
//
// Prepared means the request is durable but execution has not begun. Started
// means the executor may have changed the world. Completed carries a durable
// result. Ambiguous means execution began but no trustworthy result was
// recorded, so the step must never be silently retried.
type ToolStepState string

const (
	ToolStepPrepared  ToolStepState = "prepared"
	ToolStepStarted   ToolStepState = "started"
	ToolStepCompleted ToolStepState = "completed"
	ToolStepAmbiguous ToolStepState = "ambiguous"
)

type ToolStepResult struct {
	Content []any `json:"content"`
	IsError bool  `json:"is_error"`
}

// ToolStep is an internal durable record of one built-in tool request and its
// execution boundary. ToolUseEventID is preassigned from the public event ID
// space so the eventual agent.tool_use and agent.tool_result can use the same
// stable correlation ID.
type ToolStep struct {
	ID             string
	AttemptID      string
	Ordinal        int
	ToolUseEventID string
	ToolName       string
	Input          map[string]any
	State          ToolStepState
	Result         *ToolStepResult
	CreatedAt      time.Time
	UpdatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
}
