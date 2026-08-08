package app

import (
	"context"
	"log"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

const (
	defaultDeploymentPollInterval = time.Second
	defaultDeploymentClaimBatch   = 20
)

type ScheduledDeploymentRunner interface {
	ClaimDue(context.Context, int) ([]DeploymentScheduleClaim, error)
	RunScheduled(context.Context, string, time.Time) (domain.DeploymentRun, error)
}

type DeploymentReconciler struct {
	runner       ScheduledDeploymentRunner
	pollInterval time.Duration
	batchSize    int
}

func NewDeploymentReconciler(runner ScheduledDeploymentRunner) *DeploymentReconciler {
	return &DeploymentReconciler{
		runner: runner, pollInterval: defaultDeploymentPollInterval,
		batchSize: defaultDeploymentClaimBatch,
	}
}

// Run claims due cron occurrences through PostgreSQL and creates their
// Sessions. Claims are leased and scheduled run rows are unique per occurrence,
// so multiple worker replicas can run this loop safely and a crashed worker is
// retried without exposing duplicate successful Sessions.
func (r *DeploymentReconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		if err := r.reconcile(ctx); err != nil && ctx.Err() == nil {
			// A failed scan must not terminate the worker role. The leased claim
			// becomes eligible again and the next tick also retries database
			// connectivity.
			log.Printf("deployments: scheduled run reconciliation failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *DeploymentReconciler) reconcile(ctx context.Context) error {
	claims, err := r.runner.ClaimDue(ctx, r.batchSize)
	if err != nil {
		return err
	}
	var firstErr error
	for _, claim := range claims {
		if _, err := r.runner.RunScheduled(
			ctx, claim.DeploymentID, claim.ScheduledAt,
		); err != nil {
			// Claim a batch to amortize the database lock, but do not let one
			// malformed Deployment strand every later claim until its lease
			// expires. The failed occurrence remains retryable.
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
