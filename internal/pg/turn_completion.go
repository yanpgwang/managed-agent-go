package pg

import (
	"context"
	"encoding/json"

	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/pg/pgstore"
)

// turnProcessedEventIDs returns every input event consumed by a completed turn,
// including a companion system.message attached to any member of a resumed
// action barrier. Both primary and child completion paths use this helper so
// their public processed_at semantics cannot drift.
func turnProcessedEventIDs(
	ctx context.Context,
	q *pgstore.Queries,
	sessionID string,
	triggerEventID string,
	triggerPayload map[string]any,
	resolutionEventIDs []string,
	extraEventIDs ...string,
) ([]string, error) {
	representedEventIDs := append([]string(nil), resolutionEventIDs...)
	if len(representedEventIDs) == 0 {
		representedEventIDs = []string{triggerEventID}
	}
	processedEventIDs := appendUniqueStrings(nil, representedEventIDs...)
	for _, eventID := range representedEventIDs {
		payload := triggerPayload
		if eventID != triggerEventID || payload == nil {
			row, err := q.GetEvent(ctx, pgstore.GetEventParams{
				SessionID: sessionID, ID: eventID,
			})
			if err != nil {
				return nil, err
			}
			payload = nil
			if err := json.Unmarshal(row.Payload, &payload); err != nil {
				return nil, err
			}
		}
		if companionID, _ := payload[domain.InternalCompanionSystemEventID].(string); companionID != "" {
			processedEventIDs = appendUniqueStrings(processedEventIDs, companionID)
		}
	}
	return appendUniqueStrings(processedEventIDs, extraEventIDs...), nil
}
