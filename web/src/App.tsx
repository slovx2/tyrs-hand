import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Activity,
  MessageCircle,
  Server,
  ShieldCheck,
  Smartphone,
  type LucideIcon,
} from 'lucide-react'
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
import { t } from './i18n'
import { DiscordPage } from './pages/DiscordPage'
import { LoginPage } from './pages/LoginPage'
import { SetupPage } from './pages/SetupPage'
import { WorkersPage } from './pages/WorkersPage'
import {
  WorkerDetailPage,
  WorkerOverviewPage,
  WorkerUsersPage,
} from './pages/WorkerDetailPage'
import { WorkerConfigRoute } from './pages/WorkerConfigPage'
import { WorkerWorkspacePage } from './pages/WorkspacesPage'
import { DevicesPage } from './pages/DevicesPage'
import { UsersPage } from './pages/UsersPage'
import { InvitePage } from './pages/InvitePage'
import { useUI } from './state'

interface SetupStatus {
  setupRequired: boolean
}

interface NavigationItem {
  to: string
  label: string
  icon: LucideIcon
}

interface NavigationGroup {
  label: string
  items: NavigationItem[]
}

const navigation: NavigationGroup[] = [
  {
    label: 'Overview',
    items: [{ to: '/', label: '健康与容量', icon: Activity }],
  },
  {
    label: 'Compute',
    items: [{ to: '/workers', label: 'Worker 与 Workspace', icon: Server }],
  },
  {
    label: 'Clients',
    items: [{ to: '/devices', label: '移动端定时任务', icon: Smartphone }],
  },
  {
    label: 'Integrations',
    items: [{ to: '/settings/discord', label: 'Discord', icon: MessageCircle }],
  },
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
      <Route path="/invite" element={<InvitePage />} />
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
        <Route path="workers" element={<WorkersPage />} />
        <Route path="workers/:workerId" element={<WorkerDetailPage />}>
          <Route index element={<Navigate to="overview" replace />} />
          <Route path="overview" element={<WorkerOverviewPage />} />
          <Route path="codex" element={<WorkerConfigRoute />} />
          <Route path="workspace" element={<WorkerWorkspacePage />} />
          <Route path="users" element={<WorkerUsersPage />} />
        </Route>
        <Route path="devices" element={<DevicesPage />} />
        <Route path="settings/discord" element={<DiscordPage />} />
        <Route path="users" element={<UsersPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

function AuthenticatedLayout() {
  const location = useLocation()
  const locale = useUI((state) => state.locale)
  const setCSRFToken = useUI((state) => state.setCSRFToken)
  const activeNavigationItem = navigation
    .flatMap((group) => group.items)
    .find((item) =>
      item.to === '/'
        ? location.pathname === '/'
        : location.pathname.startsWith(item.to),
    )
  const me = useQuery({
    queryKey: ['me'],
    queryFn: async () => {
      const session = await api<{
        id: string
        username: string
        role: 'admin' | 'user'
        csrfToken: string
        expiresAt: string
      }>('/auth/me')
      setCSRFToken(session.csrfToken)
      return session
    },
    retry: false,
  })
  if (me.isLoading) return <FullPageMessage message={t(locale, 'loading')} />
  if (me.isError)
    return <Navigate to="/login" state={{ from: location }} replace />
  return (
    <div className="app-shell">
      <aside className="app-sidebar">
        <Link to="/" className="brand">
          <img className="brand-logo" src="/tyrs-hand.png" alt="" />
          <span>tyrs-hand</span>
        </Link>
        <nav className="app-navigation" aria-label="主导航">
          {navigation
            .concat(
              me.data?.role === 'admin'
                ? [
                    {
                      label: 'Admin',
                      items: [
                        { to: '/users', label: '用户管理', icon: ShieldCheck },
                      ],
                    },
                  ]
                : [],
            )
            .map((group) => (
              <div className="nav-group" key={group.label}>
                <div className="nav-group-label">{group.label}</div>
                <div className="nav-group-items">
                  {group.items.map((item) => (
                    <NavLink
                      key={item.to}
                      to={item.to}
                      end={item.to === '/'}
                      aria-label={item.label}
                      className={({ isActive }) =>
                        `nav-item ${isActive ? 'nav-item-active' : ''}`
                      }
                    >
                      <item.icon
                        size={16}
                        strokeWidth={1.8}
                        aria-hidden="true"
                      />
                      <span>{item.label}</span>
                    </NavLink>
                  ))}
                </div>
              </div>
            ))}
        </nav>
        <div className="sidebar-account">
          <div className="sidebar-account-icon" aria-hidden="true">
            {me.data?.username.slice(0, 1).toUpperCase()}
          </div>
          <div className="sidebar-account-copy">
            <strong>{me.data?.username}</strong>
            <span>{me.data?.role === 'admin' ? '系统管理员' : '普通用户'}</span>
          </div>
          <LogoutButton onLogout={() => setCSRFToken(undefined)} />
        </div>
      </aside>
      <div className="app-main">
        <header className="app-topbar">
          <div className="app-breadcrumb">
            控制台 <span>/</span>{' '}
            <strong>{activeNavigationItem?.label ?? '管理后台'}</strong>
          </div>
          <div className="control-status">
            <ShieldCheck size={14} strokeWidth={2} aria-hidden="true" />
            <span>Control 正常</span>
          </div>
        </header>
        <main className="app-page">
          <div className="app-content">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}

function Dashboard() {
  const resources = ['workers'] as const
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
      <p className="muted mt-2">Worker 与 Discord 工作区运行状态。</p>
      <div className="mt-8 grid gap-4 sm:grid-cols-1">
        {resources.map((resource, index) => (
          <div className="panel" key={resource}>
            <div className="muted text-xs font-medium tracking-[0.14em] uppercase">
              {resource}
            </div>
            <div className="mt-3 text-4xl font-semibold tracking-[-0.05em]">
              {queries[index].data?.items?.length ?? '—'}
            </div>
          </div>
        ))}
      </div>
      <div className="danger-note mt-6">
        Agent 默认拥有工作区写权限和公网访问能力。平台密钥与数据库凭据不会注入
        Agent 环境。
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
      disabled={mutation.isPending}
      onClick={() => mutation.mutate()}
    >
      {mutation.isPending ? '退出中…' : t(useUI.getState().locale, 'signOut')}
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
