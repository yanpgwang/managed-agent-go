package temporal

import (
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// NewWorker builds a Temporal worker that serves the SessionWorkflow and its
// Activities on the session task queue. The caller runs it (worker.Run) and owns
// the client's lifecycle.
//
// Activities are registered under their stable names (ActivityLoadEvents,
// ActivityRunTurn) so a Go method rename cannot silently break workflow replay.
func NewWorker(c client.Client, acts *Activities) worker.Worker {
	w := worker.New(c, TaskQueue, worker.Options{})
	w.RegisterWorkflow(SessionWorkflow)
	w.RegisterActivityWithOptions(acts.LoadEvents, activity.RegisterOptions{Name: ActivityLoadEvents})
	w.RegisterActivityWithOptions(acts.RunTurn, activity.RegisterOptions{Name: ActivityRunTurn})
	return w
}
