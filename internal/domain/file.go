package domain

import "time"

// FileState is internal lifecycle state. Only ready files are visible through
// the public API; uploading and deleting rows are durable reconciliation
// intents.
type FileState string

const (
	FileStateUploading FileState = "uploading"
	FileStateReady     FileState = "ready"
	FileStateDeleting  FileState = "deleting"
)

// FileScope identifies the resource that produced a file. Client uploads have
// no scope; Session Resource copies and runtime outputs use Session scope.
type FileScope struct {
	ID   string
	Type string
}

// File is the authoritative metadata projection for one object-store blob.
// BlobKey and ChecksumSHA256 are internal integrity fields and never cross the
// public wire.
type File struct {
	ID             string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Filename       string
	MimeType       string
	SizeBytes      int64
	Downloadable   bool
	Scope          *FileScope
	BlobKey        string
	ChecksumSHA256 string
	// OutputPath is the normalized path relative to /mnt/session/outputs for a
	// runtime-produced deliverable. It is internal and never crosses the Files
	// wire contract.
	OutputPath string
	State      FileState
}
