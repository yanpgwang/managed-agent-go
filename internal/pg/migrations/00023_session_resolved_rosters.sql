-- +goose Up
-- +goose StatementBegin

-- Agent resources store coordinator rosters as immutable Agent Version pins.
-- Session resources expose those pins as full Agent definitions and retain the
-- resulting snapshots for child Thread creation. Backfill Sessions created
-- before that distinction was persisted. A self pin uses the Session's already
-- overridden AgentSnapshot; external pins use the exact stored Agent Version.
UPDATE sessions AS session
SET body = jsonb_set(
    session.body,
    '{MultiagentRoster}',
    COALESCE((
        SELECT jsonb_agg(
            CASE
                WHEN roster.value->>'id' = session.agent_id
                    THEN (session.body->'AgentSnapshot') - 'Multiagent'
                ELSE agent.body - 'Multiagent'
            END
            ORDER BY roster.ordinality
        )
        FROM jsonb_array_elements(
            session.body #> '{AgentSnapshot,Multiagent,agents}'
        ) WITH ORDINALITY AS roster(value, ordinality)
        LEFT JOIN agents AS agent
          ON agent.id = roster.value->>'id'
         AND agent.version = (roster.value->>'version')::integer
    ), '[]'::jsonb),
    true
)
WHERE jsonb_typeof(
    session.body #> '{AgentSnapshot,Multiagent,agents}'
) = 'array'
  AND NOT session.body ? 'MultiagentRoster'
  AND NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(
          session.body #> '{AgentSnapshot,Multiagent,agents}'
      ) AS entry(value)
      WHERE jsonb_typeof(entry.value) <> 'object'
         OR entry.value->>'type' <> 'agent'
         OR COALESCE(entry.value->>'id', '') = ''
         OR COALESCE(entry.value->>'version', '') = ''
  );

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

UPDATE sessions
SET body = body - 'MultiagentRoster'
WHERE body ? 'MultiagentRoster';

-- +goose StatementEnd
