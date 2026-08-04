# Deployment assets

This directory contains deployment-specific assets, grouped by their support
level rather than by container technology.

| Directory | Support level | Purpose |
| --- | --- | --- |
| [`local`](local/) | Development | Reproducible PostgreSQL, Temporal, NATS, MinIO, API, and worker stack for local development and integration tests |

`deployments/local` is the only committed deployment bundle today. It builds
the current checkout and is not a production manifest.

Future deployment bundles are added only when their lifecycle is tested and
documented:

- `deployments/docker` will be a supported single-host installation that pulls
  versioned release images instead of building source;
- `charts/mango` will contain the independently versioned Helm chart;
- `deployments/kind` may contain end-to-end cluster fixtures that are not
  production defaults.

The release topology uses one immutable Mango image with separate process
roles. API and worker capacity must remain independently scalable, and schema
migration will become an explicit one-shot role before production bundles are
published. Production manifests should reference external PostgreSQL,
Temporal, NATS, and object storage by default rather than installing stateful
dependencies implicitly.

Use the repository-level `Makefile` for stable commands:

```sh
make image
make image-smoke
make local-config
make local-up
make local-health
make local-down
```

See [Deployment model](../docs/deployment.md) for the current guarantees and
the promotion criteria for Docker and Kubernetes assets.
