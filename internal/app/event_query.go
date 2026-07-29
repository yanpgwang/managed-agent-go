package app

import "time"

// EventQuery expresses the public List Events filters. Sequence bounds are
// internal cursor values and never appear directly on the HTTP wire.
type EventQuery struct {
	AfterSeq     int64
	BeforeSeq    int64
	Limit        int
	Desc         bool
	Types        []string
	CreatedAtGt  *time.Time
	CreatedAtGte *time.Time
	CreatedAtLt  *time.Time
	CreatedAtLte *time.Time
}
