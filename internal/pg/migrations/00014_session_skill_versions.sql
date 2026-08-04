-- +goose Up
-- +goose StatementBegin

-- Active Agent configurations and immutable Session snapshots pin concrete
-- custom Skill Versions relationally as well as in their JSON projections.
-- The child rows keep archive deletion from racing a newly committed reference.
CREATE TABLE agent_skill_versions (
    agent_id      text    NOT NULL,
    agent_version integer NOT NULL,
    position      integer NOT NULL CHECK (position >= 0),
    skill_id      text    NOT NULL,
    skill_version text    NOT NULL,
    PRIMARY KEY (agent_id, position),
    FOREIGN KEY (agent_id, agent_version)
        REFERENCES agents(id, version) ON DELETE CASCADE,
    FOREIGN KEY (skill_id, skill_version)
        REFERENCES skill_versions(skill_id, version) ON DELETE RESTRICT
);

CREATE INDEX agent_skill_versions_version_idx
    ON agent_skill_versions(skill_id, skill_version, agent_id);

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

-- Version 13 could already be running while this migration was prepared. Pin
-- every concrete, still-ready custom reference present in the latest active
-- Agent or an existing Session. Former opaque values, aliases, and references
-- to missing archives remain readable through the application's legacy decoder
-- but cannot honestly be reconstructed as immutable pins.
WITH latest_agents AS (
    SELECT DISTINCT ON (id) id, version, body, archived_at
    FROM agents
    ORDER BY id, version DESC
), agent_refs AS (
    SELECT agent.id AS agent_id,
           agent.version AS agent_version,
           (ref.ordinality - 1)::integer AS position,
           ref.value AS value
    FROM latest_agents AS agent
    CROSS JOIN LATERAL jsonb_array_elements(
        CASE
            WHEN jsonb_typeof(agent.body -> 'Skills') = 'array'
                THEN agent.body -> 'Skills'
            ELSE '[]'::jsonb
        END
    ) WITH ORDINALITY AS ref(value, ordinality)
    WHERE agent.archived_at IS NULL
)
INSERT INTO agent_skill_versions (
    agent_id, agent_version, position, skill_id, skill_version
)
SELECT ref.agent_id,
       ref.agent_version,
       ref.position,
       ref.value ->> 'skill_id',
       ref.value ->> 'version'
FROM agent_refs AS ref
JOIN skill_versions AS version
  ON version.skill_id = ref.value ->> 'skill_id'
 AND version.version = ref.value ->> 'version'
 AND version.state = 'ready'
JOIN skills AS skill ON skill.id = version.skill_id AND skill.ready
WHERE jsonb_typeof(ref.value) = 'object'
  AND ref.value ->> 'type' = 'custom';

WITH session_refs AS (
    SELECT session.id AS session_id,
           (ref.ordinality - 1)::integer AS position,
           ref.value AS value
    FROM sessions AS session
    CROSS JOIN LATERAL jsonb_array_elements(
        CASE
            WHEN jsonb_typeof(session.body #> '{AgentSnapshot,Skills}') = 'array'
                THEN session.body #> '{AgentSnapshot,Skills}'
            ELSE '[]'::jsonb
        END
    ) WITH ORDINALITY AS ref(value, ordinality)
)
INSERT INTO session_skill_versions (
    session_id, position, skill_id, skill_version
)
SELECT ref.session_id,
       ref.position,
       ref.value ->> 'skill_id',
       ref.value ->> 'version'
FROM session_refs AS ref
JOIN skill_versions AS version
  ON version.skill_id = ref.value ->> 'skill_id'
 AND version.version = ref.value ->> 'version'
 AND version.state = 'ready'
JOIN skills AS skill ON skill.id = version.skill_id AND skill.ready
WHERE jsonb_typeof(ref.value) = 'object'
  AND ref.value ->> 'type' = 'custom';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS session_skill_versions;
DROP TABLE IF EXISTS agent_skill_versions;
-- +goose StatementEnd
