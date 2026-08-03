# Upstream Claude Managed Agents documentation

This directory keeps a local development snapshot of the official English
Claude Managed Agents (CMA) documentation. It gives coding agents an
inspectable, versioned compatibility reference without making hosted network
access a prerequisite for every task.

The snapshot contains:

- the complete `Managed Agents` guide section listed by Anthropic's
  [`llms.txt`](https://platform.claude.com/llms.txt) catalog; and
- the related public Beta API reference families for agents, environments,
  sessions, files, skills, memory stores, vaults, deployments, dreams,
  tunnels, user profiles, webhooks, and self-hosted work.

Start at [`snapshot/INDEX.md`](snapshot/INDEX.md). Refresh all generated files
from their official `.md` endpoints with:

```bash
./scripts/sync-cma-docs.sh
```

Files below `snapshot/` are generated upstream reference material. Do not edit
them by hand. If the local snapshot conflicts with the currently hosted
documentation, the hosted official documentation wins.

The upstream material is authored and copyrighted by Anthropic. It is retained
with source URLs and checksums in `snapshot/MANIFEST.tsv` and is not covered by
Mango's Apache-2.0 license. Its presence is not an Anthropic endorsement and is
not, by itself, proof of API compatibility.
