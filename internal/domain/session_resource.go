package domain

import (
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	SessionUploadsRoot                = "/mnt/session/uploads"
	SessionOutputsRoot                = "/mnt/session/outputs"
	SessionMemoryRoot                 = "/mnt/memory"
	SessionRepositoryRoot             = "/workspace"
	MaxSessionFileMountPathBytes      = 1024
	MaxSessionFileMountComponentBytes = 255
	MaxSessionMemoryStores            = 8
	MaxSessionMemoryInstructionsChars = 4096
)

const (
	SessionResourceTypeFile          = "file"
	SessionResourceTypeMemoryStore   = "memory_store"
	SessionResourceTypeGitRepository = "git_repository"
	GitRepositoryCheckoutBranch      = "branch"
	GitRepositoryCheckoutCommit      = "commit"
	MemoryAccessReadWrite            = "read_write"
	MemoryAccessReadOnly             = "read_only"
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
	ID                       string
	SessionID                string
	ResourceType             string
	SourceFileID             string
	FileID                   string
	BlobKey                  string `json:"-"`
	MemoryStoreID            string
	MemoryAccess             string
	MemoryInstructions       string
	MemoryStoreName          string
	MemoryStoreDescription   string
	RepositoryURL            string
	RepositoryCheckoutType   string
	RepositoryCheckoutValue  string
	RepositoryResolvedCommit string
	MountPath                string
	CreatedAt                time.Time
	UpdatedAt                time.Time
	State                    SessionResourceState
}

func (r SessionResource) Type() string {
	if r.ResourceType == "" {
		return SessionResourceTypeFile
	}
	return r.ResourceType
}

var gitCommitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// NormalizeGitRepositoryCheckout validates the user-selected branch or exact
// commit. Empty values mean the repository's advertised default branch.
func NormalizeGitRepositoryCheckout(checkoutType, value string) (string, string, error) {
	if checkoutType == "" && value == "" {
		return "", "", nil
	}
	switch checkoutType {
	case GitRepositoryCheckoutCommit:
		if !gitCommitPattern.MatchString(value) {
			return "", "", Validation("checkout.sha must be a full 40-character Git commit SHA")
		}
		return checkoutType, strings.ToLower(value), nil
	case GitRepositoryCheckoutBranch:
		if !validGitBranchName(value) {
			return "", "", Validation("checkout.name is not a valid Git branch name")
		}
		return checkoutType, value, nil
	default:
		return "", "", Validation("checkout.type must be branch or commit")
	}
}

// NormalizeGitRepositoryMountPath confines repositories to Mango's writable
// workspace and reserves the immutable custom-Skill subtree.
func NormalizeGitRepositoryMountPath(rawURL string, requested *string) (string, error) {
	parsed, err := validateGitRepositoryURL(rawURL)
	if err != nil {
		return "", err
	}
	var raw string
	if requested == nil {
		name, err := repositoryName(parsed)
		if err != nil {
			return "", err
		}
		raw = path.Join(SessionRepositoryRoot, name)
	} else {
		raw = *requested
	}
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
	if clean == SessionRepositoryRoot || !strings.HasPrefix(clean, SessionRepositoryRoot+"/") {
		return "", Validation("mount_path must be a child of /workspace")
	}
	if SessionFileMountPathsConflict(clean, SessionSkillsRoot) {
		return "", Validation("mount_path cannot overlap the custom Skill directory")
	}
	if err := validateSessionFileMountPath(clean); err != nil {
		return "", err
	}
	return clean, nil
}

// ValidateGitRepositoryURL accepts only anonymous HTTPS remotes. Credentials
// and additional URL surfaces are deliberately deferred until Mango has a
// first-class secret reference rather than accepting raw tokens.
func ValidateGitRepositoryURL(raw string) error {
	_, err := validateGitRepositoryURL(raw)
	return err
}

func validateGitRepositoryURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > 2048 || !utf8.ValidString(raw) {
		return nil, Validation("url must be a valid HTTPS Git repository URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(raw, "#") ||
		parsed.Opaque != "" {
		return nil, Validation("url must be an anonymous HTTPS Git repository URL without query or fragment")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		return nil, Validation("url must identify a Git repository")
	}
	for _, character := range raw {
		if unicode.IsControl(character) {
			return nil, Validation("url cannot contain control characters")
		}
	}
	return parsed, nil
}

func repositoryName(parsed *url.URL) (string, error) {
	name, err := url.PathUnescape(path.Base(strings.TrimSuffix(parsed.Path, "/")))
	if err != nil {
		return "", Validation("url contains an invalid repository path")
	}
	name = strings.TrimSuffix(name, ".git")
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") ||
		!utf8.ValidString(name) || len(name) > MaxSessionFileMountComponentBytes {
		return "", Validation("url must produce a safe repository mount name")
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return "", Validation("url must produce a safe repository mount name")
		}
	}
	return name, nil
}

func validGitBranchName(value string) bool {
	if value == "" || len(value) > 1024 || !utf8.ValidString(value) ||
		strings.HasPrefix(value, "-") ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasSuffix(value, ".") || strings.Contains(value, "..") ||
		strings.Contains(value, "@{") || strings.Contains(value, "//") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." ||
			strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) ||
			strings.ContainsRune(`~^:?*[\\`, character) {
			return false
		}
	}
	return true
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
