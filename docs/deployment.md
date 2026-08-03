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

## Observability

Both roles emit structured `log/slog` records to stderr. The handler and
minimum level are configuration:

| Variable | Default | Meaning |
| --- | --- | --- |
| `MANAGED_AGENT_LOG_FORMAT` | `text` | `text` for development, `json` for production log pipelines |
| `MANAGED_AGENT_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |

Every record carries the process `role`, and request-scoped records carry the
resolved `request_id` plus a `session_id` where one applies. The `request-id`
response header is unchanged; a well-formed client-supplied `request-id` is now
honored so a caller can stitch its traces to the server's. Records never
include headers, query values, or request bodies.

## Health and readiness

Health endpoints are a Mango deployment choice. The Claude Managed Agents API
documents no health, readiness, or status endpoint, so they are served outside
`/v1` and are not part of the compatibility surface.

| Endpoint | Meaning |
| --- | --- |
| `GET /healthz` | Liveness. Cheap, never probes a dependency, so an outage does not cause healthy processes to be restarted. |
| `GET /readyz` | Readiness. Probes PostgreSQL, Temporal, and NATS, and returns `503` with a body naming the failing dependency. |

The `orchestrate` worker serves the same two endpoints on its own listener
(`MANAGED_AGENT_WORKER_HEALTH_ADDR`, default `127.0.0.1:8081`) because it has no
other HTTP surface. Probe bounds are configurable with
`MANAGED_AGENT_HEALTH_TIMEOUT` (default `2s`) and `MANAGED_AGENT_HEALTH_CACHE_TTL`
(default `1s`); the cache keeps aggressive readiness polling from amplifying
into dependency load.

`MANAGED_AGENT_SSE_KEEPALIVE_INTERVAL` (default `15s`) sets the idle interval
between SSE comment keepalives on the event stream.

## Production promotion gates

A supported Docker or Kubernetes bundle requires:

1. explicit, versioned schema migration;
2. graceful API shutdown and worker draining;
3. repeatable live conformance for remote sandbox adapters;
4. real PostgreSQL, Temporal, NATS, and sandbox integration tests in CI;
5. versioned images with upgrade and rollback documentation.

Dependency-aware API and worker readiness is implemented: `/readyz` probes
PostgreSQL, Temporal, and NATS in both roles.

Kubernetes packaging will use separate API and worker Deployments from the same
image. Stateful services remain external by default. An Operator is not part
of the initial deployment model and will be considered only if Mango introduces
Kubernetes-native custom resources that require reconciliation.
