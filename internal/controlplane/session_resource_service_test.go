package controlplane

import (
	"context"
	"errors"
	"testing"

	"github.com/yanpgwang/mango/internal/app"
	"github.com/yanpgwang/mango/internal/domain"
)

type aggregateLimitFileRepository struct{}

func (aggregateLimitFileRepository) BeginUpload(context.Context, domain.File) error {
	panic("unexpected BeginUpload")
}

func (aggregateLimitFileRepository) CompleteUpload(
	context.Context,
	string,
	app.BlobInfo,
) (domain.File, error) {
	panic("unexpected CompleteUpload")
}

func (aggregateLimitFileRepository) Get(_ context.Context, id string) (domain.File, error) {
	return domain.File{
		ID: id, BlobKey: "files/" + id, State: domain.FileStateReady,
		SizeBytes: 300 << 20, ChecksumSHA256: "unused",
	}, nil
}

func (aggregateLimitFileRepository) List(
	context.Context,
	app.FileListQuery,
) (app.FileListPage, error) {
	panic("unexpected List")
}

func (aggregateLimitFileRepository) BeginDelete(
	context.Context,
	string,
) (domain.File, error) {
	panic("unexpected BeginDelete")
}

func (aggregateLimitFileRepository) RemoveIncomplete(context.Context, string) error {
	panic("unexpected RemoveIncomplete")
}

func (aggregateLimitFileRepository) ListIncomplete(context.Context) ([]domain.File, error) {
	panic("unexpected ListIncomplete")
}

func TestSessionResourcePreparationRejectsUnsupportedEnvironmentAndOverLimit(t *testing.T) {
	service := &SessionResourceService{admissionEnabled: true}
	input := app.FileSessionResourceInput{FileID: "file_source"}
	_, err := service.PrepareForSession(
		context.Background(),
		domain.Session{EnvironmentType: "self_hosted"},
		[]app.FileSessionResourceInput{input},
	)
	assertDomainErrorKind(t, err, domain.KindUnsupported)

	overLimit := make([]app.FileSessionResourceInput, MaxSessionResources+1)
	_, err = service.PrepareForSession(
		context.Background(),
		domain.Session{EnvironmentType: "cloud"},
		overLimit,
	)
	assertDomainErrorKind(t, err, domain.KindValidation)
}

func TestSessionResourcePreparationRejectsAggregateBytesBeforeCopy(t *testing.T) {
	service := &SessionResourceService{
		files: aggregateLimitFileRepository{}, admissionEnabled: true,
	}
	_, err := service.PrepareForSession(
		context.Background(),
		domain.Session{ID: "sesn_bytes", EnvironmentType: "cloud"},
		[]app.FileSessionResourceInput{
			{FileID: "file_a"},
			{FileID: "file_b"},
		},
	)
	assertDomainErrorKind(t, err, domain.KindTooLarge)
}

func TestSessionResourcePreparationRejectsProviderWithoutAdmissionCapability(t *testing.T) {
	service := &SessionResourceService{}
	_, err := service.PrepareForSession(
		context.Background(),
		domain.Session{ID: "sesn_unsupported", EnvironmentType: "cloud"},
		[]app.FileSessionResourceInput{{FileID: "file_source"}},
	)
	assertDomainErrorKind(t, err, domain.KindUnsupported)
}

func assertDomainErrorKind(t *testing.T, err error, want domain.ErrKind) {
	t.Helper()
	var domainErr *domain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Kind != want {
		t.Fatalf("error = %v, want domain kind %v", err, want)
	}
}
