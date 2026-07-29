---
title: Getting started
slug: /getting-started
sidebar_position: 2
---

# Getting started

This guide runs the server with its deterministic offline model and sends one
message through the full environment → agent → session → event flow.

## Requirements

- Docker with Compose
- `curl`
- `jq` for the shell examples

## Run the server

```bash
make -C deployments/local up
make -C deployments/local health
```

This builds and starts separate API and worker containers plus PostgreSQL,
Temporal, Temporal UI, and NATS. The examples use `http://localhost:8080`; the
Workflow explorer is at `http://localhost:8233`. No model credentials are
required because the worker defaults to the deterministic offline model.

In another shell, verify readiness:

```bash
curl -i http://localhost:8080/readyz
```

## Create an environment

Environment records identify where a session runs. The implemented `cloud`
record routes work to the Temporal worker:

```bash
ENV_ID=$(
  curl -sS http://localhost:8080/v1/environments \
    -H 'content-type: application/json' \
    -d '{"name":"local","config":{"type":"cloud"}}' |
  jq -r .id
)
```

`self_hosted` environment sessions are not implemented and return `422`.

## Create an agent

```bash
AGENT_ID=$(
  curl -sS http://localhost:8080/v1/agents \
    -H 'content-type: application/json' \
    -d '{
      "name": "Example agent",
      "model": "offline-fake",
      "system": "Be concise."
    }' |
  jq -r .id
)
```

An agent is versioned. Updating it creates a new version; sessions retain the
resolved version and configuration captured at creation time.

## Create a session

```bash
SESSION_ID=$(
  curl -sS http://localhost:8080/v1/sessions \
    -H 'content-type: application/json' \
    -d "{
      \"agent\": \"$AGENT_ID\",
      \"environment_id\": \"$ENV_ID\",
      \"title\": \"First session\"
    }" |
  jq -r .id
)
```

## Send a message

```bash
curl -sS "http://localhost:8080/v1/sessions/$SESSION_ID/events" \
  -H 'content-type: application/json' \
  -d '{
    "events": [{
      "type": "user.message",
      "content": [{"type":"text","text":"hello"}]
    }]
  }' | jq
```

Sending an event admits durable work and returns the accepted input events. The
agent response is asynchronous. Poll history:

```bash
curl -sS \
  "http://localhost:8080/v1/sessions/$SESSION_ID/events?order=asc" |
  jq
```

A completed turn includes `agent.message` followed by
`session.status_idle`.

## Stream events

Open the stream before sending the next message:

```bash
curl -N \
  "http://localhost:8080/v1/sessions/$SESSION_ID/events/stream?event_deltas%5B%5D=agent.message"
```

The opt-in adds ephemeral `event_start` and `event_delta` frames while text is
generated. The final `agent.message` is authoritative and persisted; preview
frames are not. NATS carries low-latency wakeups and previews, while every
persisted event is reconciled from PostgreSQL by sequence.

## Use a real model endpoint

The quick start already runs an offline worker. Stop it before launching a
source worker with different model or sandbox configuration:

```bash
docker compose -f deployments/local/docker-compose.yml stop worker

export MANAGED_AGENT_DATABASE_URL="postgres://postgres:postgres@localhost:5432/managed_agent?sslmode=disable"
export MANAGED_AGENT_TEMPORAL_HOSTPORT="localhost:7233"
export MANAGED_AGENT_NATS_URL="nats://localhost:4222"

export MANAGED_AGENT_MODEL_BASE_URL=https://api.example.com
export MANAGED_AGENT_MODEL_API_KEY=replace-me
export MANAGED_AGENT_MODEL_ID=claude-model-id
export MANAGED_AGENT_MODEL_AUTH=x-api-key # or authorization-bearer

# A real model must not run against the local sandbox (it is a dev-grade
# guardrail, not a security boundary), so select the Docker sandbox for real
# isolation. The server refuses to start with a real model + local sandbox.
export MANAGED_AGENT_SANDBOX=docker

go run ./cmd/managed-agent orchestrate
```

The configured endpoint must expose an Anthropic-shaped `/v1/messages` API.
Do not run workers with different model or sandbox configuration on the same
Temporal Task Queue. Keep credentials in the environment and never commit them.

If you understand the risk and deliberately want a real model against the local
sandbox during development, set `MANAGED_AGENT_ALLOW_UNSAFE_LOCAL_SANDBOX=1` to
override the startup guard. This is a dev-only escape hatch — the local sandbox
runs tool commands on the host with no isolation, so never use it with untrusted
input or in production.

## Clean up

```bash
make -C deployments/local down
```

This keeps the PostgreSQL volume. Add `VOLUMES=1` only when you intentionally
want to delete local data. PostgreSQL schema changes are applied by embedded,
versioned `goose` migrations when API or worker processes start.

## Temporary SQLite compatibility mode

The former single-process runtime remains available during the client-action
and interrupt migration:

```bash
go run ./cmd/managed-agent serve -backend sqlite -db managed-agent.db
```

It is a frozen comparison/rollback path, not a production backend. New
deployments should use the PostgreSQL/Temporal stack above.
