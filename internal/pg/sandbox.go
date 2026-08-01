package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

var _ sandbox.BindingStore = (*Store)(nil)

func (s *Store) GetSandboxProvisioningIntent(
	ctx context.Context,
	sessionID string,
) (sandbox.ProvisioningIntent, bool, error) {
	row, err := s.q.GetSandboxProvisioningIntent(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return sandbox.ProvisioningIntent{}, false, nil
	}
	if err != nil {
		return sandbox.ProvisioningIntent{}, false, err
	}
	intent, err := sandboxProvisioningIntentFromRow(
		row.SessionID,
		row.Provider,
		row.Spec,
		row.SpecHash,
		false,
	)
	return intent, err == nil, err
}

func (s *Store) PutSandboxProvisioningIntent(
	ctx context.Context,
	intent sandbox.ProvisioningIntent,
) (sandbox.ProvisioningIntent, error) {
	spec, err := json.Marshal(intent.Spec)
	if err != nil {
		return sandbox.ProvisioningIntent{}, err
	}
	now := tsUTC(s.clock.Now())
	row, err := s.q.PutSandboxProvisioningIntent(
		ctx,
		pgstore.PutSandboxProvisioningIntentParams{
			SessionID: intent.SessionID,
			Provider:  intent.Provider,
			Spec:      spec,
			SpecHash:  intent.SpecHash,
			CreatedAt: now,
			UpdatedAt: now,
		},
	)
	if err != nil {
		return sandbox.ProvisioningIntent{}, err
	}
	return sandboxProvisioningIntentFromRow(
		row.SessionID,
		row.Provider,
		row.Spec,
		row.SpecHash,
		false,
	)
}

func (s *Store) ListSandboxProvisioningIntents(
	ctx context.Context,
	provider string,
	limit int,
) ([]sandbox.ProvisioningIntent, error) {
	if limit <= 0 {
		return []sandbox.ProvisioningIntent{}, nil
	}
	rows, err := s.q.ListSandboxProvisioningIntents(
		ctx,
		pgstore.ListSandboxProvisioningIntentsParams{
			Provider: provider,
			RowLimit: int32(limit),
		},
	)
	if err != nil {
		return nil, err
	}
	intents := make([]sandbox.ProvisioningIntent, 0, len(rows))
	for _, row := range rows {
		intent, err := sandboxProvisioningIntentFromRow(
			row.SessionID,
			row.Provider,
			row.Spec,
			row.SpecHash,
			row.DeletingAt.Valid,
		)
		if err != nil {
			return nil, err
		}
		intents = append(intents, intent)
	}
	return intents, nil
}

func (s *Store) DeleteSandboxProvisioningIntent(
	ctx context.Context,
	intent sandbox.ProvisioningIntent,
) error {
	affected, err := s.q.DeleteSandboxProvisioningIntent(
		ctx,
		pgstore.DeleteSandboxProvisioningIntentParams{
			SessionID: intent.SessionID,
			Provider:  intent.Provider,
			SpecHash:  intent.SpecHash,
		},
	)
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	current, found, err := s.GetSandboxProvisioningIntent(ctx, intent.SessionID)
	if err != nil || !found {
		return err
	}
	return fmt.Errorf(
		"pg: sandbox provisioning intent for session %s changed from %s/%s to %s/%s",
		intent.SessionID,
		intent.Provider,
		intent.SpecHash,
		current.Provider,
		current.SpecHash,
	)
}

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
	var row pgstore.SessionSandbox
	err := s.withTx(ctx, func(q *pgstore.Queries) error {
		var err error
		row, err = q.PutSandboxBinding(ctx, pgstore.PutSandboxBindingParams{
			SessionID:  binding.SessionID,
			Provider:   binding.Ref.Provider,
			ExternalID: binding.Ref.ID,
			SpecHash:   binding.SpecHash,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
		if err != nil {
			return err
		}
		return q.DeleteSandboxProvisioningIntentBySession(ctx, binding.SessionID)
	})
	if err != nil {
		return sandbox.Binding{}, err
	}
	return sandboxBindingFromRow(row), nil
}

func sandboxProvisioningIntentFromRow(
	sessionID string,
	provider string,
	rawSpec []byte,
	hash string,
	deleting bool,
) (sandbox.ProvisioningIntent, error) {
	var spec sandbox.Spec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return sandbox.ProvisioningIntent{}, fmt.Errorf(
			"pg: decode sandbox provisioning spec for session %s: %w",
			sessionID,
			err,
		)
	}
	return sandbox.ProvisioningIntent{
		SessionID: sessionID,
		Provider:  provider,
		Spec:      spec,
		SpecHash:  hash,
		Deleting:  deleting,
	}, nil
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
