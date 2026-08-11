import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'Mango',
  tagline: 'A self-hosted implementation of the Claude Managed Agents API',
  favicon: 'img/mango-mark.svg',

  future: {
    v4: true,
  },

  url: process.env.DOCS_URL ?? 'https://yanpgwang.github.io',
  baseUrl: process.env.DOCS_BASE_URL ?? '/managed-agent-go/',
  organizationName: 'yanpgwang',
  projectName: 'managed-agent-go',

  onBrokenLinks: 'throw',
  markdown: {
    mermaid: true,
  },
  themes: ['@docusaurus/theme-mermaid'],

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          path: '../docs',
          routeBasePath: '/',
          sidebarPath: './sidebars.ts',
          showLastUpdateTime: true,
          showLastUpdateAuthor: true,
          editUrl: ({docPath}) =>
            `https://github.com/yanpgwang/managed-agent-go/edit/main/docs/${docPath.replace(
              /^(\.\.\/docs\/)+/,
              '',
            )}`,
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'Mango',
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docs',
          position: 'left',
          label: 'Documentation',
        },
        {
          href: 'https://github.com/yanpgwang/managed-agent-go',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Documentation',
          items: [
            {
              label: 'Getting started',
              to: '/getting-started',
            },
            {
              label: 'Architecture',
              to: '/architecture',
            },
            {
              label: 'Multi-agent guide',
              to: '/guides/multi-agent',
            },
            {
              label: 'API reference',
              to: '/api',
            },
          ],
        },
        {
          title: 'Resources',
          items: [
            {
              label: 'API compatibility',
              to: '/compatibility',
            },
            {
              label: 'Releases',
              href: 'https://github.com/yanpgwang/managed-agent-go/releases',
            },
            {
              label: 'Roadmap',
              to: '/roadmap',
            },
            {
              label: 'GitHub',
              href: 'https://github.com/yanpgwang/managed-agent-go',
            },
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} managed-agent-go contributors. Apache-2.0.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
