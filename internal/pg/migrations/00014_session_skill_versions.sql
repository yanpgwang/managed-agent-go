-- +goose Up
-- +goose StatementBegin

-- Session snapshots pin concrete custom Skill Versions relationally as well as
-- in their JSON projection. The child rows keep archive deletion from racing a
-- Session that has already committed its immutable execution configuration.
CREATE TABLE session_skill_versions (
    session_id    text    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    position      integer NOT NULL CHECK (position >= 0),
    skill_id      text    NOT NULL,
    skill_version text    NOT NULL,
    PRIMARY KEY (session_id, position),
    FOREIGN KEY (skill_id, skill_version)
        REFERENCES skill_versions(skill_id, version) ON DELETE RESTRICT
);

CREATE INDEX session_skill_versions_version_idx
    ON session_skill_versions(skill_id, skill_version, session_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS session_skill_versions;
-- +goose StatementEnd
