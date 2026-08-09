-- +goose Up
-- +goose StatementBegin

-- A Session freezes more than its coordinator once multi-agent orchestration
-- is enabled. Pin each resolved Agent execution scope independently so every
-- roster member remains executable without consulting mutable Agent resources.
ALTER TABLE session_skill_versions
    ADD COLUMN agent_id text,
    ADD COLUMN agent_version integer;

UPDATE session_skill_versions AS pin
SET agent_id = COALESCE(
        session.agent_id,
        session.body #>> '{AgentSnapshot,ID}'
    ),
    agent_version = COALESCE(
        session.agent_version,
        (session.body #>> '{AgentSnapshot,Version}')::integer
    )
FROM sessions AS session
WHERE session.id = pin.session_id;

ALTER TABLE session_skill_versions
    ALTER COLUMN agent_id SET NOT NULL,
    ALTER COLUMN agent_version SET NOT NULL,
    DROP CONSTRAINT session_skill_versions_pkey,
    ADD PRIMARY KEY (session_id, agent_id, agent_version, position);

-- Sessions created after the resolved-roster migration already contain the
-- complete immutable Agent snapshots. Retain every still-ready custom Version
-- available during this upgrade, even when no child Thread has been created
-- yet. A repeated self/reference scope is harmless and is already represented
-- by the primary rows above.
WITH roster_agents AS (
    SELECT session.id AS session_id,
           member.value AS agent
    FROM sessions AS session
    CROSS JOIN LATERAL jsonb_array_elements(
        CASE
            WHEN jsonb_typeof(session.body -> 'MultiagentRoster') = 'array'
                THEN session.body -> 'MultiagentRoster'
            ELSE '[]'::jsonb
        END
    ) AS member(value)
), roster_refs AS (
    SELECT roster.session_id,
           roster.agent ->> 'ID' AS agent_id,
           (roster.agent ->> 'Version')::integer AS agent_version,
           (ref.ordinality - 1)::integer AS position,
           ref.value AS value
    FROM roster_agents AS roster
    CROSS JOIN LATERAL jsonb_array_elements(
        CASE
            WHEN jsonb_typeof(roster.agent -> 'Skills') = 'array'
                THEN roster.agent -> 'Skills'
            ELSE '[]'::jsonb
        END
    ) WITH ORDINALITY AS ref(value, ordinality)
    WHERE roster.agent ->> 'ID' IS NOT NULL
      AND roster.agent ->> 'Version' ~ '^[1-9][0-9]*$'
)
INSERT INTO session_skill_versions (
    session_id, agent_id, agent_version, position, skill_id, skill_version
)
SELECT ref.session_id,
       ref.agent_id,
       ref.agent_version,
       ref.position,
       ref.value ->> 'skill_id',
       ref.value ->> 'version'
FROM roster_refs AS ref
JOIN skill_versions AS version
  ON version.skill_id = ref.value ->> 'skill_id'
 AND version.version = ref.value ->> 'version'
 AND version.state = 'ready'
JOIN skills AS skill ON skill.id = version.skill_id AND skill.ready
WHERE jsonb_typeof(ref.value) = 'object'
  AND ref.value ->> 'type' = 'custom'
ON CONFLICT (session_id, agent_id, agent_version, position) DO NOTHING;

DROP INDEX session_skill_versions_version_idx;
CREATE INDEX session_skill_versions_version_idx
    ON session_skill_versions(
        skill_id, skill_version, session_id, agent_id, agent_version
    );

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- The former schema can represent only the coordinator execution scope.
DELETE FROM session_skill_versions AS pin
USING sessions AS session
WHERE pin.session_id = session.id
  AND (pin.agent_id, pin.agent_version) IS DISTINCT FROM
      (
          COALESCE(session.agent_id, session.body #>> '{AgentSnapshot,ID}'),
          COALESCE(
              session.agent_version,
              (session.body #>> '{AgentSnapshot,Version}')::integer
          )
      );

DROP INDEX session_skill_versions_version_idx;
ALTER TABLE session_skill_versions
    DROP CONSTRAINT session_skill_versions_pkey,
    DROP COLUMN agent_id,
    DROP COLUMN agent_version,
    ADD PRIMARY KEY (session_id, position);
CREATE INDEX session_skill_versions_version_idx
    ON session_skill_versions(skill_id, skill_version, session_id);

-- +goose StatementEnd
