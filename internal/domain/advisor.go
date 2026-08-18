package domain

// AdvisorConsultation is the normalized result of one Mango-managed advisor
// inference. Workflow-assigned identifiers make persistence idempotent across
// Temporal Activity retries while the ordinary client-tool result remains in
// the executor's private provider transcript.
type AdvisorConsultation struct {
	ThreadID        string
	UsageRequestID  string
	LifecycleIDs    []string
	Model           string
	UsageModel      string
	Usage           TokenUsage
	UsageKnown      bool
	StopReason      string
	PublicContent   []any
	AdviceDelivered bool
}
