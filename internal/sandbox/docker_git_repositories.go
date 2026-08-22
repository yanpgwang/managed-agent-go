package sandbox

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"path"
	"time"

	"github.com/moby/moby/client"
)

func (s *dockerSandbox) initGitRepositories() {
	if s == nil || s.provider == nil {
		return
	}
	s.repositories = newCommandGitRepositories(
		DockerProviderName,
		s.Exec,
		s.uploadGitRepositoryArchive,
	)
}

func (s *dockerSandbox) HasGitRepository(
	ctx context.Context,
	mount GitRepositoryMount,
) (bool, error) {
	if s.repositories == nil {
		return false, fmt.Errorf("sandbox: Docker Git repository data plane is unavailable")
	}
	return s.repositories.HasGitRepository(ctx, mount)
}

func (s *dockerSandbox) ImportGitRepository(
	ctx context.Context,
	mount GitRepositoryMount,
	content io.Reader,
) error {
	if s.repositories == nil {
		return fmt.Errorf("sandbox: Docker Git repository data plane is unavailable")
	}
	return s.repositories.ImportGitRepository(ctx, mount, content)
}

func (s *dockerSandbox) RemoveGitRepository(
	ctx context.Context,
	runtimePath string,
	identity string,
) error {
	if s.repositories == nil {
		return nil
	}
	return s.repositories.RemoveGitRepository(ctx, runtimePath, identity)
}

func (s *dockerSandbox) uploadGitRepositoryArchive(
	ctx context.Context,
	destination string,
	content io.Reader,
	size int64,
) error {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		tarWriter := tar.NewWriter(writer)
		err := tarWriter.WriteHeader(&tar.Header{
			Name: path.Base(destination), Mode: 0o600, Size: size,
			Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0).UTC(),
		})
		if err == nil {
			var copied int64
			copied, err = io.CopyN(tarWriter, content, size)
			if err == nil && copied != size {
				err = io.ErrUnexpectedEOF
			}
		}
		if closeErr := tarWriter.Close(); err == nil {
			err = closeErr
		}
		_ = writer.CloseWithError(err)
		done <- err
	}()
	_, copyErr := s.provider.engine.CopyToContainer(
		ctx,
		s.cid,
		client.CopyToContainerOptions{
			DestinationPath: path.Dir(destination),
			Content:         reader,
		},
	)
	if copyErr != nil {
		_ = reader.CloseWithError(copyErr)
	} else {
		_ = reader.Close()
	}
	archiveErr := <-done
	if copyErr != nil {
		return fmt.Errorf("sandbox: Docker Engine copy repository archive: %w", copyErr)
	}
	if archiveErr != nil {
		return fmt.Errorf("sandbox: build Docker repository transfer: %w", archiveErr)
	}
	return nil
}

var _ GitRepositorySandbox = (*dockerSandbox)(nil)
