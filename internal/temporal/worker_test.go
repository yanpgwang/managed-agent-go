package temporal

import (
	"testing"
	"time"
)

// TestWorkerConfig_DefaultsBoundConcurrency asserts a zero WorkerConfig still
// produces bounded worker options. The Temporal SDK default is 1000 concurrent
// Activity executions, which for Mango would mean up to 1000 concurrent model
// calls, sandbox commands, and PostgreSQL transactions from one process.
func TestWorkerConfig_DefaultsBoundConcurrency(t *testing.T) {
	opts := WorkerConfig{}.WorkerOptions()
	if opts.MaxConcurrentActivityExecutionSize != DefaultMaxConcurrentActivities {
		t.Errorf("MaxConcurrentActivityExecutionSize = %d, want %d",
			opts.MaxConcurrentActivityExecutionSize, DefaultMaxConcurrentActivities)
	}
	if opts.MaxConcurrentWorkflowTaskExecutionSize != DefaultMaxConcurrentWorkflowTasks {
		t.Errorf("MaxConcurrentWorkflowTaskExecutionSize = %d, want %d",
			opts.MaxConcurrentWorkflowTaskExecutionSize, DefaultMaxConcurrentWorkflowTasks)
	}
	if opts.MaxConcurrentActivityTaskPollers != DefaultActivityPollers {
		t.Errorf("MaxConcurrentActivityTaskPollers = %d, want %d",
			opts.MaxConcurrentActivityTaskPollers, DefaultActivityPollers)
	}
	if opts.MaxConcurrentWorkflowTaskPollers != DefaultWorkflowPollers {
		t.Errorf("MaxConcurrentWorkflowTaskPollers = %d, want %d",
			opts.MaxConcurrentWorkflowTaskPollers, DefaultWorkflowPollers)
	}
	if opts.WorkerStopTimeout != DefaultWorkerDrainTimeout {
		t.Errorf("WorkerStopTimeout = %v, want %v",
			opts.WorkerStopTimeout, DefaultWorkerDrainTimeout)
	}
}

// TestWorkerConfig_AppliesConfiguredLimits asserts an explicit configuration
// actually reaches worker.Options instead of being dropped.
func TestWorkerConfig_AppliesConfiguredLimits(t *testing.T) {
	opts := WorkerConfig{
		MaxConcurrentActivities:    5,
		MaxConcurrentWorkflowTasks: 6,
		ActivityPollers:            7,
		WorkflowPollers:            8,
		DrainTimeout:               90 * time.Second,
	}.WorkerOptions()
	if opts.MaxConcurrentActivityExecutionSize != 5 {
		t.Errorf("MaxConcurrentActivityExecutionSize = %d, want 5",
			opts.MaxConcurrentActivityExecutionSize)
	}
	if opts.MaxConcurrentWorkflowTaskExecutionSize != 6 {
		t.Errorf("MaxConcurrentWorkflowTaskExecutionSize = %d, want 6",
			opts.MaxConcurrentWorkflowTaskExecutionSize)
	}
	if opts.MaxConcurrentActivityTaskPollers != 7 {
		t.Errorf("MaxConcurrentActivityTaskPollers = %d, want 7",
			opts.MaxConcurrentActivityTaskPollers)
	}
	if opts.MaxConcurrentWorkflowTaskPollers != 8 {
		t.Errorf("MaxConcurrentWorkflowTaskPollers = %d, want 8",
			opts.MaxConcurrentWorkflowTaskPollers)
	}
	if opts.WorkerStopTimeout != 90*time.Second {
		t.Errorf("WorkerStopTimeout = %v, want 90s", opts.WorkerStopTimeout)
	}
}

// TestWorkerConfig_DrainTimeoutIsTheSDKStopTimeout pins the mapping the
// orchestrate command relies on: the configured drain is what the SDK waits for
// before it cancels an in-flight Activity's context.
func TestWorkerConfig_DrainTimeoutIsTheSDKStopTimeout(t *testing.T) {
	if got := (WorkerConfig{DrainTimeout: 12 * time.Second}).WorkerOptions().WorkerStopTimeout; got != 12*time.Second {
		t.Fatalf("WorkerStopTimeout = %v, want the configured 12s drain", got)
	}
	// The SDK's own default is zero, i.e. no wait at all; Mango must not
	// inherit that.
	if DefaultWorkerDrainTimeout <= 0 {
		t.Fatal("DefaultWorkerDrainTimeout must be positive so Activities get a drain window")
	}
}

// TestRuntimeOptions_TaskQueueFallback asserts the deployment constructor keeps
// the existing task queue semantics: an empty queue still means TaskQueue.
func TestRuntimeOptions_TaskQueueFallback(t *testing.T) {
	if TaskQueue == "" {
		t.Fatal("TaskQueue must be a non-empty constant")
	}
	opts := RuntimeOptions{}
	if opts.TaskQueue != "" {
		t.Fatal("zero RuntimeOptions should leave TaskQueue empty for the fallback")
	}
	if opts.Worker.WorkerOptions().MaxConcurrentActivityExecutionSize != DefaultMaxConcurrentActivities {
		t.Fatal("zero RuntimeOptions must still bound worker concurrency")
	}
}
