import type { Model } from "@codex-app-server/v2/Model";
import type { ServerRequest } from "@codex-app-server/ServerRequest";
import * as Crypto from "expo-crypto";
import { create } from "zustand";

import { ControlApi } from "@/api/control";
import { materializeUserInput, type LocalAttachment } from "@/app-server/attachments";
import { latestCompletedPlan, textInput, type TurnPreferences } from "@/app-server/officialClient";
import { officialClientFor } from "@/app-server/registry";
import { projectForThread, targetKey, type MobileProject, type ThreadRecord } from "@/app-server/types";
import { loadCachedProjects, loadCachedThreads, replaceCachedThreads,
  saveProjects, saveThreadRecord } from "@/db/cache";
import { listConnections, setActiveConnection, type Connection } from "@/db/connections";
import { listSSHProjects } from "@/db/sshProjects";
import { loadThemeMode, saveLastTurnPreferences, saveThemeMode } from "@/db/settings";
import type { ThemeMode } from "@/theme/tokens";

type AppState = {
  ready: boolean;
  refreshing: boolean;
  error: string | null;
  themeMode: ThemeMode;
  connections: Connection[];
  activeConnection: Connection | null;
  projects: MobileProject[];
  threads: ThreadRecord[];
  modelsByTarget: Record<string, Model[]>;
  pendingRequests: Record<string, ServerRequest[]>;
  selectedProjectId: string | null;
  initialize: () => Promise<void>;
  refresh: () => Promise<void>;
  reloadConnections: () => Promise<void>;
  switchConnection: (profileId: string) => Promise<void>;
  setSelectedProject: (projectId: string | null) => void;
  setThemeMode: (mode: ThemeMode) => void;
  loadThread: (threadId: string) => Promise<void>;
  startTask: (projectId: string, text: string, attachments: LocalAttachment[],
    preferences: TurnPreferences, clientMessageId?: string) => Promise<string>;
  submitMessage: (threadId: string, text: string, attachments: LocalAttachment[],
    preferences: TurnPreferences, clientMessageId?: string) => Promise<void>;
  executePlan: (threadId: string, preferences: TurnPreferences) => Promise<void>;
  interruptThread: (threadId: string) => Promise<void>;
  answerRequest: (threadId: string, requestId: string | number, result: unknown) => boolean;
  setThreadArchived: (threadId: string, archived: boolean) => Promise<void>;
  renameThread: (threadId: string, name: string) => Promise<void>;
};

type StoreSet = (partial: Partial<AppState> | ((state: AppState) => Partial<AppState>)) => void;
type StoreGet = () => AppState;

const refreshPromises = new Map<string, Promise<void>>();
const subscribedClients = new WeakSet<object>();
const threadRefreshTimers = new Map<string, ReturnType<typeof setTimeout>>();

export const useAppStore = create<AppState>((set, get) => ({
  ready: false, refreshing: false, error: null, themeMode: "system", connections: [],
  activeConnection: null, projects: [], threads: [], modelsByTarget: {}, pendingRequests: {},
  selectedProjectId: null,

  initialize: async () => {
    const [connections, themeMode] = await Promise.all([listConnections(), loadThemeMode()]);
    const activeConnection = connections.find((item) => item.active) ?? connections[0] ?? null;
    const [projects, threads] = activeConnection ? await Promise.all([
      loadCachedProjects(activeConnection.profileId), loadCachedThreads(activeConnection.profileId),
    ]) : [[], []];
    set({ ready: true, connections, activeConnection, projects, threads, themeMode,
      selectedProjectId: projects[0]?.id ?? null });
    if (activeConnection) void get().refresh();
  },

  refresh: () => {
    const connection = get().activeConnection;
    if (!connection) return Promise.resolve();
    const active = refreshPromises.get(connection.profileId);
    if (active) return active;
    if (get().activeConnection?.profileId === connection.profileId) {
      set({ refreshing: true, error: null });
    }
    const promise: Promise<void> = refreshProfile(connection, set, get).finally(() => {
      refreshPromises.delete(connection.profileId);
      if (get().activeConnection?.profileId === connection.profileId) set({ refreshing: false });
    });
    refreshPromises.set(connection.profileId, promise);
    return promise;
  },

  reloadConnections: async () => {
    const connections = await listConnections();
    const current = get().activeConnection;
    if (!current || !connections.some((item) => item.profileId === current.profileId)) {
      const activeConnection = connections.find((item) => item.active) ?? connections[0] ?? null;
      const [projects, threads] = activeConnection ? await Promise.all([
        loadCachedProjects(activeConnection.profileId), loadCachedThreads(activeConnection.profileId),
      ]) : [[], []];
      set({ connections, activeConnection, projects, threads, modelsByTarget: {},
        pendingRequests: {}, selectedProjectId: projects[0]?.id ?? null });
      if (activeConnection) void get().refresh();
      return;
    }
    set({ connections, activeConnection: connections.find((item) =>
      item.profileId === current.profileId) ?? current });
  },

  switchConnection: async (profileId) => {
    await setActiveConnection(profileId);
    const connections = await listConnections();
    const activeConnection = connections.find((item) => item.profileId === profileId) ?? null;
    const [projects, threads] = activeConnection ? await Promise.all([
      loadCachedProjects(profileId), loadCachedThreads(profileId),
    ]) : [[], []];
    set({ connections, activeConnection, projects, threads, modelsByTarget: {}, pendingRequests: {},
      selectedProjectId: projects[0]?.id ?? null, error: null });
    await get().refresh();
  },

  setSelectedProject: (selectedProjectId) => set({ selectedProjectId }),
  setThemeMode: (themeMode) => { set({ themeMode }); void saveThemeMode(themeMode); },
  loadThread: async (threadId) => loadOfficialThread(threadId, set, get),

  startTask: async (projectId, text, attachments, preferences, suppliedId) => {
    const connection = requireConnection(get());
    const project = requireProject(get(), projectId);
    const client = bindClient(connection, project.workspaceId, set, get);
    await client.connect();
    const clientMessageId = suppliedId ?? Crypto.randomUUID();
    const input = await materializeUserInput(connection, project, clientMessageId, text, attachments);
    const response = await client.startThread(project.cwd, preferences.model);
    const record: ThreadRecord = { thread: response.thread, archived: false,
      workspaceId: project.workspaceId, projectId: project.id };
    await saveAndSetThread(connection.profileId, record, set, get);
    await client.submitNewThread(response.thread, { clientMessageId, input, preferences,
      projectId: project.id });
    await saveLastTurnPreferences(connection.profileId, preferences).catch(() => undefined);
    // 首条 userMessage 物化前，官方 thread/read(includeTurns) 会拒绝；item 通知负责触发权威刷新。
    void get().refresh();
    return response.thread.id;
  },

  submitMessage: async (threadId, text, attachments, preferences, suppliedId) => {
    const connection = requireConnection(get());
    const record = requireThread(get(), threadId);
    const project = requireProject(get(), record.projectId);
    const clientMessageId = suppliedId ?? Crypto.randomUUID();
    const input = await materializeUserInput(connection, project, clientMessageId, text, attachments);
    const client = bindClient(connection, record.workspaceId, set, get);
    await client.connect();
    await client.submit({ threadId, clientMessageId, input, preferences, projectId: project.id });
    await loadOfficialThread(threadId, set, get);
  },

  executePlan: async (threadId, preferences) => {
    const record = requireThread(get(), threadId);
    const plan = latestCompletedPlan(record.thread);
    if (!plan) throw new Error("最新完成的 Turn 没有可执行计划");
    const clientId = `plan:${threadId}:${plan.itemId}`;
    const connection = requireConnection(get());
    const client = bindClient(connection, record.workspaceId, set, get);
    await client.connect();
    await client.submit({ threadId, clientMessageId: clientId,
      input: [textInput(`PLEASE IMPLEMENT THIS PLAN:\n${plan.text}`)],
      preferences: { ...preferences, collaborationMode: "default" }, projectId: record.projectId });
    await loadOfficialThread(threadId, set, get);
  },

  interruptThread: async (threadId) => {
    const connection = requireConnection(get());
    const record = requireThread(get(), threadId);
    const active = [...record.thread.turns].reverse().find((turn) => turn.status === "inProgress");
    if (!active) return;
    const client = bindClient(connection, record.workspaceId, set, get);
    await client.connect();
    await client.interrupt(threadId, active.id);
    await loadOfficialThread(threadId, set, get);
  },

  answerRequest: (threadId, requestId, result) => {
    const connection = get().activeConnection;
    const record = get().threads.find((item) => item.thread.id === threadId);
    if (!connection || !record) return false;
    const client = bindClient(connection, record.workspaceId, set, get);
    const answered = client.answerRequest(requestId, result);
    syncPendingRequests(client, threadId, set);
    return answered;
  },

  setThreadArchived: async (threadId, archived) => {
    const connection = requireConnection(get());
    const record = requireThread(get(), threadId);
    const client = bindClient(connection, record.workspaceId, set, get);
    await client.connect();
    if (archived) await client.archive(threadId); else await client.unarchive(threadId);
    set((state) => ({ threads: state.threads.map((item) => item.thread.id === threadId
      ? { ...item, archived } : item) }));
    void get().refresh();
  },

  renameThread: async (threadId, name) => {
    const connection = requireConnection(get());
    const record = requireThread(get(), threadId);
    const client = bindClient(connection, record.workspaceId, set, get);
    await client.connect();
    await client.setThreadName(threadId, name);
    await loadOfficialThread(threadId, set, get);
  },
}));

async function refreshProfile(connection: Connection, set: StoreSet, get: StoreGet): Promise<void> {
  let projectCatalog: Awaited<ReturnType<typeof projectsForConnection>>;
  try {
    projectCatalog = await projectsForConnection(connection);
    await saveProjects(connection.profileId, projectCatalog.projects, projectCatalog.bootstrap);
    if (get().activeConnection?.profileId === connection.profileId) {
      set({ projects: projectCatalog.projects,
        selectedProjectId: projectCatalog.projects.some((item) =>
          item.id === get().selectedProjectId) ? get().selectedProjectId :
          projectCatalog.projects[0]?.id ?? null });
    }
  } catch (error) {
    if (get().activeConnection?.profileId === connection.profileId) {
      set({ error: error instanceof Error ? error.message : "刷新项目失败" });
    }
    return;
  }
  try {
    const records: ThreadRecord[] = [];
    const catalogs: Record<string, Model[]> = {};
    for (const workspaceId of uniqueTargets(projectCatalog.projects)) {
      const client = bindClient(connection, workspaceId, set, get);
      await client.connect();
      const [active, archived, models] = await Promise.all([
        client.listThreads(), client.listThreads({ archived: true }), client.listModels(),
      ]);
      catalogs[targetKey(connection.profileId, workspaceId)] = models;
      for (const [thread, isArchived] of [...active.map((item) => [item, false] as const),
        ...archived.map((item) => [item, true] as const)]) {
        const project = projectForThread(projectCatalog.projects.filter((item) =>
          item.workspaceId === workspaceId), thread);
        if (project) records.push({ thread, archived: isArchived, workspaceId,
          projectId: project.id });
      }
    }
    const deduplicated = [...new Map(records.map((record) => [record.thread.id, record])).values()]
      .sort((left, right) => (right.thread.recencyAt ?? right.thread.updatedAt) -
        (left.thread.recencyAt ?? left.thread.updatedAt));
    const existing = get().activeConnection?.profileId === connection.profileId ? get().threads : [];
    const merged = preserveLoadedTurns(deduplicated, existing);
    await replaceCachedThreads(connection.profileId, merged);
    if (get().activeConnection?.profileId !== connection.profileId) return;
    set({ threads: merged, modelsByTarget: catalogs, error: null });
  } catch (error) {
    if (get().activeConnection?.profileId === connection.profileId) {
      set({ error: error instanceof Error ? error.message : "刷新失败" });
    }
  }
}

function preserveLoadedTurns(summaries: ThreadRecord[], existing: ThreadRecord[]): ThreadRecord[] {
  const byId = new Map(existing.map((record) => [record.thread.id, record]));
  return summaries.map((summary) => {
    const loaded = byId.get(summary.thread.id);
    if (!loaded || summary.thread.turns.length > 0 || loaded.thread.turns.length === 0) return summary;
    return { ...summary, thread: { ...summary.thread, turns: loaded.thread.turns } };
  });
}

async function projectsForConnection(connection: Connection): Promise<{
  projects: MobileProject[];
  bootstrap: Awaited<ReturnType<ControlApi["bootstrap"]>> | null;
}> {
  if (connection.kind === "ssh") {
    const configured = await listSSHProjects(connection.profileId);
    return { bootstrap: null, projects: configured.map((project) => ({ id: project.id,
      workspaceId: null, name: project.remotePath.split("/").filter(Boolean).at(-1) ??
        `${connection.user}@${connection.host}`, relativePath: project.remotePath,
      cwd: project.remotePath, kind: "ssh", availabilityStatus: "available",
      branch: null, dirty: false })) };
  }
  const bootstrap = await new ControlApi(connection).bootstrap();
  if (bootstrap.serverId !== connection.serverId) throw new Error("Control 与当前连接不匹配");
  return { bootstrap, projects: bootstrap.projects.map((project) => ({ ...project,
    cwd: project.absolutePath })) };
}

function uniqueTargets(projects: MobileProject[]): (string | null)[] {
  return [...new Set(projects.map((project) => project.workspaceId))];
}

function bindClient(connection: Connection, workspaceId: string | null,
  set: StoreSet, get: StoreGet) {
  const client = officialClientFor(connection, workspaceId);
  if (!subscribedClients.has(client)) {
    subscribedClients.add(client);
    client.subscribe((event) => {
      const threadId = eventThreadId(event);
      if (threadId) syncPendingRequests(client, threadId, set);
      if (get().activeConnection?.profileId !== connection.profileId) return;
      if (threadId && (event.method.startsWith("item/") || event.method.startsWith("turn/") ||
        event.method === "serverRequest/resolved")) {
        scheduleThreadRefresh(connection.profileId, threadId, get);
      } else {
        scheduleProfileRefresh(connection.profileId, get);
      }
    });
  }
  return client;
}

function eventThreadId(event: { params: unknown }): string | null {
  if (!event.params || typeof event.params !== "object") return null;
  const params = event.params as { threadId?: unknown; thread?: { id?: unknown } };
  if (typeof params.threadId === "string") return params.threadId;
  return typeof params.thread?.id === "string" ? params.thread.id : null;
}

function syncPendingRequests(client: ReturnType<typeof officialClientFor>, threadId: string,
  set: StoreSet): void {
  const pending = client.pendingRequests(threadId);
  set((state) => ({ pendingRequests: { ...state.pendingRequests, [threadId]: pending } }));
}

function scheduleThreadRefresh(profileId: string, threadId: string, get: StoreGet): void {
  const key = `${profileId}:${threadId}`;
  const current = threadRefreshTimers.get(key);
  if (current) clearTimeout(current);
  threadRefreshTimers.set(key, setTimeout(() => {
    threadRefreshTimers.delete(key);
    if (get().activeConnection?.profileId === profileId) {
      void get().loadThread(threadId).catch(() => undefined);
    }
  }, 120));
}

function scheduleProfileRefresh(profileId: string, get: StoreGet): void {
  const key = `${profileId}:list`;
  if (threadRefreshTimers.has(key)) return;
  threadRefreshTimers.set(key, setTimeout(() => {
    threadRefreshTimers.delete(key);
    if (get().activeConnection?.profileId === profileId) void get().refresh();
  }, 180));
}

async function loadOfficialThread(threadId: string, set: StoreSet, get: StoreGet): Promise<void> {
  const connection = requireConnection(get());
  const record = requireThread(get(), threadId);
  const client = bindClient(connection, record.workspaceId, set, get);
  await client.connect();
  await client.resumeThread(threadId);
  const thread = await client.readThread(threadId);
  if (get().activeConnection?.profileId !== connection.profileId) return;
  const next = { ...record, thread };
  await saveAndSetThread(connection.profileId, next, set, get);
  syncPendingRequests(client, threadId, set);
}

async function saveAndSetThread(profileId: string, record: ThreadRecord, set: StoreSet,
  get: StoreGet): Promise<void> {
  await saveThreadRecord(profileId, record);
  if (get().activeConnection?.profileId !== profileId) return;
  set((state) => ({ threads: [record, ...state.threads.filter((item) =>
    item.thread.id !== record.thread.id)].sort((left, right) =>
    (right.thread.recencyAt ?? right.thread.updatedAt) -
      (left.thread.recencyAt ?? left.thread.updatedAt)) }));
}

function requireConnection(state: AppState): Connection {
  if (!state.activeConnection) throw new Error("当前没有可用连接");
  return state.activeConnection;
}

function requireProject(state: AppState, projectId: string | null): MobileProject {
  const project = projectId ? state.projects.find((item) => item.id === projectId) : null;
  if (!project) throw new Error("项目不在当前连接中");
  return project;
}

function requireThread(state: AppState, threadId: string): ThreadRecord {
  const record = state.threads.find((item) => item.thread.id === threadId);
  if (!record) throw new Error("会话不在当前连接中");
  return record;
}
