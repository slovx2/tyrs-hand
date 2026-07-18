import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import {
  Link,
  Navigate,
  NavLink,
  Outlet,
  Route,
  Routes,
  useLocation,
} from 'react-router'
import { api } from './api/client'
import { t, type MessageKey } from './i18n'
import { GitHubPage } from './pages/GitHubPage'
import { DiscordPage } from './pages/DiscordPage'
import { LoginPage } from './pages/LoginPage'
import { ResourcePage } from './pages/ResourcePage'
import { SetupPage } from './pages/SetupPage'
import { SettingsPage } from './pages/SettingsPage'
import { useUI } from './state'

interface SetupStatus {
  setupRequired: boolean
  githubConfigured: boolean
}

const navigation: Array<{ to: string; label: MessageKey }> = [
  { to: '/', label: 'overview' },
  { to: '/repositories', label: 'repositories' },
  { to: '/installations', label: 'github' },
  { to: '/trigger-rules', label: 'rules' },
  { to: '/agent-profiles', label: 'profiles' },
  { to: '/work-items', label: 'workItems' },
  { to: '/threads', label: 'workItems' },
  { to: '/jobs', label: 'jobs' },
  { to: '/workers', label: 'workers' },
  { to: '/worktrees', label: 'workers' },
  { to: '/audit-logs', label: 'audit' },
  { to: '/settings/github', label: 'github' },
  { to: '/settings/discord', label: 'discord' },
  { to: '/settings', label: 'settings' },
]

export function App() {
  const theme = useUI((state) => state.theme)
  useEffect(() => {
    document.documentElement.classList.toggle('dark', theme === 'dark')
    document.documentElement.dataset.theme = theme
  }, [theme])

  const setup = useQuery({
    queryKey: ['setup-status'],
    queryFn: () => api<SetupStatus>('/setup/status'),
  })
  if (setup.isLoading) return <FullPageMessage message="正在检查系统状态…" />
  if (setup.isError)
    return <FullPageMessage message={(setup.error as Error).message} error />
  return (
    <Routes>
      <Route path="/setup" element={<SetupPage />} />
      <Route path="/login" element={<LoginPage />} />
      <Route
        element={
          setup.data?.setupRequired ? (
            <Navigate to="/setup" replace />
          ) : (
            <AuthenticatedLayout />
          )
        }
      >
        <Route index element={<Dashboard />} />
        <Route
          path="repositories"
          element={<ResourcePage resource="repositories" title="仓库" />}
        />
        <Route
          path="installations"
          element={
            <ResourcePage
              resource="installations"
              title="GitHub Installation"
            />
          }
        />
        <Route
          path="trigger-rules"
          element={<ResourcePage resource="trigger-rules" title="触发规则" />}
        />
        <Route
          path="agent-profiles"
          element={
            <ResourcePage resource="agent-profiles" title="Agent 配置" />
          }
        />
        <Route
          path="work-items"
          element={<ResourcePage resource="work-items" title="工作项" />}
        />
        <Route
          path="threads"
          element={<ResourcePage resource="threads" title="Thread / Turn" />}
        />
        <Route
          path="jobs"
          element={<ResourcePage resource="jobs" title="任务与尝试" />}
        />
        <Route
          path="workers"
          element={<ResourcePage resource="workers" title="Worker" />}
        />
        <Route
          path="worktrees"
          element={
            <ResourcePage resource="worktrees" title="缓存与 Worktree" />
          }
        />
        <Route
          path="audit-logs"
          element={<ResourcePage resource="audit-logs" title="审计日志" />}
        />
        <Route path="settings/github" element={<GitHubPage />} />
        <Route path="settings/discord" element={<DiscordPage />} />
        <Route path="settings" element={<SettingsPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

function AuthenticatedLayout() {
  const location = useLocation()
  const locale = useUI((state) => state.locale)
  const setCSRFToken = useUI((state) => state.setCSRFToken)
  const me = useQuery({
    queryKey: ['me'],
    queryFn: () => api<{ username: string; expiresAt: string }>('/auth/me'),
    retry: false,
  })
  if (me.isLoading) return <FullPageMessage message={t(locale, 'loading')} />
  if (me.isError)
    return <Navigate to="/login" state={{ from: location }} replace />
  return (
    <div className="min-h-screen lg:grid lg:grid-cols-[250px_1fr]">
      <aside className="app-sidebar border-b px-4 py-5 lg:border-r lg:border-b-0">
        <Link to="/" className="brand mb-7 text-xl">
          tyrs-hand
        </Link>
        <nav className="grid grid-cols-2 gap-1 sm:grid-cols-4 lg:grid-cols-1">
          {navigation.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.to === '/'}
              className={({ isActive }) =>
                `nav-item px-3 py-2 text-sm ${isActive ? 'nav-item-active' : ''}`
              }
            >
              {item.to === '/threads'
                ? 'Thread / Turn'
                : item.to === '/worktrees'
                  ? '缓存 / Worktree'
                  : t(locale, item.label)}
            </NavLink>
          ))}
        </nav>
        <div className="muted mt-8 text-xs">{me.data?.username}</div>
        <LogoutButton onLogout={() => setCSRFToken(undefined)} />
      </aside>
      <main className="min-w-0 p-4 sm:p-8 lg:p-10">
        <Outlet />
      </main>
    </div>
  )
}

function Dashboard() {
  const resources = ['work-items', 'jobs', 'workers'] as const
  const queries = resources.map((resource) =>
    // hooks 数量固定，资源列表不会在运行时变化。
    // eslint-disable-next-line react-hooks/rules-of-hooks
    useQuery({
      queryKey: [resource],
      queryFn: () => api<{ items: unknown[] }>(`/${resource}`),
    }),
  )
  return (
    <section>
      <h1 className="text-3xl font-bold">控制面概览</h1>
      <p className="muted mt-2">GitHub 事件、任务租约和 Codex 运行状态。</p>
      <div className="mt-8 grid gap-4 sm:grid-cols-3">
        {resources.map((resource, index) => (
          <div className="panel" key={resource}>
            <div className="muted text-xs font-medium tracking-[0.14em] uppercase">
              {resource}
            </div>
            <div className="mt-3 text-4xl font-semibold tracking-[-0.05em]">
              {queries[index].data?.items.length ?? '—'}
            </div>
          </div>
        ))}
      </div>
      <div className="danger-note mt-6">
        Agent 默认拥有工作区写权限和公网访问能力。平台密钥、GitHub Token
        与数据库凭据不会注入 Agent 环境。
      </div>
    </section>
  )
}

function LogoutButton({ onLogout }: { onLogout: () => void }) {
  const queryClient = useQueryClient()
  const mutation = useMutation({
    mutationFn: () => api<void>('/auth/logout', { method: 'POST' }),
    onSettled: async () => {
      onLogout()
      await queryClient.resetQueries()
      window.location.assign('/login')
    },
  })
  return (
    <button
      className="muted mt-2 cursor-pointer text-sm hover:underline"
      onClick={() => mutation.mutate()}
    >
      {t(useUI.getState().locale, 'signOut')}
    </button>
  )
}

function FullPageMessage({
  message,
  error = false,
}: {
  message: string
  error?: boolean
}) {
  return (
    <div
      className={`grid min-h-screen place-items-center p-8 ${error ? 'error-text' : ''}`}
    >
      {message}
    </div>
  )
}
