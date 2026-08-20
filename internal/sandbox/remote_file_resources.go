package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
)

const (
	remoteResourceControlRoot = "/var/lib/mango/session-resources"
	remoteResourceMarkerRoot  = remoteResourceControlRoot + "/markers"
)

type remoteFilePermissions struct {
	Mode int
}

type remoteFileInfo struct {
	SizeBytes int64
	Regular   bool
	Directory bool
}

// remoteFileResourceDataPlane is deliberately private and smaller than a
// portable filesystem. Each adapter maps these operations to its pinned
// provider SDK; CMA resource lifecycle and validation remain above it.
type remoteFileResourceDataPlane interface {
	ResourceCreateDirectory(context.Context, string, remoteFilePermissions) error
	ResourceUpload(context.Context, string, io.Reader, remoteFilePermissions) error
	ResourceOpen(context.Context, string) (io.ReadCloser, error)
	ResourceStat(context.Context, string) (remoteFileInfo, error)
	ResourceRemoveFile(context.Context, string) error
	ResourceIsNotFound(error) bool
}

type remoteFileResources struct {
	provider string
	files    remoteFileResourceDataPlane
	mu       sync.Mutex
}

func newRemoteFileResources(
	provider string,
	files remoteFileResourceDataPlane,
) *remoteFileResources {
	return &remoteFileResources{provider: provider, files: files}
}

func (r *remoteFileResources) HasFileResource(
	ctx context.Context,
	mount FileResourceMount,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := validateFileResourceMount(mount); err != nil {
		return false, Permanent(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	info, err := r.files.ResourceStat(ctx, path.Clean(mount.RuntimePath))
	if r.files.ResourceIsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("sandbox: %s inspect File Resource: %w", r.provider, err)
	}
	if !info.Regular {
		return false, nil
	}

	marker, err := r.readMarker(ctx, mount.RuntimePath)
	if r.files.ResourceIsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return marker == resourceMarker(mount), nil
}

func (r *remoteFileResources) ImportFileResource(
	ctx context.Context,
	mount FileResourceMount,
	content io.Reader,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if content == nil {
		return errors.New("sandbox: File Resource content is required")
	}
	if err := validateFileResourceMount(mount); err != nil {
		return Permanent(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.ensureLayout(ctx, mount.RuntimePath); err != nil {
		return err
	}
	target := path.Clean(mount.RuntimePath)
	// Publish the new identity before overwriting the visible copy. The pending
	// marker does not satisfy HasFileResource, but it prevents a stale detach
	// from deleting the new identity while an interrupted import is retried.
	if err := r.writeMarker(
		ctx, mount.RuntimePath, pendingResourceMarker(mount.Identity),
	); err != nil {
		return err
	}

	hash := sha256.New()
	limited := &io.LimitedReader{R: content, N: mount.SizeBytes + 1}
	counter := &countingReader{reader: io.TeeReader(limited, hash)}
	if err := r.files.ResourceUpload(ctx, target, counter, remoteFilePermissions{
		Mode: 0o666,
	}); err != nil {
		return fmt.Errorf("sandbox: %s stream File Resource: %w", r.provider, err)
	}
	if counter.count != mount.SizeBytes {
		return fmt.Errorf(
			"sandbox: File Resource size mismatch: received %d bytes, expected %d",
			counter.count,
			mount.SizeBytes,
		)
	}
	if checksum := hex.EncodeToString(hash.Sum(nil)); checksum != mount.ChecksumSHA256 {
		return errors.New("sandbox: File Resource checksum mismatch")
	}
	stored, err := r.files.ResourceStat(ctx, target)
	if err != nil {
		return fmt.Errorf("sandbox: %s inspect imported File Resource: %w", r.provider, err)
	}
	if !stored.Regular || stored.SizeBytes != mount.SizeBytes {
		return errors.New("sandbox: provider did not persist the complete File Resource")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.writeMarker(ctx, mount.RuntimePath, resourceMarker(mount)); err != nil {
		return err
	}
	return nil
}

func (r *remoteFileResources) RemoveFileResource(
	ctx context.Context,
	runtimePath string,
	identity string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateResourceIdentity(identity); err != nil {
		return Permanent(err)
	}
	if _, err := resourceRelativePath(runtimePath); err != nil {
		return Permanent(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	marker, err := r.readMarker(ctx, runtimePath)
	if r.files.ResourceIsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if resourceMarkerIdentity([]byte(marker)) != identity {
		return nil
	}
	if err := r.removeFileIfPresent(ctx, path.Clean(runtimePath)); err != nil {
		return err
	}
	if err := r.removeFileIfPresent(ctx, remoteResourceMarkerPath(runtimePath)); err != nil {
		return err
	}
	return nil
}

func (r *remoteFileResources) ensureLayout(
	ctx context.Context,
	runtimePath string,
) error {
	for _, directory := range []struct {
		path string
		mode int
	}{
		{remoteResourceControlRoot, 0o755},
		{remoteResourceMarkerRoot, 0o755},
		{SessionUploadsRoot, 0o777},
	} {
		if err := r.ensureDirectory(ctx, directory.path, directory.mode); err != nil {
			return err
		}
	}
	relative, err := resourceRelativePath(runtimePath)
	if err != nil {
		return Permanent(err)
	}
	parent := SessionUploadsRoot
	components := strings.Split(path.Dir(relative), "/")
	for _, component := range components {
		if component == "" || component == "." {
			continue
		}
		parent = path.Join(parent, component)
		if err := r.ensureDirectory(ctx, parent, 0o777); err != nil {
			return err
		}
	}
	return nil
}

func (r *remoteFileResources) ensureDirectory(
	ctx context.Context,
	directory string,
	mode int,
) error {
	info, err := r.files.ResourceStat(ctx, directory)
	if err == nil {
		if info.Directory {
			return nil
		}
		return fmt.Errorf(
			"sandbox: %s resource path %s is not a directory",
			r.provider,
			directory,
		)
	}
	if !r.files.ResourceIsNotFound(err) {
		return fmt.Errorf(
			"sandbox: %s inspect resource directory %s: %w",
			r.provider,
			directory,
			err,
		)
	}
	permissions := remoteFilePermissions{Mode: mode}
	createErr := r.files.ResourceCreateDirectory(ctx, directory, permissions)
	if createErr == nil {
		return nil
	}
	// A successful provider-side create can still lose its acknowledgement.
	// Re-read the durable state before returning a retryable error.
	info, statErr := r.files.ResourceStat(ctx, directory)
	if statErr == nil && info.Directory {
		return nil
	}
	return fmt.Errorf(
		"sandbox: %s create resource directory %s: %w",
		r.provider,
		directory,
		createErr,
	)
}

func (r *remoteFileResources) writeMarker(
	ctx context.Context,
	runtimePath string,
	marker string,
) error {
	permissions := remoteFilePermissions{Mode: 0o600}
	if err := r.files.ResourceUpload(
		ctx,
		remoteResourceMarkerPath(runtimePath),
		strings.NewReader(marker),
		permissions,
	); err != nil {
		return fmt.Errorf("sandbox: %s write File Resource marker: %w", r.provider, err)
	}
	return nil
}

func (r *remoteFileResources) readMarker(
	ctx context.Context,
	runtimePath string,
) (string, error) {
	markerPath := remoteResourceMarkerPath(runtimePath)
	info, err := r.files.ResourceStat(ctx, markerPath)
	if err != nil {
		return "", err
	}
	if !info.Regular || info.SizeBytes > 4096 {
		return "", errors.New("sandbox: File Resource marker is invalid")
	}
	reader, err := r.files.ResourceOpen(ctx, markerPath)
	if err != nil {
		return "", fmt.Errorf("sandbox: %s open File Resource marker: %w", r.provider, err)
	}
	defer reader.Close() //nolint:errcheck // read error below is authoritative
	raw, err := io.ReadAll(io.LimitReader(reader, 4097))
	if err != nil {
		return "", fmt.Errorf("sandbox: %s read File Resource marker: %w", r.provider, err)
	}
	if len(raw) > 4096 {
		return "", errors.New("sandbox: File Resource marker exceeds its limit")
	}
	return string(raw), nil
}

func (r *remoteFileResources) removeFileIfPresent(
	ctx context.Context,
	filePath string,
) error {
	err := r.files.ResourceRemoveFile(ctx, filePath)
	if err == nil || r.files.ResourceIsNotFound(err) {
		return nil
	}
	return fmt.Errorf("sandbox: %s remove File Resource path %s: %w", r.provider, filePath, err)
}

func remoteResourceMarkerPath(runtimePath string) string {
	sum := sha256.Sum256([]byte(path.Clean(runtimePath)))
	return path.Join(remoteResourceMarkerRoot, hex.EncodeToString(sum[:]))
}

func pendingResourceMarker(identity string) string {
	return identity + "\npending\n"
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	read, err := r.reader.Read(buffer)
	r.count += int64(read)
	return read, err
}

func remotePermissionDigits(mode int) string {
	return "0" + strconv.FormatInt(int64(mode), 8)
}
