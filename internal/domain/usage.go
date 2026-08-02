package domain

// CacheCreationUsage is the prompt-cache creation breakdown exposed by the
// Managed Agents Session usage object.
type CacheCreationUsage struct {
	Ephemeral1hInputTokens int64
	Ephemeral5mInputTokens int64
}

// TokenUsage is cumulative model usage. Individual model responses carry a
// value with the same shape; Workflow code sums every provider round in a
// public turn and PostgreSQL applies the turn total exactly once.
type TokenUsage struct {
	CacheCreation        CacheCreationUsage
	CacheReadInputTokens int64
	InputTokens          int64
	OutputTokens         int64
	// Speed is the provider-reported inference mode for one model request. It is
	// intentionally not accumulated into Session usage; span events use it to
	// report the actual mode (which may differ from a requested fast fallback).
	Speed string
}

func (u *TokenUsage) Add(other TokenUsage) {
	u.CacheCreation.Ephemeral1hInputTokens += other.CacheCreation.Ephemeral1hInputTokens
	u.CacheCreation.Ephemeral5mInputTokens += other.CacheCreation.Ephemeral5mInputTokens
	u.CacheReadInputTokens += other.CacheReadInputTokens
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
}
