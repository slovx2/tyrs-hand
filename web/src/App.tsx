import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Activity,
  AppWindow,
  Bot,
  BriefcaseBusiness,
  ClipboardList,
  FileText,
  FolderGit2,
  GitBranch,
  GitFork,
  KeyRound,
  MessageCircle,
  MessagesSquare,
  ScrollText,
  Server,
  ShieldCheck,
  SlidersHorizontal,
  Workflow,
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
import { GitHubPage } from './pages/GitHubPage'
import { DiscordPage } from './pages/DiscordPage'
import { LoginPage } from './pages/LoginPage'
import { ResourcePage } from './pages/ResourcePage'
import { SetupPage } from './pages/SetupPage'
import { SettingsPage } from './pages/SettingsPage'
import { GitHubAgentSettingsPage } from './pages/GitHubAgentSettingsPage'
import { WorkersPage } from './pages/WorkersPage'
import { SSHPage } from './pages/SSHPage'
import { useUI } from './state'

interface SetupStatus {
  setupRequired: boolean
  githubConfigured: boolean
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
    label: 'Integrations',
    items: [
      { to: '/settings/github', label: 'GitHub App', icon: GitFork },
      { to: '/installations', label: 'Installations', icon: AppWindow },
      { to: '/repositories', label: '仓库', icon: FolderGit2 },
      { to: '/trigger-rules', label: '触发规则', icon: Workflow },
      { to: '/agent-profiles', label: 'Agent Profiles', icon: Bot },
      {
        to: '/settings/github-agent',
        label: 'GitHub Agent 参数',
        icon: SlidersHorizontal,
      },
      {
        to: '/settings/github-agent-instructions',
        label: 'GitHub Agent 指令',
        icon: FileText,
      },
      { to: '/settings/discord', label: 'Discord', icon: MessageCircle },
    ],
  },
  {
    label: 'Access',
    items: [{ to: '/ssh', label: '出站 SSH', icon: KeyRound }],
  },
  {
    label: 'Operations',
    items: [
      { to: '/work-items', label: 'Work Items', icon: ClipboardList },
      { to: '/threads', label: 'Thread / Turn', icon: MessagesSquare },
      { to: '/jobs', label: 'Jobs', icon: BriefcaseBusiness },
      { to: '/worktrees', label: '缓存 / Worktree', icon: GitBranch },
      { to: '/audit-logs', label: '审计日志', icon: ScrollText },
    ],
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
          element={
            <ResourcePage
              resource="trigger-rules"
              title="触发规则"
              description="默认使用评论第一行 /tyrs-hand 命令和 tyrs-hand Label；全文 mention 仅作为默认关闭的兼容类型。"
            />
          }
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
        <Route path="workers" element={<WorkersPage />} />
        <Route path="ssh" element={<SSHPage />} />
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
        <Route
          path="settings/github-agent"
          element={<GitHubAgentSettingsPage />}
        />
        <Route
          path="settings/github-agent-instructions"
          element={<SettingsPage />}
        />
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
        username: string
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
          {navigation.map((group) => (
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
                    <item.icon size={16} strokeWidth={1.8} aria-hidden="true" />
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
            <span>系统管理员</span>
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
              {queries[index].data?.items?.length ?? '—'}
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
