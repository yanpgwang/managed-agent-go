-- +goose Up
-- +goose StatementBegin

ALTER TABLE session_resources
    DROP CONSTRAINT session_resources_shape_check,
    DROP CONSTRAINT session_resources_resource_type_check,
    ADD COLUMN repository_url text,
    ADD COLUMN repository_checkout_type text,
    ADD COLUMN repository_checkout_value text,
    ADD COLUMN repository_resolved_commit text;

-- Repository archives reuse the durable File/blob intent lifecycle, but are
-- implementation objects rather than public Files.
ALTER TABLE files ADD COLUMN internal_use boolean NOT NULL DEFAULT false;

ALTER TABLE session_resources
    ADD CONSTRAINT session_resources_resource_type_check
        CHECK (resource_type IN ('file', 'memory_store', 'git_repository')),
    ADD CONSTRAINT session_resources_repository_checkout_check CHECK (
        repository_checkout_type IS NULL
        OR repository_checkout_type IN ('branch', 'commit')
    ),
    ADD CONSTRAINT session_resources_repository_commit_check CHECK (
        repository_resolved_commit IS NULL
        OR repository_resolved_commit ~ '^[0-9a-f]{40}$'
    ),
    ADD CONSTRAINT session_resources_shape_check CHECK (
        (resource_type = 'file'
            AND source_file_id IS NOT NULL
            AND file_id IS NOT NULL
            AND memory_store_id IS NULL
            AND memory_access IS NULL
            AND memory_instructions IS NULL
            AND memory_store_name IS NULL
            AND memory_store_description IS NULL
            AND repository_url IS NULL
            AND repository_checkout_type IS NULL
            AND repository_checkout_value IS NULL
            AND repository_resolved_commit IS NULL)
        OR
        (resource_type = 'memory_store'
            AND source_file_id IS NULL
            AND file_id IS NULL
            AND memory_store_id IS NOT NULL
            AND memory_access IS NOT NULL
            AND memory_instructions IS NOT NULL
            AND memory_store_name IS NOT NULL
            AND memory_store_description IS NOT NULL
            AND repository_url IS NULL
            AND repository_checkout_type IS NULL
            AND repository_checkout_value IS NULL
            AND repository_resolved_commit IS NULL)
        OR
        (resource_type = 'git_repository'
            AND source_file_id IS NULL
            AND file_id IS NOT NULL
            AND memory_store_id IS NULL
            AND memory_access IS NULL
            AND memory_instructions IS NULL
            AND memory_store_name IS NULL
            AND memory_store_description IS NULL
            AND repository_url IS NOT NULL
            AND ((repository_checkout_type IS NULL AND repository_checkout_value IS NULL)
                OR (repository_checkout_type IS NOT NULL AND repository_checkout_value IS NOT NULL))
            AND repository_resolved_commit IS NOT NULL)
    );

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

UPDATE files
SET state = 'deleting', updated_at = now()
WHERE id IN (
    SELECT file_id
    FROM session_resources
    WHERE resource_type = 'git_repository'
);

DELETE FROM session_resources WHERE resource_type = 'git_repository';

ALTER TABLE files DROP COLUMN IF EXISTS internal_use;

ALTER TABLE session_resources
    DROP CONSTRAINT IF EXISTS session_resources_shape_check,
    DROP CONSTRAINT IF EXISTS session_resources_repository_commit_check,
    DROP CONSTRAINT IF EXISTS session_resources_repository_checkout_check,
    DROP CONSTRAINT IF EXISTS session_resources_resource_type_check,
    DROP COLUMN IF EXISTS repository_resolved_commit,
    DROP COLUMN IF EXISTS repository_checkout_value,
    DROP COLUMN IF EXISTS repository_checkout_type,
    DROP COLUMN IF EXISTS repository_url;

ALTER TABLE session_resources
    ADD CONSTRAINT session_resources_resource_type_check
        CHECK (resource_type IN ('file', 'memory_store')),
    ADD CONSTRAINT session_resources_shape_check CHECK (
        (resource_type = 'file'
            AND source_file_id IS NOT NULL
            AND file_id IS NOT NULL
            AND memory_store_id IS NULL
            AND memory_access IS NULL
            AND memory_instructions IS NULL
            AND memory_store_name IS NULL
            AND memory_store_description IS NULL)
        OR
        (resource_type = 'memory_store'
            AND source_file_id IS NULL
            AND file_id IS NULL
            AND memory_store_id IS NOT NULL
            AND memory_access IS NOT NULL
            AND memory_instructions IS NOT NULL
            AND memory_store_name IS NOT NULL
            AND memory_store_description IS NOT NULL)
    );

-- +goose StatementEnd
