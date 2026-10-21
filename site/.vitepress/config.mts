import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'Upsera',
  description:
    'Self-hosted uptime monitoring and status pages for teams. Multi-region checks, fewer false alarms.',
  base: process.env.DOCS_BASE ?? '/upsera/',
  cleanUrls: true,
  lastUpdated: false,
  themeConfig: {
    nav: [
      { text: 'Guide', link: '/guide/getting-started' },
      { text: 'Comparison', link: '/comparison' },
      { text: 'GitHub', link: 'https://github.com/davidsugianto/upsera' },
    ],
    sidebar: [
      {
        text: 'Start',
        items: [
          { text: 'Getting started', link: '/guide/getting-started' },
          { text: 'Configuration', link: '/guide/configuration' },
          { text: 'HTTPS with Caddy', link: '/guide/https' },
          { text: 'Backups', link: '/guide/backups' },
        ],
      },
      {
        text: 'Monitoring',
        items: [
          { text: 'Monitors', link: '/guide/monitors' },
          { text: 'Alerting', link: '/guide/alerting' },
          { text: 'Status pages', link: '/guide/status-pages' },
          { text: 'Multi-location probes', link: '/guide/probes' },
          { text: 'Degraded state and SLOs', link: '/guide/slos' },
        ],
      },
      {
        text: 'Automation',
        items: [
          { text: 'Monitors as code (upsctl)', link: '/guide/upsctl' },
          { text: 'Prometheus metrics', link: '/guide/metrics' },
        ],
      },
      {
        text: 'More',
        items: [{ text: 'How it compares', link: '/comparison' }],
      },
    ],
    socialLinks: [{ icon: 'github', link: 'https://github.com/davidsugianto/upsera' }],
    search: { provider: 'local' },
    editLink: {
      pattern: 'https://github.com/davidsugianto/upsera/edit/master/site/:path',
      text: 'Edit this page on GitHub',
    },
    footer: { message: 'Released under the MIT licence.' },
  },
})
