import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'managed-agent-go',
  tagline: 'A self-hosted implementation of the Claude Managed Agents API',

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
      title: 'managed-agent-go',
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
              label: 'API reference',
              to: '/api',
            },
          ],
        },
        {
          title: 'Project',
          items: [
            {
              label: 'Compatibility',
              to: '/compatibility',
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
