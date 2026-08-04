# Documentation site

The public documentation is stored in the repository-level `docs/` directory.
This directory contains only the Docusaurus build configuration.

```bash
npm install
npm start
```

The production defaults target GitHub Pages at
`https://yanpgwang.github.io/managed-agent-go/`. Override `DOCS_URL` and
`DOCS_BASE_URL` when building for another host.

The `.github/workflows/pages.yml` workflow builds and deploys the static site
after documentation changes land on `main`, and it can also be started manually.
Configure the repository's Pages publishing source as **GitHub Actions** before
the first deployment.
