package agentruntime

import (
	"fmt"
	"strings"

	"github.com/yanpgwang/managed-agent-go/internal/model"
)

const (
	ListAgentsToolName  = "list_agents"
	SendToAgentToolName = "send_to_agent"
)

// CoordinatorToolSchemas are the private model-facing tools automatically
// attached to a Managed Agents coordinator. They are runtime capabilities, not
// entries in the persisted Agent toolset.
func CoordinatorToolSchemas() []model.ToolSchema {
	return []model.ToolSchema{
		{
			Name: ListAgentsToolName,
			Description: "List the agents this coordinator can delegate to and the " +
				"persistent session threads it has already started.",
			InputSchema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		},
		{
			Name: SendToAgentToolName,
			Description: "Asynchronously send a self-contained task to a roster agent, " +
				"or send a follow-up to one of its existing persistent session threads. " +
				"The agent's report arrives in a later turn.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_name": map[string]any{
						"type":        "string",
						"description": "Unique name of the roster agent.",
					},
					"message": map[string]any{
						"type":        "string",
						"description": "Self-contained task or follow-up message.",
					},
					"session_thread_id": map[string]any{
						"type":        "string",
						"description": "Existing child session thread to resume. Omit to start a new thread.",
					},
				},
				"required":             []any{"agent_name", "message"},
				"additionalProperties": false,
			},
		},
	}
}

type SendToAgentInput struct {
	AgentName       string
	Message         string
	SessionThreadID string
}

func ParseSendToAgentInput(input map[string]any) (SendToAgentInput, error) {
	if input == nil {
		return SendToAgentInput{}, fmt.Errorf("send_to_agent input is required")
	}
	for key := range input {
		switch key {
		case "agent_name", "message", "session_thread_id":
		default:
			return SendToAgentInput{}, fmt.Errorf("send_to_agent input contains unknown field %q", key)
		}
	}
	agentName, _ := input["agent_name"].(string)
	message, _ := input["message"].(string)
	threadID, _ := input["session_thread_id"].(string)
	agentName = strings.TrimSpace(agentName)
	message = strings.TrimSpace(message)
	threadID = strings.TrimSpace(threadID)
	if agentName == "" {
		return SendToAgentInput{}, fmt.Errorf("send_to_agent agent_name is required")
	}
	if message == "" {
		return SendToAgentInput{}, fmt.Errorf("send_to_agent message is required")
	}
	return SendToAgentInput{
		AgentName: agentName, Message: message, SessionThreadID: threadID,
	}, nil
}

func IsCoordinatorTool(name string) bool {
	return name == ListAgentsToolName || name == SendToAgentToolName
}
