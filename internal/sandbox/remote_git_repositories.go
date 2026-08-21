package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
)

func newRemoteGitRepositories(
	provider string,
	resources *remoteFileResources,
	execute repositoryExecute,
) *commandGitRepositories {
	return newCommandGitRepositories(
		provider,
		execute,
		func(ctx context.Context, destination string, content io.Reader, size int64) error {
			if resources == nil || resources.files == nil {
				return errors.New("sandbox: remote Git repository file data plane is required")
			}
			if err := resources.files.ResourceUpload(
				ctx, destination, content, remoteFilePermissions{Mode: 0o600},
			); err != nil {
				return err
			}
			info, err := resources.files.ResourceStat(ctx, destination)
			if err != nil {
				return err
			}
			if !info.Regular || info.SizeBytes != size {
				return fmt.Errorf("sandbox: provider did not persist the complete Git repository archive")
			}
			return nil
		},
	)
}
