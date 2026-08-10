package pg

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

// CompleteThreadWorkflowTurn atomically commits one child Thread turn, its
// private provider continuation, the condensed primary-stream projection, and
// any coordinator wakeup caused by the child's report.
func (s *Store) CompleteThreadWorkflowTurn(
	ctx context.Context,
	sessionID string,
	threadID string,
	triggerEventID string,
	outputDrafts []domain.EventDraft,
	status domain.Status,
	attemptID string,
	attemptState domain.RunAttemptState,
	attemptError *string,
	pendingActionEventIDs []string,
	resolutionEventIDs []string,
	transcriptDelta []domain.Message,
	toolUseMappings []domain.ProviderToolUseMapping,
	usage domain.TokenUsage,
) (TurnCompletion, error) {
	if err := validatePendingCompletion(
		status, outputDrafts, pendingActionEventIDs,
	); err != nil {
		return TurnCompletion{}, err
	}
	var result TurnCompletion
	err := s.withPGXTx(ctx, func(tx pgx.Tx, q *pgstore.Queries) error {
		row, err := q.LockSession(ctx, sessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("session not found")
		}
		if err != nil {
			return err
		}
		if row.DeletingAt.Valid {
			return domain.Conflict("session deletion is in progress")
		}
		session, err := sessionFromLockRow(row)
		if err != nil {
			return err
		}
		thread, err := loadSessionThreadForUpdate(ctx, tx, sessionID, threadID)
		if err != nil {
			return err
		}
		if thread.ParentThreadID == nil {
			return domain.Conflict("child completion cannot target the primary Thread")
		}
		trigger, err := q.GetEvent(ctx, pgstore.GetEventParams{
			SessionID: sessionID, ID: triggerEventID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("trigger event not found")
		}
		if err != nil {
			return err
		}
		if trigger.ThreadID != threadID {
			return domain.Conflict("trigger event belongs to another Thread")
		}
		pendingResume := false
		if trigger.ProcessedAt.Valid {
			pendingResume, err = q.IsUnresolvedPendingResolution(
				ctx,
				pgstore.IsUnresolvedPendingResolutionParams{
					SessionID: sessionID, ThreadID: threadID,
					ResolvingEventID: &triggerEventID,
				},
			)
			if err != nil {
				return err
			}
		}
		if trigger.ProcessedAt.Valid && !pendingResume {
			rows, err := q.ListEventsByTurn(ctx, pgstore.ListEventsByTurnParams{
				SessionID: sessionID, TurnEventID: &triggerEventID,
			})
			if err != nil {
				return err
			}
			events, err := eventsFromRows(rows)
			if err != nil {
				return err
			}
			result = TurnCompletion{
				Session: session, Events: events, Applied: false,
				Parked: turnEventsParked(events),
			}
			return nil
		}
		if attemptID != "" {
			if err := validateAttemptFinish(attemptState, attemptError); err != nil {
				return err
			}
			if err := s.finishAttemptLocked(
				ctx, q, attemptID, attemptState, attemptError,
				sessionID, triggerEventID,
			); err != nil {
				return err
			}
		}
		resolvedPending, err := s.resolvePendingBarrierLocked(
			ctx, q, sessionID, threadID, triggerEventID, resolutionEventIDs,
		)
		if err != nil {
			return err
		}

		now := s.clock.Now().UTC()
		primaryID := *thread.ParentThreadID
		report := []any(nil)
		if status == domain.StatusIdle && len(pendingActionEventIDs) == 0 {
			report = lastAgentMessageContent(outputDrafts)
		}
		drafts := childThreadDrafts(
			outputDrafts, thread.ID, thread.Agent.Name,
			primaryID, session.AgentSnapshot.Name, len(report) > 0,
		)
		maxSeq, err := q.MaxEventSeq(ctx, sessionID)
		if err != nil {
			return err
		}
		childEvents, maxSeq, err := s.appendThreadDrafts(
			ctx, q, sessionID, threadID, drafts, maxSeq, &triggerEventID,
		)
		if err != nil {
			return err
		}
		allEvents := append([]domain.Event(nil), childEvents...)
		allowedActions := make(map[string]domain.Event, len(childEvents))
		for _, event := range childEvents {
			allowedActions[event.ID] = event
		}
		clientActionEventIDs := make(map[string]string, len(pendingActionEventIDs))
		clientActionDrafts := make([]domain.EventDraft, 0, len(pendingActionEventIDs))
		for _, actionEventID := range pendingActionEventIDs {
			action, ok := allowedActions[actionEventID]
			if !ok {
				return domain.Validation(
					"pending action must reference an action event committed by this turn",
				)
			}
			if _, ok := domain.PendingActionKindForEvent(action.Type, action.Payload); !ok {
				return domain.Validation("event cannot park a pending action")
			}
			payload := cloneEventPayload(action.Payload)
			payload["session_thread_id"] = threadID
			clientID := s.ids.NewID(domain.PrefixEvent)
			clientActionEventIDs[actionEventID] = clientID
			clientActionDrafts = append(clientActionDrafts, domain.EventDraft{
				ID: clientID, Type: action.Type, Payload: payload,
			})
		}
		if len(clientActionDrafts) > 0 {
			projected, next, err := s.appendThreadDrafts(
				ctx, q, sessionID, primaryID,
				clientActionDrafts, maxSeq, &triggerEventID,
			)
			if err != nil {
				return err
			}
			maxSeq = next
			allEvents = append(allEvents, projected...)
		}
		if err := s.insertPendingActionsLocked(
			ctx, q, sessionID, threadID, pendingActionEventIDs,
			allowedActions, clientActionEventIDs,
		); err != nil {
			return err
		}

		if len(report) > 0 {
			received, next, err := s.appendThreadDrafts(
				ctx, q, sessionID, primaryID,
				[]domain.EventDraft{{
					Type: domain.EvAgentThreadMessageReceived,
					Payload: map[string]any{
						"from_session_thread_id": threadID,
						"from_agent_name":        thread.Agent.Name,
						"content":                report,
					},
				}}, maxSeq, nil,
			)
			if err != nil {
				return err
			}
			maxSeq = next
			allEvents = append(allEvents, received...)
		}

		for _, draft := range drafts {
			if !isThreadLifecycleEvent(draft.Type) {
				continue
			}
			draft = projectChildLifecycleDraft(draft, clientActionEventIDs)
			crossPosted, next, err := s.appendThreadDrafts(
				ctx, q, sessionID, primaryID,
				[]domain.EventDraft{draft}, maxSeq, &triggerEventID,
			)
			if err != nil {
				return err
			}
			maxSeq = next
			allEvents = append(allEvents, crossPosted...)
		}

		if transcriptDelta != nil {
			representedEventIDs := resolutionEventIDs
			if len(representedEventIDs) == 0 {
				representedEventIDs = []string{triggerEventID}
			}
			representedJSON, err := json.Marshal(representedEventIDs)
			if err != nil {
				return err
			}
			messagesJSON, err := json.Marshal(transcriptDelta)
			if err != nil {
				return err
			}
			mappingsJSON, err := json.Marshal(toolUseMappings)
			if err != nil {
				return err
			}
			if err := q.InsertProviderTranscriptTurn(
				ctx, pgstore.InsertProviderTranscriptTurnParams{
					SessionID: sessionID, TriggerEventID: triggerEventID,
					CommittedThroughSeq: maxSeq,
					RepresentedEventIds: representedJSON,
					Messages:            messagesJSON, ToolUseMappings: mappingsJSON,
					CreatedAt: tsUTC(now),
				},
			); err != nil {
				return err
			}
		}
		processedEventIDs := resolutionEventIDs
		if len(processedEventIDs) == 0 {
			processedEventIDs = []string{triggerEventID}
		}
		for _, resolutionEventID := range resolutionEventIDs {
			row, err := q.GetEvent(ctx, pgstore.GetEventParams{
				SessionID: sessionID, ID: resolutionEventID,
			})
			if err != nil {
				return err
			}
			var payload map[string]any
			if err := json.Unmarshal(row.Payload, &payload); err != nil {
				return err
			}
			if companionID, _ := payload[domain.InternalCompanionSystemEventID].(string); companionID != "" {
				processedEventIDs = appendUniqueStrings(processedEventIDs, companionID)
			}
		}
		for _, eventID := range processedEventIDs {
			if err := q.MarkEventProcessed(ctx, pgstore.MarkEventProcessedParams{
				ProcessedAt: tsUTC(now), SessionID: sessionID, ID: eventID,
			}); err != nil {
				return err
			}
		}

		thread.Usage.Add(usage)
		thread.TransitionStatus(status, now)
		if err := putSessionThreadTx(ctx, tx, thread); err != nil {
			return err
		}
		if len(report) > 0 {
			primary, err := loadSessionThreadForUpdate(
				ctx, tx, sessionID, primaryID,
			)
			if err != nil {
				return err
			}
			if primary.Status == domain.StatusIdle {
				primary.TransitionStatus(domain.StatusRunning, now)
				if err := putSessionThreadTx(ctx, tx, primary); err != nil {
					return err
				}
			}
			if err := q.UpsertOutbox(ctx, pgstore.UpsertOutboxParams{
				SessionID: sessionID, MaxEventSeq: maxSeq, EnqueuedAt: tsUTC(now),
			}); err != nil {
				return err
			}
		}

		session.Usage.Add(usage)
		aggregated, err := aggregateSessionThreadStatus(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		oldSessionStatus := session.Status
		session.TransitionStatus(aggregated, now)
		if err := putSessionOnlyTx(ctx, tx, session); err != nil {
			return err
		}
		pendingClientEventIDs, err := q.SessionPendingClientActionEventIDs(
			ctx, sessionID,
		)
		if err != nil {
			return err
		}
		pendingBoundaryChanged := len(pendingActionEventIDs) > 0 || resolvedPending
		if oldSessionStatus != aggregated ||
			(aggregated == domain.StatusIdle && pendingBoundaryChanged) {
			var statusDraft *domain.EventDraft
			switch aggregated {
			case domain.StatusRunning:
				statusDraft = &domain.EventDraft{
					Type: domain.EvSessionStatusRunning, Payload: map[string]any{},
				}
			case domain.StatusIdle:
				stopReason := map[string]any{"type": "end_turn"}
				if len(pendingClientEventIDs) > 0 {
					stopReason = map[string]any{
						"type":      "requires_action",
						"event_ids": pendingClientEventIDs,
					}
				}
				statusDraft = &domain.EventDraft{
					Type:    domain.EvSessionStatusIdle,
					Payload: map[string]any{"stop_reason": stopReason},
				}
			case domain.StatusRescheduling:
				statusDraft = &domain.EventDraft{
					Type:    domain.EvSessionStatusRescheduling,
					Payload: map[string]any{},
				}
			}
			if statusDraft != nil {
				statusEvents, _, err := s.appendThreadDrafts(
					ctx, q, sessionID, primaryID,
					[]domain.EventDraft{*statusDraft}, maxSeq, &triggerEventID,
				)
				if err != nil {
					return err
				}
				allEvents = append(allEvents, statusEvents...)
			}
		}
		result = TurnCompletion{
			Session: session, Events: allEvents, Applied: true,
			Parked: len(pendingActionEventIDs) > 0,
		}
		return nil
	})
	if err != nil {
		return TurnCompletion{}, err
	}
	s.notifySession(ctx, sessionID)
	return result, nil
}

func childThreadDrafts(
	drafts []domain.EventDraft,
	threadID string,
	agentName string,
	primaryThreadID string,
	primaryAgentName string,
	reportFinalMessage bool,
) []domain.EventDraft {
	reportIndex := -1
	if reportFinalMessage {
		for index := len(drafts) - 1; index >= 0; index-- {
			if drafts[index].Type == domain.EvAgentMessage {
				reportIndex = index
				break
			}
		}
	}
	out := make([]domain.EventDraft, 0, len(drafts))
	for index, draft := range drafts {
		if index == reportIndex {
			out = append(out, domain.EventDraft{
				Type: domain.EvAgentThreadMessageSent,
				Payload: map[string]any{
					"to_session_thread_id": primaryThreadID,
					"to_agent_name":        primaryAgentName,
					"content":              draft.Payload["content"],
				},
			})
			continue
		}
		payload := make(map[string]any, len(draft.Payload)+2)
		for key, value := range draft.Payload {
			payload[key] = value
		}
		switch draft.Type {
		case domain.EvSessionStatusIdle:
			draft.Type = domain.EvSessionThreadStatusIdle
		case domain.EvSessionStatusTerminated:
			draft.Type = domain.EvSessionThreadStatusTerminated
		case domain.EvSessionStatusRescheduling:
			draft.Type = domain.EvSessionThreadStatusRescheduled
		}
		if isThreadLifecycleEvent(draft.Type) {
			payload["session_thread_id"] = threadID
			payload["agent_name"] = agentName
		}
		draft.Payload = payload
		out = append(out, draft)
	}
	return out
}

func isThreadLifecycleEvent(eventType string) bool {
	switch eventType {
	case domain.EvSessionThreadStatusIdle,
		domain.EvSessionThreadStatusRunning,
		domain.EvSessionThreadStatusRescheduled,
		domain.EvSessionThreadStatusTerminated:
		return true
	default:
		return false
	}
}

func cloneEventPayload(payload map[string]any) map[string]any {
	cloned := make(map[string]any, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

// projectChildLifecycleDraft rewrites the requires-action ids on the primary
// projection to the cross-posted tool-use ids clients can actually observe and
// answer. The child ledger keeps its own canonical ids for provider context.
func projectChildLifecycleDraft(
	draft domain.EventDraft,
	clientActionEventIDs map[string]string,
) domain.EventDraft {
	payload := cloneEventPayload(draft.Payload)
	stopReason, _ := payload["stop_reason"].(map[string]any)
	if stopReason == nil || stopReason["type"] != "requires_action" {
		draft.Payload = payload
		return draft
	}
	projectedStop := cloneEventPayload(stopReason)
	if eventIDs, ok := stringList(stopReason["event_ids"]); ok {
		for index, eventID := range eventIDs {
			if projectedID := clientActionEventIDs[eventID]; projectedID != "" {
				eventIDs[index] = projectedID
			}
		}
		projectedStop["event_ids"] = eventIDs
	}
	payload["stop_reason"] = projectedStop
	draft.Payload = payload
	return draft
}

func lastAgentMessageContent(drafts []domain.EventDraft) []any {
	for index := len(drafts) - 1; index >= 0; index-- {
		if drafts[index].Type != domain.EvAgentMessage {
			continue
		}
		content, _ := drafts[index].Payload["content"].([]any)
		return content
	}
	return nil
}

func aggregateSessionThreadStatus(
	ctx context.Context,
	tx pgx.Tx,
	sessionID string,
) (domain.Status, error) {
	rows, err := tx.Query(ctx, `
SELECT status
FROM session_threads
WHERE session_id = $1 AND archived_at IS NULL`, sessionID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	allTerminated := true
	hasRescheduling := false
	for rows.Next() {
		var status domain.Status
		if err := rows.Scan(&status); err != nil {
			return "", err
		}
		if status == domain.StatusRunning {
			return domain.StatusRunning, nil
		}
		if status == domain.StatusRescheduling {
			hasRescheduling = true
		}
		if status != domain.StatusTerminated {
			allTerminated = false
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if hasRescheduling {
		return domain.StatusRescheduling, nil
	}
	if allTerminated {
		return domain.StatusTerminated, nil
	}
	return domain.StatusIdle, nil
}
