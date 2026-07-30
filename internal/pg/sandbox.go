package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

var _ sandbox.BindingStore = (*Store)(nil)

func (s *Store) GetSandboxBinding(
	ctx context.Context,
	sessionID string,
) (sandbox.Binding, bool, error) {
	row, err := s.q.GetSandboxBinding(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return sandbox.Binding{}, false, nil
	}
	if err != nil {
		return sandbox.Binding{}, false, err
	}
	return sandboxBindingFromRow(row), true, nil
}

// PutSandboxBinding atomically elects the first provider reference for a
// session. A competing worker receives the already-committed winner and can
// tear down its losing resource without overwriting durable ownership.
func (s *Store) PutSandboxBinding(
	ctx context.Context,
	binding sandbox.Binding,
) (sandbox.Binding, error) {
	now := tsUTC(s.clock.Now())
	row, err := s.q.PutSandboxBinding(ctx, pgstore.PutSandboxBindingParams{
		SessionID:  binding.SessionID,
		Provider:   binding.Ref.Provider,
		ExternalID: binding.Ref.ID,
		SpecHash:   binding.SpecHash,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	if err != nil {
		return sandbox.Binding{}, err
	}
	return sandboxBindingFromRow(row), nil
}

func (s *Store) DeleteSandboxBinding(
	ctx context.Context,
	binding sandbox.Binding,
) error {
	affected, err := s.q.DeleteSandboxBinding(ctx, pgstore.DeleteSandboxBindingParams{
		SessionID:  binding.SessionID,
		Provider:   binding.Ref.Provider,
		ExternalID: binding.Ref.ID,
	})
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	current, found, err := s.GetSandboxBinding(ctx, binding.SessionID)
	if err != nil || !found {
		return err
	}
	return fmt.Errorf(
		"pg: sandbox binding for session %s changed from %s/%s to %s/%s",
		binding.SessionID,
		binding.Ref.Provider,
		binding.Ref.ID,
		current.Ref.Provider,
		current.Ref.ID,
	)
}

func sandboxBindingFromRow(row pgstore.SessionSandbox) sandbox.Binding {
	return sandbox.Binding{
		SessionID: row.SessionID,
		Ref: sandbox.Ref{
			Provider: row.Provider,
			ID:       row.ExternalID,
		},
		SpecHash: row.SpecHash,
	}
}
