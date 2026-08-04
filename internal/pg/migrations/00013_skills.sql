-- +goose Up
-- +goose StatementBegin

-- Skill rows and immutable Version metadata are authoritative in PostgreSQL;
-- archive bytes live in the configured S3-compatible object store. Version
-- intent states make process-crash reconciliation deterministic.
CREATE TABLE skills (
    id                     text        PRIMARY KEY,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL,
    display_title          text        NOT NULL,
    latest_version         text,
    source                 text        NOT NULL CHECK (source = 'custom'),
    display_title_explicit boolean     NOT NULL DEFAULT false,
    ready                  boolean     NOT NULL DEFAULT false
);

CREATE UNIQUE INDEX skills_explicit_display_title_idx
    ON skills (lower(display_title))
    WHERE display_title_explicit;
CREATE INDEX skills_ready_list_idx
    ON skills (created_at DESC, id DESC)
    WHERE ready;

CREATE TABLE skill_versions (
    skill_id        text        NOT NULL REFERENCES skills(id) ON DELETE RESTRICT,
    version         text        NOT NULL CHECK (version ~ '^[0-9]+$'),
    created_at      timestamptz NOT NULL,
    description     text        NOT NULL,
    directory       text        NOT NULL,
    name            text        NOT NULL,
    blob_key        text        NOT NULL UNIQUE,
    size_bytes      bigint      NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    checksum_sha256 text        NOT NULL DEFAULT '',
    state           text        NOT NULL CHECK (state IN ('uploading', 'ready', 'deleting')),
    initial         boolean     NOT NULL DEFAULT false,
    PRIMARY KEY (skill_id, version)
);

CREATE INDEX skill_versions_ready_list_idx
    ON skill_versions (skill_id, created_at DESC, version DESC)
    WHERE state = 'ready';
CREATE INDEX skill_versions_incomplete_idx
    ON skill_versions (created_at, skill_id, version)
    WHERE state <> 'ready';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS skill_versions;
DROP TABLE IF EXISTS skills;
-- +goose StatementEnd
