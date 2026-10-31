import { defineConfig } from 'vitepress'

const SITE_URL = 'https://runbox.xyun.dev'
const SITE_NAME = 'Runbox'
const SITE_DESC =
  '专为单机服务器打造的轻量 Agent 沙箱。一个容器承载多个身份，每个身份拥有独立 UID、私有工作区、配额和持久安装环境：上传、执行命令、按路径取回产物，不需要编排器或外部数据库。'
const OG_IMAGE = `${SITE_URL}/assets/og-image.png`

const softwareJsonLd = {
  '@context': 'https://schema.org',
  '@type': 'SoftwareApplication',
  name: SITE_NAME,
  alternateName: 'Run in a box',
  description: SITE_DESC,
  url: SITE_URL,
  downloadUrl: 'https://github.com/xy200303/runbox/releases/latest',
  softwareVersion: '0.2.2',
  applicationCategory: 'DeveloperApplication',
  operatingSystem: 'Linux (container)',
  inLanguage: 'zh-CN',
  isAccessibleForFree: true,
  offers: { '@type': 'Offer', price: '0', priceCurrency: 'CNY' },
  license: 'https://opensource.org/licenses/MIT',
  author: { '@type': 'Person', name: 'xy200303', url: 'https://github.com/xy200303' },
  keywords:
    'agent sandbox,代码执行沙箱,AI Agent 沙箱,单机沙箱,文件隔离,runbox,dify-sandbox,轻量沙箱',
  featureList:
    '单容器多身份,按 UID 的文件隔离,持久 pip/npm 环境,路径式文件下载,tar 批量下载,流式上传,Range 下载,SQLite 身份状态,Prometheus 指标'
}

const websiteJsonLd = {
  '@context': 'https://schema.org',
  '@type': 'WebSite',
  name: SITE_NAME,
  alternateName: 'Run in a box',
  url: SITE_URL,
  inLanguage: 'zh-CN',
  description: SITE_DESC
}

export default defineConfig({
  lang: 'zh-CN',
  title: SITE_NAME,
  titleTemplate: ':title | Runbox',
  description: SITE_DESC,
  appearance: 'force-light',
  base: '/',
  lastUpdated: true,
  sitemap: { hostname: SITE_URL },
  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: '/assets/logo.svg' }],
    ['meta', { name: 'author', content: 'xy200303' }],
    ['meta', { name: 'robots', content: 'index, follow, max-image-preview:large' }],
    ['meta', { name: 'theme-color', content: '#0d1117' }],
    ['meta', { property: 'og:type', content: 'website' }],
    ['meta', { property: 'og:site_name', content: SITE_NAME }],
    ['meta', { property: 'og:locale', content: 'zh_CN' }],
    ['meta', { property: 'og:title', content: 'Runbox — Run in a box.' }],
    ['meta', { property: 'og:description', content: SITE_DESC }],
    ['meta', { property: 'og:url', content: SITE_URL }],
    ['meta', { property: 'og:image', content: OG_IMAGE }],
    ['meta', { property: 'og:image:width', content: '1200' }],
    ['meta', { property: 'og:image:height', content: '630' }],
    ['meta', { property: 'og:image:alt', content: 'Runbox — Run in a box.' }],
    ['meta', { name: 'twitter:card', content: 'summary_large_image' }],
    ['meta', { name: 'twitter:title', content: 'Runbox — Run in a box.' }],
    ['meta', { name: 'twitter:description', content: SITE_DESC }],
    ['meta', { name: 'twitter:image', content: OG_IMAGE }],
    ['script', { type: 'application/ld+json' }, JSON.stringify(softwareJsonLd)],
    ['script', { type: 'application/ld+json' }, JSON.stringify(websiteJsonLd)]
  ],
  transformHead({ pageData }) {
    const path = pageData.relativePath.replace(/index\.md$/, '').replace(/\.md$/, '')
    return [['link', { rel: 'canonical', href: `${SITE_URL}/${path}` }]]
  },
  themeConfig: {
    logo: '/assets/logo.svg',
    siteTitle: 'Runbox',
    nav: [
      { text: '首页', link: '/' },
      { text: '文档', link: '/guide/quickstart' },
      { text: '接口', link: '/guide/api' },
      { text: '部署', link: '/guide/deploy' }
    ],
    sidebar: {
      '/guide/': [
        {
          text: '上手',
          items: [
            { text: '快速开始', link: '/guide/quickstart' },
            { text: '身份模型', link: '/guide/identity' }
          ]
        },
        {
          text: '接口与部署',
          items: [
            { text: 'HTTP 接口', link: '/guide/api' },
            { text: '部署与加固', link: '/guide/deploy' }
          ]
        }
      ]
    },
    socialLinks: [{ icon: 'github', link: 'https://github.com/xy200303/runbox' }],
    outline: { label: '本页目录' },
    docFooter: { prev: '上一页', next: '下一页' },
    lastUpdated: { text: '最后更新' },
    footer: {
      message:
        'MIT 许可 · <a href="/guide/quickstart">快速开始</a> · <a href="/guide/identity">身份模型</a> · <a href="/guide/api">HTTP 接口</a> · <a href="/guide/deploy">部署与加固</a> · <a href="https://github.com/xy200303/runbox">源码</a> · <a href="https://github.com/xy200303/runbox/issues">反馈</a>',
      copyright: 'Copyright © 2026 Runbox contributors'
    }
  }
})
