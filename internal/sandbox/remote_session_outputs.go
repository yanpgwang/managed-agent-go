package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

const remoteSessionOutputControlRoot = "/var/lib/mango/session-outputs"

// ensureSessionOutputLayout creates the only writable deliverable root exposed
// to sandbox tools and a separate adapter-owned staging directory. The staging
// path is never accepted by ReadFile or WriteFile; provider SDK calls use it
// only to stream a point-in-time tar archive back to Mango.
func (r *remoteFileResources) ensureSessionOutputLayout(ctx context.Context) error {
	for _, directory := range []struct {
		path string
		mode int
	}{
		{SessionOutputsRoot, 0o777},
		{remoteSessionOutputControlRoot, 0o700},
	} {
		if err := r.ensureDirectory(ctx, directory.path, directory.mode); err != nil {
			return err
		}
	}
	return nil
}

// openRemoteSessionOutputs asks the isolated sandbox to archive exactly the
// Session output directory, then streams that regular file through the
// provider SDK. The application layer remains responsible for rejecting links,
// devices, traversal, oversize files, and archive changes between passes.
func openRemoteSessionOutputs(
	ctx context.Context,
	provider string,
	resources *remoteFileResources,
	execute func(context.Context, Command) (*Result, error),
) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resources == nil || resources.files == nil || execute == nil {
		return nil, errors.New("sandbox: remote Session output data plane is required")
	}
	if err := resources.ensureSessionOutputLayout(ctx); err != nil {
		return nil, err
	}
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, fmt.Errorf("sandbox: allocate remote Session output snapshot: %w", err)
	}
	archivePath := remoteSessionOutputControlRoot + "/snapshot-" +
		hex.EncodeToString(suffix[:]) + ".tar"
	cleanup := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), remoteDefaultPeriod)
		defer cancel()
		err := resources.files.ResourceRemoveFile(cleanupCtx, archivePath)
		if err == nil || resources.files.ResourceIsNotFound(err) {
			return nil
		}
		return fmt.Errorf("sandbox: %s remove Session output snapshot: %w", provider, err)
	}

	result, err := execute(ctx, Command{
		Path: "tar",
		Args: []string{"-C", SessionOutputsRoot, "-cf", archivePath, "."},
	})
	if err != nil {
		_ = cleanup()
		return nil, fmt.Errorf("sandbox: %s archive Session outputs: %w", provider, err)
	}
	if result == nil || result.ExitCode != 0 {
		_ = cleanup()
		message := "tar exited without a result"
		if result != nil {
			message = fmt.Sprintf("tar exited with code %d", result.ExitCode)
			if stderr := strings.TrimSpace(string(result.Stderr)); stderr != "" {
				message += ": " + stderr
			}
		}
		return nil, Permanent(fmt.Errorf(
			"sandbox: %s cannot archive Session outputs: %s",
			provider,
			message,
		))
	}
	info, err := resources.files.ResourceStat(ctx, archivePath)
	if err != nil {
		_ = cleanup()
		return nil, fmt.Errorf("sandbox: %s inspect Session output snapshot: %w", provider, err)
	}
	if !info.Regular {
		_ = cleanup()
		return nil, Permanent(fmt.Errorf(
			"sandbox: %s Session output snapshot is not a regular file",
			provider,
		))
	}
	reader, err := resources.files.ResourceOpen(ctx, archivePath)
	if err != nil {
		_ = cleanup()
		return nil, fmt.Errorf("sandbox: %s open Session output snapshot: %w", provider, err)
	}
	return &remoteSessionOutputArchive{reader: reader, cleanup: cleanup}, nil
}

type remoteSessionOutputArchive struct {
	reader  io.ReadCloser
	cleanup func() error
	once    sync.Once
	err     error
}

func (a *remoteSessionOutputArchive) Read(buffer []byte) (int, error) {
	return a.reader.Read(buffer)
}

func (a *remoteSessionOutputArchive) Close() error {
	a.once.Do(func() {
		a.err = errors.Join(a.reader.Close(), a.cleanup())
	})
	return a.err
}
