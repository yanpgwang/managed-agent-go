# Repository instructions

Follow `CONTRIBUTING.md` for testing, compatibility, durability, security,
documentation, and pull-request requirements.

## Product boundary

- Mango is an independent, self-hosted implementation of the documented Claude
  Managed Agents contract. It must never proxy or delegate Sessions, Files,
  sandbox execution, scheduling, persistence, or other Managed Agents behavior
  to Anthropic's hosted Managed Agents service.
- The only required external AI dependency is an Anthropic Messages-compatible
  endpoint. Runtime model execution calls `/v1/messages` with one external
  model credential; the endpoint, model, and authentication style are ordinary
  configuration rather than additional credentials.
- Official CMA documentation and the pinned SDK are specification and client
  compatibility sources, not runtime dependencies. Development, CI, and
  production must not require hosted Managed Agents credentials.

## Compatibility-driven development

- The stable [Claude Managed Agents documentation](https://platform.claude.com/docs/en/managed-agents/overview)
  and the pinned official Go SDK define the target contract. Do not maintain a
  separate Mango product roadmap.
- `docs/compatibility.md` is the current delta ledger. GitHub Issues define
  active engineering work.
- Before implementing substantial compatibility work, read only the relevant
  official CMA guide, API reference, and compatibility row.
- Select one user-visible, end-to-end gap. State its acceptance criteria and
  non-goals before implementation.
- Prefer stable CMA workflows. Do not implement research-preview capabilities
  unless the task explicitly selects them.
- Implement the smallest safe slice that closes the selected gap. Expand
  internal architecture only when required for observable correctness,
  durability, recovery, or security.
- When public CMA behavior is ambiguous, choose a conservative self-hosted
  behavior, document the limitation, and test the official SDK against Mango.
  Observation of a hosted upstream service is optional clean-room research,
  requires an explicitly authorized task, and cannot gate implementation,
  testing, deployment, or operation.
- Stop when the acceptance criteria and required tests pass. Record adjacent
  gaps as separate Issues instead of expanding the current change.
- A completed compatibility change must update the affected API documentation
  and `docs/compatibility.md`.
