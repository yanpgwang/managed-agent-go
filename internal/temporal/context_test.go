package temporal

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
)

func TestPrepareRequestContextLeavesPredictiveGrowthHeadroom(t *testing.T) {
	request := model.Request{
		Model: "unknown-model",
		Messages: []domain.Message{
			{
				Role: domain.RoleUser,
				Content: []domain.ContentBlock{{
					Type: "text", Text: strings.Repeat("old-user ", 18_000),
				}},
			},
			{
				Role: domain.RoleAssistant,
				Content: []domain.ContentBlock{{
					Type: "text", Text: strings.Repeat("old-assistant ", 18_000),
				}},
			},
			{
				Role: domain.RoleUser,
				Content: []domain.ContentBlock{{
					Type: "text", Text: strings.Repeat("current ", 18_000),
				}},
			},
		},
	}

	prepared, err := prepareRequestContext(request, false, 0)
	require.NoError(t, err)
	require.True(t, prepared.Projection.Compacted)
	require.LessOrEqual(t,
		prepared.Measurement.Tokens+prepared.Limits.GrowthReserve,
		prepared.Limits.InputLimit,
	)
}
