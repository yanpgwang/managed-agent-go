package domain

import (
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	SessionUploadsRoot                = "/mnt/session/uploads"
	SessionOutputsRoot                = "/mnt/session/outputs"
	SessionMemoryRoot                 = "/mnt/memory"
	MaxSessionFileMountPathBytes      = 1024
	MaxSessionFileMountComponentBytes = 255
	MaxSessionMemoryStores            = 8
	MaxSessionMemoryInstructionsChars = 4096
)

const (
	SessionResourceTypeFile        = "file"
	SessionResourceTypeMemoryStore = "memory_store"
	MemoryAccessReadWrite          = "read_write"
	MemoryAccessReadOnly           = "read_only"
)

type SessionResourceState string

const (
	SessionResourceActive   SessionResourceState = "active"
	SessionResourceDeleting SessionResourceState = "deleting"
)

// SessionResource is the durable union of a File attachment and a Memory Store
// attachment. File resources own a session-scoped File copy. Memory Store
// resources snapshot presentation fields at Session creation so later Store
// renames do not move a live sandbox mount.
type SessionResource struct {
	ID                     string
	SessionID              string
	ResourceType           string
	SourceFileID           string
	FileID                 string
	BlobKey                string `json:"-"`
	MemoryStoreID          string
	MemoryAccess           string
	MemoryInstructions     string
	MemoryStoreName        string
	MemoryStoreDescription string
	MountPath              string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	State                  SessionResourceState
}

func (r SessionResource) Type() string {
	if r.ResourceType == "" {
		return SessionResourceTypeFile
	}
	return r.ResourceType
}

// NormalizeSessionMemoryStoreMountPath follows the Managed Agents convention:
// lowercase the Store name and collapse each run of non-alphanumeric Unicode
// characters to one hyphen beneath /mnt/memory.
func NormalizeSessionMemoryStoreMountPath(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", Validation("memory store name must be valid UTF-8")
	}
	var slug strings.Builder
	lastHyphen := false
	for _, character := range strings.ToLower(name) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			slug.WriteRune(character)
			lastHyphen = false
			continue
		}
		if slug.Len() > 0 && !lastHyphen {
			slug.WriteByte('-')
			lastHyphen = true
		}
	}
	value := strings.TrimSuffix(slug.String(), "-")
	if value == "" {
		return "", Validation("memory store name must produce a non-empty mount slug")
	}
	if len(value) > MaxSessionFileMountComponentBytes {
		return "", Validation("memory store mount slug cannot exceed 255 bytes")
	}
	return path.Join(SessionMemoryRoot, value), nil
}

func NormalizeSessionFileMountPath(fileID string, requested *string) (string, error) {
	if fileID == "" {
		return "", Validation("file_id is required")
	}
	if !utf8.ValidString(fileID) || strings.Contains(fileID, "/") {
		return "", Validation("file_id is invalid")
	}
	for _, character := range fileID {
		if unicode.IsControl(character) {
			return "", Validation("file_id is invalid")
		}
	}
	if requested == nil {
		mountPath := path.Join(SessionUploadsRoot, fileID)
		if !strings.HasPrefix(mountPath, SessionUploadsRoot+"/") {
			return "", Validation("file_id cannot escape the Session uploads directory")
		}
		if err := validateSessionFileMountPath(mountPath); err != nil {
			return "", err
		}
		return mountPath, nil
	}
	raw := *requested
	if raw == "" || !strings.HasPrefix(raw, "/") {
		return "", Validation("mount_path must be an absolute path")
	}
	if !utf8.ValidString(raw) {
		return "", Validation("mount_path must be valid UTF-8")
	}
	for _, character := range raw {
		if unicode.IsControl(character) {
			return "", Validation("mount_path cannot contain control characters")
		}
	}
	for _, component := range strings.Split(raw, "/") {
		if component == ".." {
			return "", Validation("mount_path cannot contain parent traversal")
		}
	}
	clean := path.Clean(raw)
	if clean == "/" || clean == SessionUploadsRoot {
		return "", Validation("mount_path must identify a file")
	}
	var mountPath string
	if strings.HasPrefix(clean, SessionUploadsRoot+"/") {
		mountPath = clean
	} else {
		mountPath = path.Join(SessionUploadsRoot, strings.TrimPrefix(clean, "/"))
	}
	if err := validateSessionFileMountPath(mountPath); err != nil {
		return "", err
	}
	return mountPath, nil
}

func validateSessionFileMountPath(mountPath string) error {
	if len(mountPath) > MaxSessionFileMountPathBytes {
		return Validation("mount_path exceeds 1024 bytes after normalization")
	}
	for _, component := range strings.Split(strings.TrimPrefix(mountPath, "/"), "/") {
		if len(component) > MaxSessionFileMountComponentBytes {
			return Validation("mount_path components cannot exceed 255 bytes")
		}
	}
	return nil
}

// SessionFileMountPathsConflict reports both exact collisions and file/tree
// collisions. A mounted file cannot also be the parent of another mounted
// file, because materialization would otherwise depend on insertion order.
func SessionFileMountPathsConflict(left string, right string) bool {
	left = path.Clean(left)
	right = path.Clean(right)
	return left == right || strings.HasPrefix(left, right+"/") ||
		strings.HasPrefix(right, left+"/")
}
