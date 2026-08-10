import type { Model } from "@codex-app-server/v2/Model";
import type { ServerRequest } from "@codex-app-server/ServerRequest";
import * as Crypto from "expo-crypto";
import { create } from "zustand";

import { ControlApi } from "@/api/control";
import { materializeUserInput, type LocalAttachment } from "@/app-server/attachments";
import { latestCompletedPlan, textInput, THREAD_PAGE_SIZE,
  type TurnPreferences } from "@/app-server/officialClient";
import { officialClientFor } from "@/app-server/registry";
import { recoverPendingProfileSubmissions } from "@/app-server/submissionRecovery";
import { projectForThread, targetKey, type MobileProject, type ThreadRecord } from "@/app-server/types";
import { loadCachedProjects, loadCachedThreads, replaceCachedThreads,
  saveProjects, saveThreadRecord } from "@/db/cache";
import { listConnections, setActiveConnection, type Connection } from "@/db/connections";
import { listSSHProjects } from "@/db/sshProjects";
import { loadThemeMode, saveLastTurnPreferences, saveThemeMode } from "@/db/settings";
import type { ThemeMode } from "@/theme/tokens";
import { CoalescingKeyedQueue } from "./coalescingQueue";
import { mergeThreadCatalog } from "./threadCatalog";
import { mergeOlderPage, mergeTailPage } from "./threadHistory";

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
  refreshThreadTail: (threadId: string) => Promise<void>;
  loadOlderThread: (threadId: string) => Promise<void>;
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
const threadLoadPromises = new Map<string, Promise<void>>();
const threadTailQueue = new CoalescingKeyedQueue();
const olderThreadPromises = new Map<string, Promise<void>>();
const hydratedThreads = new Set<string>();
const pendingCatalogThreads = new Set<string>();

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
  refreshThreadTail: async (threadId) => queueThreadTailRefresh(threadId, set, get),
  loadOlderThread: async (threadId) => loadOlderOfficialThread(threadId, set, get),

  startTask: async (projectId, text, attachments, preferences, suppliedId) => {
    const connection = requireConnection(get());
    const project = requireProject(get(), projectId);
    const client = bindClient(connection, project.workspaceId, set, get);
    await client.connect();
    const clientMessageId = suppliedId ?? Crypto.randomUUID();
    const input = await materializeUserInput(connection, project, clientMessageId, text, attachments);
    const response = await client.startThread(project.cwd, preferences.model);
    pendingCatalogThreads.add(threadKey(connection.profileId, response.thread.id));
    const record: ThreadRecord = { thread: response.thread, archived: false,
      workspaceId: project.workspaceId, projectId: project.id,
      history: { kind: "loaded", olderCursor: null, tailOlderCursor: null,
        hasLoadedOldest: true } };
    await saveAndSetThread(connection.profileId, record, set, get);
    await client.submitNewThread(response.thread, { clientMessageId, input, preferences,
      projectId: project.id });
    await saveLastTurnPreferences(connection.profileId, preferences).catch(() => undefined);
    // 首条 userMessage 物化前，分页读取可能暂不可用；item 通知负责触发权威刷新。
    void get().refresh();
    if (!response.thread.name?.trim()) {
      void generateAndSetThreadTitle({ threadId: response.thread.id, cwd: project.cwd, prompt: text,
        serviceTier: preferences.serviceTier, client, get });
    }
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
    await queueThreadTailRefresh(threadId, set, get).catch(() => undefined);
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
    await queueThreadTailRefresh(threadId, set, get).catch(() => undefined);
  },

  interruptThread: async (threadId) => {
    const connection = requireConnection(get());
    const record = requireThread(get(), threadId);
    const active = [...record.thread.turns].reverse().find((turn) => turn.status === "inProgress");
    if (!active) return;
    const client = bindClient(connection, record.workspaceId, set, get);
    await client.connect();
    await client.interrupt(threadId, active.id);
    await queueThreadTailRefresh(threadId, set, get).catch(() => undefined);
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
    await queueThreadTailRefresh(threadId, set, get).catch(() => undefined);
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
    const recovery = await recoverPendingProfileSubmissions({
      profileId: connection.profileId,
      projects: projectCatalog.projects,
      clientFor: (workspaceId) => bindClient(connection, workspaceId, set, get),
    });
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
          projectId: project.id, history: { kind: "summary" } });
      }
    }
    const deduplicated = [...new Map(records.map((record) => [record.thread.id, record])).values()]
      .sort((left, right) => (right.thread.recencyAt ?? right.thread.updatedAt) -
        (left.thread.recencyAt ?? left.thread.updatedAt));
    const existing = get().activeConnection?.profileId === connection.profileId ? get().threads : [];
    const listedIds = new Set(deduplicated.map((record) => record.thread.id));
    for (const threadId of listedIds) {
      pendingCatalogThreads.delete(threadKey(connection.profileId, threadId));
    }
    const pendingIds = new Set(existing.flatMap((record) =>
      pendingCatalogThreads.has(threadKey(connection.profileId, record.thread.id))
        ? [record.thread.id] : []));
    const merged = mergeThreadCatalog(deduplicated, existing, pendingIds);
    await replaceCachedThreads(connection.profileId, merged);
    if (get().activeConnection?.profileId !== connection.profileId) return;
    set({ threads: merged, modelsByTarget: catalogs,
      error: recovery.errors.length > 0
        ? `恢复未确认提交失败：${recovery.errors.join("；")}` : null });
  } catch (error) {
    if (get().activeConnection?.profileId === connection.profileId) {
      set({ error: error instanceof Error ? error.message : "刷新失败" });
    }
  }
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

async function generateAndSetThreadTitle(input: {
  threadId: string;
  cwd: string;
  prompt: string;
  serviceTier: string | null;
  client: ReturnType<typeof officialClientFor>;
  get: StoreGet;
}): Promise<void> {
  try {
    const generated = await input.client.generateThreadTitle({ cwd: input.cwd,
      prompt: input.prompt, serviceTier: input.serviceTier });
    if (!generated) return;
    const current = input.get().threads.find((record) => record.thread.id === input.threadId);
    // 生成期间发生人工或其他桌面端改名时，不用过期 Luna 结果覆盖。
    if (!current || current.thread.name?.trim()) return;
    await input.client.setThreadName(input.threadId, generated.title);
  } catch {
    // 标题属于非阻塞增强；主 Turn 已成功时继续保留首条消息回退标题。
  }
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
      if (event.method === "thread/name/updated") {
        const name = eventThreadName(event);
        if (threadId && name !== null) {
          void applyObservedThreadName(connection.profileId, threadId, name, set, get);
        }
      } else if (threadId && (event.method.startsWith("item/") || event.method.startsWith("turn/") ||
        event.method === "serverRequest/resolved")) {
        scheduleThreadRefresh(connection.profileId, threadId, get);
        if (event.method === "turn/completed") {
          scheduleFinalThreadRefresh(connection.profileId, threadId, get);
        }
      } else {
        scheduleProfileRefresh(connection.profileId, get);
      }
    });
  }
  return client;
}

function eventThreadName(event: { params: unknown }): string | null {
  if (!event.params || typeof event.params !== "object") return null;
  const name = (event.params as { threadName?: unknown }).threadName;
  return typeof name === "string" ? name : null;
}

async function applyObservedThreadName(profileId: string, threadId: string, name: string,
  set: StoreSet, get: StoreGet): Promise<void> {
  if (get().activeConnection?.profileId !== profileId) return;
  const current = get().threads.find((record) => record.thread.id === threadId);
  if (!current || current.thread.name === name) return;
  await setAndCacheThread(profileId,
    { ...current, thread: { ...current.thread, name } }, set, get);
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
  if (threadRefreshTimers.has(key)) return;
  threadRefreshTimers.set(key, setTimeout(() => {
    threadRefreshTimers.delete(key);
    if (get().activeConnection?.profileId === profileId) {
      void get().refreshThreadTail(threadId).catch(() => undefined);
    }
  }, 120));
}

function scheduleFinalThreadRefresh(profileId: string, threadId: string, get: StoreGet): void {
  const key = `${profileId}:${threadId}:completed`;
  if (threadRefreshTimers.has(key)) return;
  threadRefreshTimers.set(key, setTimeout(() => {
    threadRefreshTimers.delete(key);
    if (get().activeConnection?.profileId === profileId) {
      void get().refreshThreadTail(threadId).catch(() => undefined);
    }
  }, 600));
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
  const key = threadKey(connection.profileId, threadId);
  const active = threadLoadPromises.get(key);
  if (active) return active;
  const promise = (async () => {
    const record = requireThread(get(), threadId);
    const client = bindClient(connection, record.workspaceId, set, get);
    await client.connect();
    const resumed = await client.resumeThreadPage(threadId, "full", THREAD_PAGE_SIZE,
      record.thread.historyMode);
    if (get().activeConnection?.profileId !== connection.profileId) return;
    const current = requireThread(get(), threadId);
    const wasHydrated = hydratedThreads.has(key) && current.history.kind === "loaded";
    const merged = wasHydrated
      ? mergeTailPage(current.thread.turns, resumed.page.turns)
      : { turns: resumed.page.turns, overlapped: false };
    const latest = requireThread(get(), threadId);
    const preserve = wasHydrated && merged.overlapped && latest.history.kind === "loaded";
    const turns = merged.turns;
    const history = preserve && latest.history.kind === "loaded"
      ? { ...latest.history, tailOlderCursor: resumed.page.nextCursor }
      : { kind: "loaded" as const,
        olderCursor: resumed.page.nextCursor,
        tailOlderCursor: resumed.page.nextCursor,
        hasLoadedOldest: resumed.page.nextCursor === null };
    const next: ThreadRecord = { ...latest, thread: { ...resumed.thread, turns }, history };
    hydratedThreads.add(key);
    await setAndCacheThread(connection.profileId, next, set, get);
    syncPendingRequests(client, threadId, set);
  })().finally(() => {
    if (threadLoadPromises.get(key) === promise) threadLoadPromises.delete(key);
  });
  threadLoadPromises.set(key, promise);
  return promise;
}

function queueThreadTailRefresh(threadId: string, set: StoreSet, get: StoreGet): Promise<void> {
  const connection = requireConnection(get());
  const key = threadKey(connection.profileId, threadId);
  return threadTailQueue.run(key, async () => {
    const loading = threadLoadPromises.get(key);
    if (loading) await loading;
    if (get().activeConnection?.profileId !== connection.profileId) return;
    await refreshOfficialThreadTail(connection, threadId, set, get);
  });
}

async function refreshOfficialThreadTail(connection: Connection, threadId: string,
  set: StoreSet, get: StoreGet): Promise<void> {
  const record = requireThread(get(), threadId);
  const client = bindClient(connection, record.workspaceId, set, get);
  await client.connect();
  const [metadata, page] = await Promise.all([
    client.readThreadMetadata(threadId),
    client.listTurnPage(threadId, null, THREAD_PAGE_SIZE, "full", record.thread.historyMode),
  ]);
  if (get().activeConnection?.profileId !== connection.profileId) return;
  const before = requireThread(get(), threadId);
  const merged = before.history.kind === "loaded"
    ? mergeTailPage(before.thread.turns, page.turns)
    : { turns: page.turns, overlapped: false };
  const current = requireThread(get(), threadId);
  const preserve = current.history.kind === "loaded" && merged.overlapped;
  const turns = merged.turns;
  const history = preserve && current.history.kind === "loaded"
    ? { ...current.history, tailOlderCursor: page.nextCursor }
    : { kind: "loaded" as const,
      olderCursor: page.nextCursor,
      tailOlderCursor: page.nextCursor,
      hasLoadedOldest: page.nextCursor === null };
  hydratedThreads.add(threadKey(connection.profileId, threadId));
  await setAndCacheThread(connection.profileId,
    { ...current, thread: { ...metadata, turns }, history }, set, get);
  syncPendingRequests(client, threadId, set);
}

async function loadOlderOfficialThread(threadId: string, set: StoreSet,
  get: StoreGet): Promise<void> {
  const connection = requireConnection(get());
  const record = requireThread(get(), threadId);
  if (record.history.kind !== "loaded" || !record.history.olderCursor) return;
  const key = threadKey(connection.profileId, threadId);
  const active = olderThreadPromises.get(key);
  if (active) return active;
  const cursor = record.history.olderCursor;
  const promise = (async () => {
    const client = bindClient(connection, record.workspaceId, set, get);
    await client.connect();
    const page = await client.listTurnPage(threadId, cursor, THREAD_PAGE_SIZE, "full",
      record.thread.historyMode);
    if (get().activeConnection?.profileId !== connection.profileId) return;
    const current = requireThread(get(), threadId);
    if (current.history.kind !== "loaded") return;
    const merged = mergeOlderPage(current.thread.turns, current.history.olderCursor, cursor, page);
    if (!merged) return;
    const next: ThreadRecord = {
      ...current,
      thread: { ...current.thread, turns: merged.turns },
      history: { ...current.history, olderCursor: merged.nextCursor,
        hasLoadedOldest: merged.hasLoadedOldest },
    };
    setThreadRecord(connection.profileId, next, set, get);
  })().finally(() => {
    if (olderThreadPromises.get(key) === promise) olderThreadPromises.delete(key);
  });
  olderThreadPromises.set(key, promise);
  return promise;
}

async function setAndCacheThread(profileId: string, record: ThreadRecord, set: StoreSet,
  get: StoreGet): Promise<void> {
  if (!setThreadRecord(profileId, record, set, get)) return;
  await saveThreadRecord(profileId, record);
}

function setThreadRecord(profileId: string, record: ThreadRecord, set: StoreSet,
  get: StoreGet): boolean {
  if (get().activeConnection?.profileId !== profileId) return false;
  set((state) => ({ threads: [record, ...state.threads.filter((item) =>
    item.thread.id !== record.thread.id)].sort((left, right) =>
    (right.thread.recencyAt ?? right.thread.updatedAt) -
      (left.thread.recencyAt ?? left.thread.updatedAt)) }));
  return true;
}

async function saveAndSetThread(profileId: string, record: ThreadRecord, set: StoreSet,
  get: StoreGet): Promise<void> {
  await setAndCacheThread(profileId, record, set, get);
}

function threadKey(profileId: string, threadId: string): string {
  return `${profileId}:${threadId}`;
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
