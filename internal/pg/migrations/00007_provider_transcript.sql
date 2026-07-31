-- +goose Up
-- +goose StatementBegin

-- Lossless provider-continuation history. Public events remain the client-facing
-- ledger; these immutable per-turn deltas retain provider-native content blocks
-- (including citations and opaque server-tool fields) for later model calls.
CREATE TABLE provider_transcript_turns (
    session_id             text        NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    trigger_event_id       text        NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    turn_ordinal           bigint      GENERATED ALWAYS AS IDENTITY,
    committed_through_seq  bigint      NOT NULL,
    represented_event_ids  jsonb       NOT NULL,
    messages               jsonb       NOT NULL,
    tool_use_mappings      jsonb       NOT NULL DEFAULT '[]'::jsonb,
    created_at             timestamptz NOT NULL,
    PRIMARY KEY (session_id, trigger_event_id)
);

CREATE INDEX provider_transcript_turns_order_idx
    ON provider_transcript_turns (session_id, turn_ordinal);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS provider_transcript_turns;
-- +goose StatementEnd
