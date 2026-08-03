---
title: Deployment model
slug: /deployment
sidebar_position: 4
---

# Deployment model

Mango currently publishes a reproducible local stack and builds a multi-role
application image. It does not yet publish a supported production Docker
Compose bundle or Kubernetes chart.

## Supported assets

| Asset | Status | Intended use |
| --- | --- | --- |
| Root `Dockerfile` | Buildable | Produce the API/worker image on Linux AMD64 or ARM64 |
| `deployments/local/compose.yaml` | Development | Run PostgreSQL, Temporal, NATS, API, and worker from the current checkout |
| Production Docker Compose | Planned | Supported single-host installation using versioned release images |
| Helm chart | Planned | Kubernetes API and worker deployments with external stateful dependencies |

The local stack is intentionally complete so contributors can exercise the
durable path without installing each dependency. It contains development
credentials, fixed host ports, and stateful dependencies and must not be
treated as a high-availability or hardened production configuration.

## Process topology

One immutable image serves two independently scalable roles:

```text
managed-agent serve -addr :8080
managed-agent orchestrate
```

The API owns HTTP resources, SSE, and event admission. The worker owns Temporal
Workflow/Activity execution, model calls, sandbox tools, and the outbox relay.
They share a release artifact but not a scaling or rollout policy.

Before production deployment bundles are promoted, database migration will be
removed from normal API/worker startup and exposed as an explicit one-shot
role. This avoids every replica racing to manage schema during a rollout.

## Authentication

Authentication is deployment configuration, not a flag. `serve` enforces API
keys whenever `MANAGED_AGENT_API_KEYS` is set:

```sh
export MANAGED_AGENT_API_KEYS='ops-2026-08:REDACTED,ci-2026-08:REDACTED'
```

Each entry is `<key-id>:<secret>`. The key id is a stable, non-secret label; it
is the only part written to logs and it is what a request's resolved principal
carries. Configure two entries to rotate a key without a gap. Keys are held as
SHA-256 digests and compared in constant time.

| Situation | Behavior |
| --- | --- |
| `MANAGED_AGENT_API_KEYS` set | Every request except `GET /healthz` and `GET /readyz` must present an accepted `x-api-key`; a missing or unknown key gets `401 authentication_error` |
| Unset, no `-strict` | Authentication disabled, startup warning logged, loopback bind by default — the zero-config local development path |
| Unset, with `-strict` | `serve` refuses to start |

`MANAGED_AGENT_AUTH_ALLOW_AUTHORIZATION_HEADER=true` additionally accepts
`authorization: Bearer <key>`. That header is not documented upstream as an
alternative to `x-api-key`, so it is a Mango extension and stays off by default.

Mango does not implement inbound rate limiting and emits no rate-limit response
headers; the published Managed Agents limits are Anthropic organization policy.

## Runtime limits

Every limit below has a working default, so an existing deployment needs no new
configuration. Each is deployment tuning and none affects the public API wire.

### API

| Variable | Default | Purpose |
| --- | --- | --- |
| `MANAGED_AGENT_SHUTDOWN_TIMEOUT` | `30s` | Drain window for `srv.Shutdown` on SIGINT/SIGTERM |

On shutdown the API first tells open SSE streams to end at a frame boundary, so
they close cleanly instead of holding the server non-idle until the deadline and
then being severed. Ordinary in-flight requests keep the whole window.

### PostgreSQL pool

| Variable | Default | Purpose |
| --- | --- | --- |
| `MANAGED_AGENT_DB_MAX_CONNS` | `10` | Maximum pooled connections per process |
| `MANAGED_AGENT_DB_MIN_CONNS` | `2` | Warm connections kept open |
| `MANAGED_AGENT_DB_MAX_CONN_LIFETIME` | `30m` | Recycle age for a pooled connection |
| `MANAGED_AGENT_DB_MAX_CONN_IDLE_TIME` | `5m` | Idle age before a connection is closed |
| `MANAGED_AGENT_DB_HEALTH_CHECK_PERIOD` | `1m` | Pool health-check interval |
| `MANAGED_AGENT_DB_STATEMENT_TIMEOUT` | `30s` | Server-side `statement_timeout`; `0` leaves it unset |

**Precedence:** a pool parameter already present in
`MANAGED_AGENT_DATABASE_URL` wins. `pool_max_conns`, `pool_min_conns`,
`pool_max_conn_lifetime`, `pool_max_conn_idle_time`,
`pool_health_check_period`, and `statement_timeout` carried by the URL are
authoritative, and the matching variable is ignored for that parameter only.

Migrations run under the same `statement_timeout`. Raise it, or set it to `0`,
before applying a migration expected to exceed it.

Size `MANAGED_AGENT_DB_MAX_CONNS` against the server's `max_connections`:
API replicas plus worker replicas, each multiplied by their pool maximum, must
fit inside it.

### Temporal worker

| Variable | Default | Purpose |
| --- | --- | --- |
| `MANAGED_AGENT_WORKER_MAX_CONCURRENT_ACTIVITIES` | `32` | Simultaneous Activity executions |
| `MANAGED_AGENT_WORKER_MAX_CONCURRENT_WORKFLOW_TASKS` | `32` | Simultaneous Workflow task executions |
| `MANAGED_AGENT_WORKER_ACTIVITY_POLLERS` | `2` | Activity task pollers |
| `MANAGED_AGENT_WORKER_WORKFLOW_POLLERS` | `2` | Workflow task pollers |
| `MANAGED_AGENT_WORKER_DRAIN_TIMEOUT` | `30s` | Wait for in-flight Activities on shutdown |

The Temporal SDK default is 1,000 concurrent Activity executions, which here
would mean up to 1,000 concurrent model calls, sandbox commands, and PostgreSQL
transactions from a single process. Concurrent Activities should stay well
inside `MANAGED_AGENT_DB_MAX_CONNS` and the sandbox provider's own capacity.

On SIGINT/SIGTERM the worker stops polling and waits up to
`MANAGED_AGENT_WORKER_DRAIN_TIMEOUT` for in-flight Activities. The wait itself
is bounded, so an Activity that ignores cancellation cannot block process exit;
anything still running when the bound elapses is retried on another worker.

Retry policies, Activity timeouts, and task queue names are orchestration
semantics and are not configured here.

## Repository commands

Run core checks:

```sh
make verify
```

Build and smoke-test the container entrypoint:

```sh
make image-smoke
```

Builders behind a restricted network can pass a standard Go module proxy
without changing the Dockerfile:

```sh
make image-smoke GOPROXY=https://proxy.example.com,direct
```

Validate and start the local stack:

```sh
make local-config
make local-up
make local-health
```

Stop it while retaining PostgreSQL data:

```sh
make local-down
```

Set `VOLUMES=1` only when local data should be removed:

```sh
make local-down VOLUMES=1
```

## Production promotion gates

A supported Docker or Kubernetes bundle requires:

1. explicit, versioned schema migration;
2. dependency-aware API and worker readiness;
3. graceful API shutdown and worker draining;
4. repeatable live conformance for remote sandbox adapters;
5. real PostgreSQL, Temporal, NATS, and sandbox integration tests in CI;
6. versioned images with upgrade and rollback documentation.

Kubernetes packaging will use separate API and worker Deployments from the same
image. Stateful services remain external by default. An Operator is not part
of the initial deployment model and will be considered only if Mango introduces
Kubernetes-native custom resources that require reconciliation.
