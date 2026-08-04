-- +goose Up
-- +goose StatementBegin

-- Runtime staging is bounded by the validated uncompressed bundle footprint,
-- not by the zip size. Existing canonical archives predate this metadata, so
-- mark it unknown; runtime extraction still verifies the archive checksum and
-- applies the normal 30 MB expansion ceiling. Newly created Versions persist
-- their exact value.
ALTER TABLE skill_versions
    ADD COLUMN uncompressed_size_bytes bigint NOT NULL DEFAULT -1
        CHECK (uncompressed_size_bytes >= -1 AND uncompressed_size_bytes < 30000000);

ALTER TABLE skill_versions
    ALTER COLUMN uncompressed_size_bytes DROP DEFAULT;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE skill_versions DROP COLUMN IF EXISTS uncompressed_size_bytes;
-- +goose StatementEnd
