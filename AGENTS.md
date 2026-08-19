# Repository instructions

Follow `CONTRIBUTING.md` for testing, compatibility, durability, security,
documentation, and pull-request requirements.

## Compatibility-driven development

- The stable [Claude Managed Agents documentation](https://platform.claude.com/docs/en/managed-agents/overview)
  and the pinned official Go SDK define the upstream target. Do not maintain a
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
- When the public documentation is ambiguous, record the ambiguity and use the
  smallest relevant black-box observation instead of inferring private
  implementation details.
- Stop when the acceptance criteria and required tests pass. Record adjacent
  gaps as separate Issues instead of expanding the current change.
- A completed compatibility change must update the affected API documentation
  and `docs/compatibility.md`.
