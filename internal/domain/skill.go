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
	ID          string
	SkillID     string
	Version     string
	CreatedAt   time.Time
	Description string
	Directory   string
	Name        string
	BlobKey     string
	SizeBytes   int64
	// UncompressedSizeBytes is the exact byte footprint of the validated
	// canonical bundle before zip compression, or UnknownSkillUncompressedSize
	// for Versions created before this metadata existed. Runtime admission uses
	// it to bound per-Session staging independently of compression ratio.
	UncompressedSizeBytes int64
	ChecksumSHA256        string
	State                 SkillVersionState
	Initial               bool
}

const (
	// SessionSkillsRoot is the Docker runtime directory containing immutable
	// custom Skills. Each Skill is re-rooted beneath its validated frontmatter
	// name, independent of the uploaded archive directory's original casing.
	SessionSkillsRoot = "/workspace/skills"

	// UnknownSkillUncompressedSize marks Versions created before the runtime
	// started persisting exact expanded archive sizes. Their archive checksum is
	// still authoritative; extraction applies the normal per-bundle upper bound.
	UnknownSkillUncompressedSize int64 = -1
)
