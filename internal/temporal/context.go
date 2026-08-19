package temporal

import (
	"fmt"

	"github.com/yanpgwang/mango/internal/agentruntime"
	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/model"
)

type preparedRequestContext struct {
	Request     model.Request
	Measurement model.ContextMeasurement
	Limits      model.ContextLimits
	Projection  domain.ContextProjection
}

// prepareRequestContext is the single request admission path used at turn
// preparation and immediately before every provider call. An override is a
// direct message budget used only by deterministic tests; production derives
// the budget from the Catwalk model profile.
func prepareRequestContext(
	request model.Request,
	forceCompact bool,
	overrideMessageBudget int,
) (preparedRequestContext, error) {
	result := preparedRequestContext{
		Request: request,
		Limits:  model.RequestContextLimits(request),
	}
	result.Measurement = model.MeasureRequestContext(request)
	messageTokens := domain.EstimateMessagesTokens(request.Messages)
	result.Projection = domain.ContextProjection{
		OriginalEstimatedTokens:  messageTokens,
		ProjectedEstimatedTokens: messageTokens,
	}

	shouldCompact := forceCompact ||
		result.Measurement.Tokens >= result.Limits.CompactThreshold ||
		result.Measurement.Tokens+result.Limits.GrowthReserve >= result.Limits.InputLimit
	messageBudget := result.Limits.CompactThreshold - requestContextOverhead(request)
	if overrideMessageBudget > 0 {
		messageBudget = overrideMessageBudget
		shouldCompact = messageTokens > messageBudget
	}
	if !shouldCompact {
		if result.Measurement.Tokens >= result.Limits.InputLimit {
			return preparedRequestContext{}, contextLimitError(result)
		}
		return result, nil
	}

	if forceCompact {
		aggressiveBudget := messageTokens * 3 / 4
		if aggressiveBudget > 0 && aggressiveBudget < messageBudget {
			messageBudget = aggressiveBudget
		}
	}
	if messageBudget < 1 {
		messageBudget = 1
	}
	fullMessages := request.Messages
	compactionBudget := messageBudget
	if messageTokens > messageBudget {
		compactionBudget -= agentruntime.RuntimeSkillReattachmentBudget(fullMessages)
		if compactionBudget < 1 {
			compactionBudget = 1
		}
	}
	result.Request.Messages, result.Projection = domain.CompactMessages(
		fullMessages,
		compactionBudget,
	)
	if result.Projection.Compacted {
		result.Request.Messages = agentruntime.ReattachRuntimeSkillInjections(
			fullMessages,
			result.Request.Messages,
		)
		result.Projection.ProjectedEstimatedTokens =
			domain.EstimateMessagesTokens(result.Request.Messages)
		result.Measurement = model.MeasureRequestContext(result.Request)
	}

	if result.Measurement.Tokens >= result.Limits.InputLimit {
		return preparedRequestContext{}, contextLimitError(result)
	}
	if forceCompact && !result.Projection.Compacted {
		return preparedRequestContext{}, fmt.Errorf(
			"model request exceeded the provider context window and no older context can be compacted",
		)
	}
	return result, nil
}

func contextLimitError(context preparedRequestContext) error {
	return fmt.Errorf(
		"model request context is too large: measured %d tokens, input limit %d tokens for a %d-token window",
		context.Measurement.Tokens,
		context.Limits.InputLimit,
		context.Limits.ContextWindow,
	)
}
