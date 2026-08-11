---
title: Releases
slug: /releases
---

# Releases

Mango has not published a versioned release yet. The repository is an alpha
development line: use a pinned commit when evaluating it and read
[API compatibility](compatibility.md) before depending on a capability.

Documentation-site package metadata is private build tooling and is not the
Mango product version. Conformance baselines name the upstream beta contract
and SDK used as evidence; they are also not product releases.

## Version policy

The first tag will be a pre-1.0 SemVer release. A release will bind together:

- one Git tag and immutable source revision;
- versioned API and worker container images;
- database migration and rollback instructions;
- a compatibility summary and known limitations;
- upgrade ordering for API and Temporal worker roles;
- checksums and release notes.

Until that exists, documents must not describe a compatibility snapshot or a
date as a published Mango version.

## Promotion gates

The first developer-preview release requires a reproducible image, a passing
90-operation conformance suite, a real-model smoke path, coherent user
documentation, and explicit alpha limitations.

A production-readiness claim additionally requires authentication and tenant
isolation, Worker Versioning and rolling-upgrade evidence, observability,
backup/restore guidance, quotas, production deployment manifests, and the
remaining distributed reconciliation boundaries described in the
[roadmap](roadmap.md).
