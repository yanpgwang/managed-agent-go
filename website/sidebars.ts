import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docs: [
    'intro',
    'getting-started',
    {
      type: 'category',
      label: 'Concepts',
      items: [
        'architecture',
        'architecture/domain-model',
        'architecture/session-lifecycle',
        'architecture/runtime-and-sandbox',
        'architecture/storage-context-and-tools',
      ],
    },
    {
      type: 'category',
      label: 'Guides',
      items: [
        'deployment',
        'sandboxes',
      ],
    },
    {
      type: 'category',
      label: 'API reference',
      items: [
        'api/overview',
        'api/agents',
        'api/environments',
        'api/environment-work',
        'api/sessions',
        'api/events',
        'api/session-threads',
        'api/deployments',
      ],
    },
    {
      type: 'category',
      label: 'Compatibility & conformance',
      items: [
        'compatibility',
        'compatibility/core-v1',
        'api/core-conformance',
        'api/files-conformance',
        'api/skills-conformance',
        'api/memory-conformance',
        'api/vaults-conformance',
        'api/deployments-conformance',
        'api/environment-work-conformance',
        'api/session-threads-conformance',
        'api/session-resources-conformance',
        'provenance',
      ],
    },
    'roadmap',
  ],
};

export default sidebars;
