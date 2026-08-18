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
make local-up
make local-health
```

This builds and starts separate API and worker containers plus PostgreSQL,
Temporal, Temporal UI, and NATS. The examples use `http://localhost:8080`; the
Workflow explorer is at `http://localhost:8233`. No model credentials are
required because the worker defaults to the deterministic offline model.

In another shell, verify readiness:

```bash
curl -i http://localhost:8080/readyz
```

The Compose API bootstraps one development-only Workspace key. Export it once
for the protected examples below:

```bash
export MANGO_API_KEY=sk-mango-local-development
```

## Create an environment

Environment records identify where sandbox-routed tools run. A `cloud` record
routes them to the Temporal worker:

```bash
ENV_ID=$(
  curl -sS http://localhost:8080/v1/environments \
    -H "x-api-key: $MANGO_API_KEY" \
    -H 'content-type: application/json' \
    -d '{"name":"local","config":{"type":"cloud"}}' |
  jq -r .id
)
```

With `{"type":"self_hosted"}`, built-in calls instead park for a client
`user.tool_result`.

## Create an agent

```bash
AGENT_ID=$(
  curl -sS http://localhost:8080/v1/agents \
    -H "x-api-key: $MANGO_API_KEY" \
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
    -H "x-api-key: $MANGO_API_KEY" \
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
  -H "x-api-key: $MANGO_API_KEY" \
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
  -H "x-api-key: $MANGO_API_KEY" \
  "http://localhost:8080/v1/sessions/$SESSION_ID/events?order=asc" |
  jq
```

A completed turn includes `agent.message` followed by
`session.status_idle`.

## Stream events

Open the stream before sending the next message:

```bash
curl -N \
  -H "x-api-key: $MANGO_API_KEY" \
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
docker compose -f deployments/local/compose.yaml stop worker

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

The provider name is validated strictly. The compiled choices are `local`,
`docker`, `e2b`, `cube`, `opensandbox`, and `daytona`; an unknown value fails
worker startup and never silently falls back to local host execution. Remote
provider variables and live-test commands are listed in
[Sandbox backends](sandboxes.md).

The configured endpoint must expose an Anthropic-shaped `/v1/messages` API.
Do not run workers with different model or sandbox configuration on the same
Temporal Task Queue. Keep credentials in the environment and never commit them.

Only this model endpoint is called: the credential does not need access to a
separate hosted Managed Agents service. Whether a Claude Code-oriented key is
usable therefore depends on whether its gateway permits authenticated
`POST /v1/messages` requests with streaming; the following opt-in smoke tests
answer that directly:

```bash
# Checks the external Messages endpoint only. This makes a real, potentially
# billable request.
make test-model-live

# With the local PostgreSQL and Temporal services running, checks one complete
# durable platform turn against the same model endpoint.
make test-platform-live
```

These tests run only when invoked through the live targets, do not enable tools,
and never print the API key. They are excluded from public CI because external
credentials, availability, latency, and cost are not deterministic. Use a newly
issued key if a credential has ever appeared in chat, logs, or shell history.

If you understand the risk and deliberately want a real model against the local
sandbox during development, set `MANAGED_AGENT_ALLOW_UNSAFE_LOCAL_SANDBOX=1` to
override the startup guard. This is a dev-only escape hatch — the local sandbox
runs tool commands on the host with no isolation, so never use it with untrusted
input or in production.

## Reusable local credentials

Development secrets should not be committed or copied between worktrees.
Create a user-local file once, then edit the values you actually use:

```bash
make dev-env-init
$EDITOR ~/.config/mango/dev.env
```

Run a command with that environment explicitly:

```bash
scripts/with-dev-env go run ./cmd/managed-agent orchestrate
```

The wrapper requires the file to have no group or other permissions. Set
`MANGO_ENV_FILE` only when a different repository-external path is needed.

## Clean up

```bash
make local-down
```

This keeps the PostgreSQL volume. Add `VOLUMES=1` only when you intentionally
want to delete local data. PostgreSQL schema changes are applied by embedded,
versioned `goose` migrations when API or worker processes start.
