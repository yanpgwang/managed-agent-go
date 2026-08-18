-- +goose Up
-- +goose StatementBegin

ALTER TABLE session_threads
    DROP CONSTRAINT session_threads_kind_check,
    ADD CONSTRAINT session_threads_kind_check
        CHECK (kind IN ('primary', 'child', 'advisor'));

ALTER TABLE session_threads
    DROP CONSTRAINT session_threads_parent_check,
    ADD CONSTRAINT session_threads_parent_check CHECK (
        (kind = 'primary' AND parent_thread_id IS NULL) OR
        (kind IN ('child', 'advisor') AND parent_thread_id IS NOT NULL)
    );

COMMENT ON COLUMN session_threads.kind IS
    'primary and persistent child Workflows, plus automatically terminating advisor consultations';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Operational downgrade order is mandatory: drain or cancel active Sessions,
-- run this Down migration with the current release, then deploy a pre-Advisor
-- binary. Older binaries cannot interpret Advisor roster entries or in-flight
-- Temporal commands.

-- The aggregate Session usage/cost is intentionally retained: the Advisor
-- inference really happened and remains billable even though version 32 cannot
-- represent its detailed Thread. Retaining it also keeps any budget pause
-- factually consistent and avoids trying to wake a parked Temporal Workflow
-- from migration SQL.

-- Private provider continuations may contain ordinary advisor tool_use and
-- tool_result blocks. Once the roster/tool is removed, a version-32 worker
-- cannot offer that runtime capability on a later continuation. Drop private
-- continuation state for affected Sessions so it safely rebuilds context from
-- the public ledger instead.
DELETE FROM thread_context_snapshots
WHERE session_id IN (
    SELECT DISTINCT session_id FROM session_threads WHERE kind = 'advisor'
);
DELETE FROM provider_transcript_turns
WHERE session_id IN (
    SELECT DISTINCT session_id FROM session_threads WHERE kind = 'advisor'
);

DELETE FROM tool_steps WHERE tool_name = 'advisor';

DELETE FROM model_request_usage
WHERE (session_id, thread_id) IN (
    SELECT session_id, id FROM session_threads WHERE kind = 'advisor'
);
DELETE FROM events AS event
WHERE EXISTS (
    SELECT 1
    FROM session_threads AS thread
    WHERE thread.kind = 'advisor'
      AND thread.session_id = event.session_id
      AND (
          event.thread_id = thread.id OR
          event.payload->>'session_thread_id' = thread.id OR
          event.payload->>'from_session_thread_id' = thread.id OR
          event.payload->>'to_session_thread_id' = thread.id
      )
);
DELETE FROM session_threads WHERE kind = 'advisor';

-- Version 32 cannot interpret the advisor roster entry. Strip
-- it from every durable Agent snapshot while preserving ordinary pinned Agents
-- and their order. A coordinator whose only entry was Advisor becomes a
-- single-agent definition rather than an invalid empty coordinator.
WITH stripped AS (
    SELECT
        agent.id,
        agent.version,
        COALESCE(
            jsonb_agg(entry.value ORDER BY entry.ordinality)
                FILTER (WHERE entry.value ->> 'type' <> 'advisor'),
            '[]'::jsonb
        ) AS roster
    FROM agents AS agent
    CROSS JOIN LATERAL jsonb_array_elements(
        CASE
            WHEN jsonb_typeof(agent.body #> '{Multiagent,agents}') = 'array'
                THEN agent.body #> '{Multiagent,agents}'
            ELSE '[]'::jsonb
        END
    ) WITH ORDINALITY AS entry(value, ordinality)
    WHERE jsonb_typeof(agent.body #> '{Multiagent,agents}') = 'array'
    GROUP BY agent.id, agent.version
    HAVING BOOL_OR(entry.value ->> 'type' = 'advisor')
)
UPDATE agents AS agent
SET body = CASE
        WHEN jsonb_array_length(stripped.roster) = 0
            THEN jsonb_set(agent.body, '{Multiagent}', 'null'::jsonb, true)
        ELSE jsonb_set(agent.body, '{Multiagent,agents}', stripped.roster, true)
    END
FROM stripped
WHERE agent.id = stripped.id
  AND agent.version = stripped.version;

WITH stripped AS (
    SELECT
        session.id,
        COALESCE(
            jsonb_agg(entry.value ORDER BY entry.ordinality)
                FILTER (WHERE entry.value ->> 'type' <> 'advisor'),
            '[]'::jsonb
        ) AS roster
    FROM sessions AS session
    CROSS JOIN LATERAL jsonb_array_elements(
        CASE
            WHEN jsonb_typeof(
                session.body #> '{AgentSnapshot,Multiagent,agents}'
            ) = 'array'
                THEN session.body #> '{AgentSnapshot,Multiagent,agents}'
            ELSE '[]'::jsonb
        END
    ) WITH ORDINALITY AS entry(value, ordinality)
    WHERE jsonb_typeof(
        session.body #> '{AgentSnapshot,Multiagent,agents}'
    ) = 'array'
    GROUP BY session.id
    HAVING BOOL_OR(entry.value ->> 'type' = 'advisor')
)
UPDATE sessions AS session
SET body = CASE
        WHEN jsonb_array_length(stripped.roster) = 0
            THEN jsonb_set(session.body, '{AgentSnapshot,Multiagent}', 'null'::jsonb, true)
        ELSE jsonb_set(
            session.body,
            '{AgentSnapshot,Multiagent,agents}',
            stripped.roster,
            true
        )
    END
FROM stripped
WHERE session.id = stripped.id;

WITH stripped AS (
    SELECT
        thread.session_id,
        thread.id,
        COALESCE(
            jsonb_agg(entry.value ORDER BY entry.ordinality)
                FILTER (WHERE entry.value ->> 'type' <> 'advisor'),
            '[]'::jsonb
        ) AS roster
    FROM session_threads AS thread
    CROSS JOIN LATERAL jsonb_array_elements(
        CASE
            WHEN jsonb_typeof(
                thread.body #> '{Agent,Multiagent,agents}'
            ) = 'array'
                THEN thread.body #> '{Agent,Multiagent,agents}'
            ELSE '[]'::jsonb
        END
    ) WITH ORDINALITY AS entry(value, ordinality)
    WHERE jsonb_typeof(thread.body #> '{Agent,Multiagent,agents}') = 'array'
    GROUP BY thread.session_id, thread.id
    HAVING BOOL_OR(entry.value ->> 'type' = 'advisor')
)
UPDATE session_threads AS thread
SET body = CASE
        WHEN jsonb_array_length(stripped.roster) = 0
            THEN jsonb_set(thread.body, '{Agent,Multiagent}', 'null'::jsonb, true)
        ELSE jsonb_set(
            thread.body,
            '{Agent,Multiagent,agents}',
            stripped.roster,
            true
        )
    END
FROM stripped
WHERE thread.session_id = stripped.session_id
  AND thread.id = stripped.id;

ALTER TABLE session_threads
    DROP CONSTRAINT session_threads_parent_check,
    ADD CONSTRAINT session_threads_parent_check CHECK (
        (kind = 'primary' AND parent_thread_id IS NULL) OR
        (kind = 'child' AND parent_thread_id IS NOT NULL)
    );

ALTER TABLE session_threads
    DROP CONSTRAINT session_threads_kind_check,
    ADD CONSTRAINT session_threads_kind_check
        CHECK (kind IN ('primary', 'child'));

-- +goose StatementEnd
