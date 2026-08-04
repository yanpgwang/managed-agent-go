package domain

import "time"

// Skill is the stable identity for a custom Skill. LatestVersion is empty when
// every immutable Version has been deleted.
type Skill struct {
	ID            string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DisplayTitle  string
	LatestVersion string
	Source        string
	TitleExplicit bool
	Ready         bool
}

// SkillVersionState is internal lifecycle state around object-store I/O. Only
// ready Versions cross the public API boundary.
type SkillVersionState string

const (
	SkillVersionUploading SkillVersionState = "uploading"
	SkillVersionReady     SkillVersionState = "ready"
	SkillVersionDeleting  SkillVersionState = "deleting"
)

// SkillVersion is immutable public metadata plus the private archive pointer
// and reconciliation state needed to keep PostgreSQL and object storage in
// agreement.
type SkillVersion struct {
	ID             string
	SkillID        string
	Version        string
	CreatedAt      time.Time
	Description    string
	Directory      string
	Name           string
	BlobKey        string
	SizeBytes      int64
	ChecksumSHA256 string
	State          SkillVersionState
	Initial        bool
}
