-- +goose Up
-- +goose StatementBegin

-- Runtime-produced Files retain their normalized path relative to
-- /mnt/session/outputs. The public Files contract exposes only filename and
-- Session scope; output_path is the durable idempotency and replacement key.
ALTER TABLE files ADD COLUMN output_path text;

ALTER TABLE files ADD CONSTRAINT files_output_path_check CHECK (
    output_path IS NULL OR (
        scope_type = 'session'
        AND scope_id IS NOT NULL
        AND downloadable
        AND length(output_path) BETWEEN 1 AND 1024
    )
);

-- At most one ready version of one logical deliverable is public. A
-- replacement first writes a new blob, then atomically hides the prior row and
-- publishes the new row; deleting/uploading intents remain available for
-- crash reconciliation outside the transaction.
CREATE UNIQUE INDEX files_ready_session_output_path_idx
    ON files (workspace_id, scope_id, output_path)
    WHERE state = 'ready' AND output_path IS NOT NULL;

CREATE INDEX files_session_output_cleanup_idx
    ON files (workspace_id, scope_id, state, id)
    WHERE output_path IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS files_session_output_cleanup_idx;
DROP INDEX IF EXISTS files_ready_session_output_path_idx;
ALTER TABLE files DROP CONSTRAINT IF EXISTS files_output_path_check;
ALTER TABLE files DROP COLUMN output_path;

-- +goose StatementEnd
