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

## Production promotion gates

A supported Docker or Kubernetes bundle requires:

1. explicit, versioned schema migration;
2. dependency-aware API and worker readiness;
3. graceful API shutdown and worker draining;
4. remote sandbox adapters and orphan reconciliation;
5. real PostgreSQL, Temporal, NATS, and sandbox integration tests in CI;
6. versioned images with upgrade and rollback documentation.

Kubernetes packaging will use separate API and worker Deployments from the same
image. Stateful services remain external by default. An Operator is not part
of the initial deployment model and will be considered only if Mango introduces
Kubernetes-native custom resources that require reconciliation.
