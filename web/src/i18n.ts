import type { Locale } from './state'

const messages = {
  'zh-CN': {
    overview: '概览',
    repositories: '仓库',
    rules: '触发规则',
    profiles: 'Agent 配置',
    workItems: '工作项',
    jobs: '任务',
    workers: 'Worker',
    audit: '审计日志',
    settings: '系统设置',
    github: 'GitHub App',
    discord: 'Discord',
    codex: 'Codex 设置',
    signOut: '退出',
    loading: '正在加载…',
    empty: '暂无数据',
  },
  'en-US': {
    overview: 'Overview',
    repositories: 'Repositories',
    rules: 'Trigger rules',
    profiles: 'Agent profiles',
    workItems: 'Work items',
    jobs: 'Jobs',
    workers: 'Workers',
    audit: 'Audit log',
    settings: 'Settings',
    github: 'GitHub App',
    discord: 'Discord',
    codex: 'Codex settings',
    signOut: 'Sign out',
    loading: 'Loading…',
    empty: 'No data',
  },
} as const

export type MessageKey = keyof (typeof messages)['zh-CN']

export function t(locale: Locale, key: MessageKey): string {
  return messages[locale][key]
}
