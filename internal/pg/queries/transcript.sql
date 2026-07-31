-- Provider transcript queries. Turn deltas are inserted in the same transaction
-- that commits their public events and marks the trigger processed.

-- name: InsertProviderTranscriptTurn :exec
INSERT INTO provider_transcript_turns (
    session_id,
    trigger_event_id,
    committed_through_seq,
    represented_event_ids,
    messages,
    tool_use_mappings,
    created_at
)
VALUES (
    @session_id,
    @trigger_event_id,
    @committed_through_seq,
    @represented_event_ids,
    @messages,
    @tool_use_mappings,
    @created_at
)
ON CONFLICT (session_id, trigger_event_id) DO NOTHING;

-- name: ListProviderTranscriptTurns :many
SELECT
    session_id,
    trigger_event_id,
    turn_ordinal,
    committed_through_seq,
    represented_event_ids,
    messages,
    tool_use_mappings,
    created_at
FROM provider_transcript_turns
WHERE session_id = @session_id
ORDER BY turn_ordinal;
