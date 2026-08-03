package domain

import "time"

type Environment struct {
	ID          string
	Name        string
	Description string
	Metadata    map[string]any
	Scope       string
	ConfigType  string // "cloud" | "self_hosted"
	Config      map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  *time.Time
}
