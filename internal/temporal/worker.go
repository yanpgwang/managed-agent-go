package temporal

import (
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// Worker concurrency and drain defaults. The Temporal SDK's own defaults allow
// 1000 concurrent Activity executions per worker, which for Mango means up to
// 1000 concurrent model calls, sandbox commands, and PostgreSQL transactions
// from a single process. These bounds are sized so one worker's fan-out stays
// within the connection-pool and sandbox budgets it actually has.
const (
	DefaultMaxConcurrentActivities    = 32
	DefaultMaxConcurrentWorkflowTasks = 32
	DefaultActivityPollers            = 2
	DefaultWorkflowPollers            = 2
	DefaultWorkerDrainTimeout         = 30 * time.Second
)

// WorkerConfig bounds a Temporal worker's concurrency, polling, and shutdown
// drain. A zero value selects the defaults above.
//
// It deliberately covers only capacity and shutdown. Activity retry policies,
// timeouts, and task queue names are orchestration semantics and stay where
// they are defined.
type WorkerConfig struct {
	// MaxConcurrentActivities caps simultaneously executing Activities.
	MaxConcurrentActivities int
	// MaxConcurrentWorkflowTasks caps simultaneously executing Workflow tasks.
	MaxConcurrentWorkflowTasks int
	// ActivityPollers is the number of Activity task pollers.
	ActivityPollers int
	// WorkflowPollers is the number of Workflow task pollers.
	WorkflowPollers int
	// DrainTimeout is how long worker.Stop waits for in-flight Activities to
	// finish before their contexts are cancelled. The SDK default is zero, so
	// without this an in-flight tool call is cut off the moment the process
	// receives SIGTERM.
	DrainTimeout time.Duration
}

func (c WorkerConfig) withDefaults() WorkerConfig {
	if c.MaxConcurrentActivities <= 0 {
		c.MaxConcurrentActivities = DefaultMaxConcurrentActivities
	}
	if c.MaxConcurrentWorkflowTasks <= 0 {
		c.MaxConcurrentWorkflowTasks = DefaultMaxConcurrentWorkflowTasks
	}
	if c.ActivityPollers <= 0 {
		c.ActivityPollers = DefaultActivityPollers
	}
	if c.WorkflowPollers <= 0 {
		c.WorkflowPollers = DefaultWorkflowPollers
	}
	if c.DrainTimeout <= 0 {
		c.DrainTimeout = DefaultWorkerDrainTimeout
	}
	return c
}

// WorkerOptions renders a WorkerConfig as Temporal SDK worker options. It is
// exported so a deployment can assert the bounds a config actually produces.
func (c WorkerConfig) WorkerOptions() worker.Options {
	resolved := c.withDefaults()
	return worker.Options{
		MaxConcurrentActivityExecutionSize:     resolved.MaxConcurrentActivities,
		MaxConcurrentWorkflowTaskExecutionSize: resolved.MaxConcurrentWorkflowTasks,
		MaxConcurrentActivityTaskPollers:       resolved.ActivityPollers,
		MaxConcurrentWorkflowTaskPollers:       resolved.WorkflowPollers,
		WorkerStopTimeout:                      resolved.DrainTimeout,
	}
}

// NewWorker builds a Temporal worker that serves the SessionWorkflow and its
// Activities on the session task queue. The caller runs it (worker.Run) and owns
// the client's lifecycle.
//
// Activities are registered under stable names so a Go method rename cannot
// silently break workflow replay.
func NewWorker(c client.Client, acts *Activities) worker.Worker {
	return NewWorkerOnTaskQueue(c, acts, TaskQueue)
}

// NewWorkerOnTaskQueue builds the worker on an explicit task queue. An optional
// WorkerConfig supplies concurrency, poller, and drain bounds; omitting it uses
// the package defaults.
func NewWorkerOnTaskQueue(
	c client.Client,
	acts *Activities,
	taskQueue string,
	configs ...WorkerConfig,
) worker.Worker {
	if taskQueue == "" {
		taskQueue = TaskQueue
	}
	var cfg WorkerConfig
	if len(configs) > 0 {
		cfg = configs[len(configs)-1]
	}
	w := worker.New(c, taskQueue, cfg.WorkerOptions())
	w.RegisterWorkflow(SessionWorkflow)
	w.RegisterWorkflowWithOptions(
		SandboxCleanupWorkflow,
		workflow.RegisterOptions{Name: SandboxCleanupWorkflowType},
	)
	w.RegisterActivityWithOptions(acts.LoadEvents, activity.RegisterOptions{Name: ActivityLoadEvents})
	w.RegisterActivityWithOptions(acts.LoadInterrupt, activity.RegisterOptions{Name: ActivityLoadInterrupt})
	w.RegisterActivityWithOptions(acts.LoadPendingActions, activity.RegisterOptions{Name: ActivityLoadPendingActions})
	w.RegisterActivityWithOptions(acts.PrepareTurn, activity.RegisterOptions{Name: ActivityPrepareTurn})
	w.RegisterActivityWithOptions(acts.StartModelRequest, activity.RegisterOptions{Name: ActivityStartModelRequest})
	w.RegisterActivityWithOptions(acts.AppendWorkflowEvents, activity.RegisterOptions{Name: ActivityAppendWorkflowEvents})
	w.RegisterActivityWithOptions(acts.CallModel, activity.RegisterOptions{Name: ActivityCallModel})
	w.RegisterActivityWithOptions(acts.EvaluateOutcome, activity.RegisterOptions{Name: ActivityEvaluateOutcome})
	w.RegisterActivityWithOptions(acts.ExecuteTool, activity.RegisterOptions{Name: ActivityExecuteTool})
	w.RegisterActivityWithOptions(acts.CompleteWorkflowTurn, activity.RegisterOptions{Name: ActivityCompleteWorkflowTurn})
	w.RegisterActivityWithOptions(acts.ReleaseSandbox, activity.RegisterOptions{Name: ActivityReleaseSandbox})
	return w
}
