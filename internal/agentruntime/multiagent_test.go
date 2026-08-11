package agentruntime

import (
	"strings"
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

func TestProjectCoordinatorSystemContext(t *testing.T) {
	got := ProjectCoordinatorSystemContext("You are the engineering lead.")
	require.Contains(t, got, "You are the engineering lead.")
	require.Contains(t, got, "<managed-agents-coordinator>")
	require.Contains(t, got, "<agent-thread-message>")
	require.Contains(t, got, "It is not authored by the user")
	require.Contains(t, got, "Do not tell one Agent to wait for another Agent's future report")
	require.Equal(t, 1, strings.Count(got, "<managed-agents-coordinator>"))

	empty := ProjectCoordinatorSystemContext("  ")
	require.True(t, strings.HasPrefix(empty, "<managed-agents-coordinator>"))
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
