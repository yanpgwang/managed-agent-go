package domain

import "time"

// MemoryStore is the durable, Workspace-scoped container exposed by the
// Managed Agents Memory API. Ownership stays in the relational root instead of
// leaking into the CMA wire representation.
type MemoryStore struct {
	ID          string
	Name        string
	Description string
	Metadata    map[string]string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  *time.Time
}

// Memory is the current head of one path-addressed UTF-8 document.
type Memory struct {
	ID              string
	MemoryStoreID   string
	MemoryVersionID string
	Path            string
	Content         string
	ContentSize     int64
	ContentSHA256   string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type MemoryActor struct {
	Type string
	ID   string
}

// MemoryVersion is one immutable audit snapshot. Delete rows and redacted rows
// deliberately carry nil content fields while retaining lineage and actors.
type MemoryVersion struct {
	ID            string
	MemoryStoreID string
	MemoryID      string
	Operation     string
	Path          *string
	Content       *string
	ContentSize   *int64
	ContentSHA256 *string
	CreatedAt     time.Time
	CreatedBy     MemoryActor
	RedactedAt    *time.Time
	RedactedBy    *MemoryActor
}

type MemoryListItem struct {
	Memory *Memory
	Prefix string
}
