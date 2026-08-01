package temporal

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

const lifecycleDrainDelay = 100 * time.Millisecond

// DeletionStore is the worker-side view of fenced Session deletion state.
// PostgreSQL remains authoritative; the reconciler only resumes the external
// cleanup and finalization that a crashed API process may have left unfinished.
type DeletionStore interface {
	ListDeletingSessionIDs(ctx context.Context, limit int) ([]string, error)
	FinalizeSessionDeletion(ctx context.Context, sessionID string) error
}

type SessionTerminator interface {
	TerminateSession(ctx context.Context, sessionID string) error
}

type SandboxProvisioningReconciler interface {
	ReconcileProvisioning(ctx context.Context, limit int) (int, error)
}

type LifecycleReconcilerConfig struct {
	// PollInterval is the idle delay between scans. A scan that completes work
	// immediately repeats so a backlog drains without one interval per batch.
	PollInterval time.Duration
	// BatchSize bounds both provisioning-intent and deleting-session scans.
	BatchSize int
	// AttemptTimeout prevents one unavailable provider from starving the rest of
	// the batch. The deterministic cleanup Workflow continues after this local
	// wait expires and a later scan joins it.
	AttemptTimeout time.Duration
}

func (c LifecycleReconcilerConfig) withDefaults() LifecycleReconcilerConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
	if c.AttemptTimeout <= 0 {
		c.AttemptTimeout = 30 * time.Second
	}
	return c
}

// LifecycleReconcileResult reports successfully discharged durable obligations.
type LifecycleReconcileResult struct {
	Provisioning int
	Deletions    int
}

func (r LifecycleReconcileResult) total() int {
	return r.Provisioning + r.Deletions
}

// LifecycleReconciler closes two process-crash windows:
//
//   - provider resource creation after a durable provisioning intent but before
//     the session_sandboxes binding commit;
//   - Session deletion after the PostgreSQL fence but before cleanup/finalize.
//
// Multiple workers may run it. Provider session identity, insert-if-absent
// bindings, deterministic Temporal Workflow IDs, and idempotent finalization
// make duplicate attempts safe.
type LifecycleReconciler struct {
	store      DeletionStore
	terminator SessionTerminator
	sandboxes  SandboxProvisioningReconciler
	cfg        LifecycleReconcilerConfig
}

func NewLifecycleReconciler(
	store DeletionStore,
	terminator SessionTerminator,
	sandboxes SandboxProvisioningReconciler,
	cfg LifecycleReconcilerConfig,
) *LifecycleReconciler {
	return &LifecycleReconciler{
		store:      store,
		terminator: terminator,
		sandboxes:  sandboxes,
		cfg:        cfg.withDefaults(),
	}
}

func (r *LifecycleReconciler) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}

		result, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("lifecycle reconciler: cycle error: %v", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wait := r.nextDelay(result)
		timer.Reset(wait)
	}
}

func (r *LifecycleReconciler) nextDelay(result LifecycleReconcileResult) time.Duration {
	wait := r.cfg.PollInterval
	if result.total() > 0 && wait > lifecycleDrainDelay {
		wait = lifecycleDrainDelay
	}
	return wait
}

func (r *LifecycleReconciler) RunOnce(
	ctx context.Context,
) (LifecycleReconcileResult, error) {
	var (
		result LifecycleReconcileResult
		errs   []error
	)

	if r.sandboxes != nil {
		attemptCtx, cancel := context.WithTimeout(ctx, r.cfg.AttemptTimeout)
		completed, err := r.sandboxes.ReconcileProvisioning(
			attemptCtx,
			r.cfg.BatchSize,
		)
		cancel()
		result.Provisioning = completed
		if err != nil && ctx.Err() == nil {
			errs = append(errs, fmt.Errorf("reconcile sandbox provisioning: %w", err))
		}
	}

	if r.store == nil || r.terminator == nil {
		return result, errors.Join(errs...)
	}
	sessionIDs, err := r.store.ListDeletingSessionIDs(ctx, r.cfg.BatchSize)
	if err != nil {
		errs = append(errs, fmt.Errorf("list deleting sessions: %w", err))
		return result, errors.Join(errs...)
	}
	for _, sessionID := range sessionIDs {
		if ctx.Err() != nil {
			break
		}
		attemptCtx, cancel := context.WithTimeout(ctx, r.cfg.AttemptTimeout)
		err := r.terminator.TerminateSession(attemptCtx, sessionID)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf(
				"resume cleanup for session %s: %w",
				sessionID,
				err,
			))
			continue
		}

		finalizeCtx, cancel := context.WithTimeout(ctx, r.cfg.AttemptTimeout)
		err = r.store.FinalizeSessionDeletion(finalizeCtx, sessionID)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf(
				"finalize deletion for session %s: %w",
				sessionID,
				err,
			))
			continue
		}
		result.Deletions++
	}
	return result, errors.Join(errs...)
}
