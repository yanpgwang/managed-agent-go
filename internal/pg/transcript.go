package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

// LoadProviderTranscript returns the immutable committed turn deltas in model
// continuation order. The caller decides whether the represented trigger set is
// complete enough to use instead of the legacy public-event projection.
func (s *Store) LoadProviderTranscript(
	ctx context.Context,
	sessionID string,
) (domain.ProviderTranscript, error) {
	threadID, err := s.q.GetPrimarySessionThreadID(ctx, sessionID)
	if err != nil {
		return domain.ProviderTranscript{}, err
	}
	return s.LoadThreadProviderTranscript(ctx, sessionID, threadID)
}

// LoadThreadProviderTranscript returns only the provider-native continuation
// history owned by one Thread. Public Session ordering never participates in
// private model context reconstruction.
func (s *Store) LoadThreadProviderTranscript(
	ctx context.Context,
	sessionID string,
	threadID string,
) (domain.ProviderTranscript, error) {
	rows, err := s.q.ListProviderTranscriptTurns(
		ctx,
		pgstore.ListProviderTranscriptTurnsParams{
			SessionID: sessionID,
			ThreadID:  threadID,
		},
	)
	if err != nil {
		return domain.ProviderTranscript{}, err
	}
	out := domain.ProviderTranscript{
		TriggerEventIDs: make([]string, 0, len(rows)),
	}
	publicIDs := make(map[string]struct{})
	representedIDs := make(map[string]struct{})
	for _, row := range rows {
		var represented []string
		if err := json.Unmarshal(row.RepresentedEventIds, &represented); err != nil {
			return domain.ProviderTranscript{}, fmt.Errorf(
				"pg: decode represented provider events for turn %s: %w",
				row.TriggerEventID,
				err,
			)
		}
		for _, eventID := range represented {
			if eventID == "" {
				return domain.ProviderTranscript{}, fmt.Errorf(
					"pg: empty represented provider event in turn %s",
					row.TriggerEventID,
				)
			}
			if _, duplicate := representedIDs[eventID]; duplicate {
				return domain.ProviderTranscript{}, fmt.Errorf(
					"pg: duplicate represented provider event %s",
					eventID,
				)
			}
			representedIDs[eventID] = struct{}{}
			out.TriggerEventIDs = append(out.TriggerEventIDs, eventID)
		}
		var messages []domain.Message
		if err := json.Unmarshal(row.Messages, &messages); err != nil {
			return domain.ProviderTranscript{}, fmt.Errorf(
				"pg: decode provider transcript turn %s: %w",
				row.TriggerEventID,
				err,
			)
		}
		var mappings []domain.ProviderToolUseMapping
		if err := json.Unmarshal(row.ToolUseMappings, &mappings); err != nil {
			return domain.ProviderTranscript{}, fmt.Errorf(
				"pg: decode provider tool mappings for turn %s: %w",
				row.TriggerEventID,
				err,
			)
		}
		for _, mapping := range mappings {
			if mapping.PublicEventID == "" || mapping.ProviderToolUseID == "" {
				return domain.ProviderTranscript{}, fmt.Errorf(
					"pg: invalid provider tool mapping in turn %s",
					row.TriggerEventID,
				)
			}
			if _, duplicate := publicIDs[mapping.PublicEventID]; duplicate {
				return domain.ProviderTranscript{}, fmt.Errorf(
					"pg: duplicate provider mapping for public event %s",
					mapping.PublicEventID,
				)
			}
			publicIDs[mapping.PublicEventID] = struct{}{}
		}
		out.Messages = appendProviderMessages(out.Messages, messages)
		out.ToolUseMappings = append(out.ToolUseMappings, mappings...)
	}
	return out, nil
}

func appendProviderMessages(
	base []domain.Message,
	added []domain.Message,
) []domain.Message {
	for _, message := range added {
		if len(message.Content) == 0 {
			continue
		}
		if n := len(base); n > 0 && base[n-1].Role == message.Role {
			base[n-1].Content = append(base[n-1].Content, message.Content...)
			if message.ContextUsage != nil {
				anchor := *message.ContextUsage
				base[n-1].ContextUsage = &anchor
			}
			continue
		}
		base = append(base, message)
	}
	return base
}

// closeInterruptedProviderTranscript pairs every client tool_use that has no
// result when the PostgreSQL completion lock, rather than the Workflow watcher,
// observes a winning interrupt. Provider server-tool blocks are not client
// tool_use blocks and are therefore left unchanged.
func closeInterruptedProviderTranscript(
	messages []domain.Message,
) []domain.Message {
	if messages == nil {
		// nil means this completion did not opt into private transcript
		// persistence. Preserve that distinction from an explicitly empty delta
		// so the interrupt path cannot create a false represented-event row.
		return nil
	}
	cloned := make([]domain.Message, len(messages))
	for index, message := range messages {
		cloned[index] = domain.Message{
			Role: message.Role,
			Content: append(
				[]domain.ContentBlock(nil),
				message.Content...,
			),
		}
		if message.ContextUsage != nil {
			anchor := *message.ContextUsage
			cloned[index].ContextUsage = &anchor
		}
	}
	answered := make(map[string]struct{})
	for _, message := range cloned {
		for _, block := range message.Content {
			if block.Type == "tool_result" && block.ToolResultFor != "" {
				answered[block.ToolResultFor] = struct{}{}
			}
		}
	}
	var synthetic []domain.ContentBlock
	for _, message := range cloned {
		for _, block := range message.Content {
			if block.Type != "tool_use" || block.ToolUseID == "" {
				continue
			}
			if _, ok := answered[block.ToolUseID]; ok {
				continue
			}
			answered[block.ToolUseID] = struct{}{}
			synthetic = append(synthetic, domain.ContentBlock{
				Type:          "tool_result",
				ToolResultFor: block.ToolUseID,
				Text:          "Tool execution was interrupted before a result was committed.",
				IsError:       true,
			})
		}
	}
	if len(synthetic) == 0 {
		return cloned
	}
	return appendProviderMessages(cloned, []domain.Message{{
		Role: domain.RoleUser, Content: synthetic,
	}})
}

func retainCommittedProviderMappings(
	mappings []domain.ProviderToolUseMapping,
	drafts []domain.EventDraft,
) []domain.ProviderToolUseMapping {
	committed := make(map[string]struct{})
	for _, draft := range drafts {
		switch draft.Type {
		case domain.EvAgentToolUse, domain.EvAgentCustomToolUse,
			domain.EvAgentMcpToolUse:
			if draft.ID != "" {
				committed[draft.ID] = struct{}{}
			}
		}
	}
	out := make([]domain.ProviderToolUseMapping, 0, len(mappings))
	for _, mapping := range mappings {
		if _, ok := committed[mapping.PublicEventID]; ok {
			out = append(out, mapping)
		}
	}
	return out
}
