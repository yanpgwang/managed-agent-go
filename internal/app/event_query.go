package app

import "time"

// EventPageBoundary is the complete key carried by an opaque List Events cursor.
// ProcessedAt is nil for events that have not been processed yet; Sequence is
// the deterministic tie-breaker for equal (including nil) timestamps.
type EventPageBoundary struct {
	ProcessedAt *time.Time
	Sequence    int64
}

// EventQuery expresses the public List Events filters. The public API calls
// these filters created_at for compatibility, but the Managed Agents contract
// applies them, ordering, and pagination to the event processed_at value.
type EventQuery struct {
	// ThreadID selects one Thread ledger. Empty means the primary Thread, which
	// is the public Session event surface.
	ThreadID       string
	Boundary       *EventPageBoundary
	Limit          int
	Desc           bool
	Types          []string
	ProcessedAtGt  *time.Time
	ProcessedAtGte *time.Time
	ProcessedAtLt  *time.Time
	ProcessedAtLte *time.Time
}
