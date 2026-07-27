import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docs: [
    'intro',
    'getting-started',
    {
      type: 'category',
      label: 'Architecture',
      items: [
        'architecture',
        'architecture/domain-model',
        'architecture/session-lifecycle',
        'architecture/runtime-and-sandbox',
      ],
    },
    {
      type: 'category',
      label: 'API',
      items: [
        'api/overview',
        'api/agents',
        'api/environments',
        'api/sessions',
        'api/events',
      ],
    },
    {
      type: 'category',
      label: 'Project',
      items: ['compatibility', 'roadmap', 'provenance'],
    },
  ],
};

export default sidebars;
