package agentruntime

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCoordinatorToolSchemasMatchManagedAgentsRuntimeSurface(t *testing.T) {
	schemas := CoordinatorToolSchemas()
	require.Len(t, schemas, 2)
	require.Equal(t, ListAgentsToolName, schemas[0].Name)
	require.Equal(t, SendToAgentToolName, schemas[1].Name)
	require.Equal(t, []any{"agent_name", "message"}, schemas[1].InputSchema["required"])
}

func TestParseSendToAgentInput(t *testing.T) {
	got, err := ParseSendToAgentInput(map[string]any{
		"agent_name": " reviewer ", "message": " inspect auth ",
		"session_thread_id": " sthr_existing ",
	})
	require.NoError(t, err)
	require.Equal(t, "reviewer", got.AgentName)
	require.Equal(t, "inspect auth", got.Message)
	require.Equal(t, "sthr_existing", got.SessionThreadID)

	_, err = ParseSendToAgentInput(map[string]any{
		"agent_name": "reviewer", "message": "x", "unknown": true,
	})
	require.ErrorContains(t, err, "unknown field")
}
