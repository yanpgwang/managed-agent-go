package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestDeploymentReconcilerContinuesAfterOneClaimFails(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("first occurrence failed")
	runner := &scheduledDeploymentRunnerFake{
		claims: []DeploymentScheduleClaim{
			{DeploymentID: "depl_first", ScheduledAt: time.Unix(1, 0)},
			{DeploymentID: "depl_second", ScheduledAt: time.Unix(2, 0)},
			{DeploymentID: "depl_third", ScheduledAt: time.Unix(3, 0)},
		},
		failures: map[string]error{"depl_first": wantErr},
	}

	err := NewDeploymentReconciler(runner).reconcile(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("reconcile error = %v, want %v", err, wantErr)
	}
	if got, want := runner.runs, []string{"depl_first", "depl_second", "depl_third"}; !equalStrings(got, want) {
		t.Fatalf("scheduled runs = %v, want %v", got, want)
	}
}

type scheduledDeploymentRunnerFake struct {
	claims   []DeploymentScheduleClaim
	failures map[string]error
	runs     []string
}

func (f *scheduledDeploymentRunnerFake) ClaimDue(
	context.Context,
	int,
) ([]DeploymentScheduleClaim, error) {
	return f.claims, nil
}

func (f *scheduledDeploymentRunnerFake) RunScheduled(
	_ context.Context,
	id string,
	_ time.Time,
) (domain.DeploymentRun, error) {
	f.runs = append(f.runs, id)
	return domain.DeploymentRun{}, f.failures[id]
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
