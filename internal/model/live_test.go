package model

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// TestAnthropic_LiveMessagesConformance verifies the smallest external model
// contract the platform requires: authenticated streaming POST /v1/messages
// with a non-empty text response. It is opt-in because it uses credentials,
// reaches the network, and may incur provider cost.
func TestAnthropic_LiveMessagesConformance(t *testing.T) {
	if os.Getenv("MANAGED_AGENT_TEST_LIVE_MODEL") != "1" {
		t.Skip("set MANAGED_AGENT_TEST_LIVE_MODEL=1 to run the live model conformance test")
	}
	modelID := strings.TrimSpace(os.Getenv("MANAGED_AGENT_MODEL_ID"))
	if modelID == "" {
		t.Fatal("MANAGED_AGENT_MODEL_ID is required for the live model conformance test")
	}
	client, configured, err := AnthropicFromEnv()
	if err != nil {
		t.Fatalf("configure live model: %v", err)
	}
	if !configured {
		t.Fatal("MANAGED_AGENT_MODEL_BASE_URL and MANAGED_AGENT_MODEL_API_KEY are required for the live model conformance test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var streamed strings.Builder
	response, err := client.CreateMessageStream(ctx, Request{
		Model: modelID,
		Messages: []domain.Message{{
			Role: domain.RoleUser,
			Content: []domain.ContentBlock{{
				Type: "text",
				Text: "Reply with a short plain-text acknowledgement.",
			}},
		}},
		MaxTokens: 64,
	}, func(_ int, text string) {
		streamed.WriteString(text)
	})
	if err != nil {
		t.Fatalf("live Messages API request failed: %v", err)
	}

	var responseText strings.Builder
	for _, block := range response.Content {
		if block.Type == "text" {
			responseText.WriteString(block.Text)
		}
	}
	if strings.TrimSpace(responseText.String()) == "" {
		t.Fatalf("live Messages API returned no text content (stop_reason=%q)", response.StopReason)
	}
	if strings.TrimSpace(streamed.String()) == "" {
		t.Fatal("live Messages API returned text but no streaming deltas")
	}
	if streamed.String() != responseText.String() {
		t.Fatalf(
			"streamed text does not match the final response (streamed_bytes=%d final_bytes=%d)",
			streamed.Len(),
			responseText.Len(),
		)
	}
	if response.StopReason == "" {
		t.Fatal("live Messages API returned an empty stop_reason")
	}
}
