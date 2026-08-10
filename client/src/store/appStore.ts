import type { Model } from "@codex-app-server/v2/Model";
import type { ServerNotification } from "@codex-app-server/ServerNotification";
import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";
import type { Turn } from "@codex-app-server/v2/Turn";
import * as Crypto from "expo-crypto";
import { create } from "zustand";

import { materializeUserInput, type LocalAttachment } from "@/app-server/attachments";
import { latestCompletedPlan, textInput, THREAD_PAGE_SIZE,
  type TurnPreferences } from "@/app-server/officialClient";
import { officialClientFor } from "@/app-server/registry";
import { completeOutbox, discardOutboxItem, enqueueOutbox, failOutbox, listOutbox, markOutboxProcessing,
  retryOutboxItem, setOutboxThread, type NativeOutboxItem } from "@/app-server/outbox";
import { recoverPendingProfileSubmissions } from "@/app-server/submissionRecovery";
import { projectForThread, targetKey, type MobileProject, type ThreadRecord } from "@/app-server/types";
import { loadCachedProjects, loadCachedThreads, replaceCachedThreads,
  saveProjects, saveThreadRecord } from "@/db/cache";
import { listConnections, setActiveConnection, type Connection } from "@/db/connections";
import { listSSHProjects } from "@/db/sshProjects";
import { loadThemeMode, saveLastTurnPreferences, saveThemeMode } from "@/db/settings";
import { loadUnreadThreadIds, markThreadRead, markThreadUnread, reconcileThreadReads,
  removeThreadRead } from "@/db/threadReads";
import type { ThemeMode } from "@/theme/tokens";
import { CoalescingKeyedQueue } from "./coalescingQueue";
import { mergeThreadCatalog } from "./threadCatalog";
import { mergeOlderPage, mergeTailPage } from "./threadHistory";
import { isDirectThreadNotification, reduceThreadNotification } from "./threadNotificationReducer";
import { StreamingTextQueue, type StreamingDelta } from "./streamingTextQueue";

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
  outbox: NativeOutboxItem[];
  unreadThreadIds: Record<string, true>;
  selectedProjectId: string | null;
  initialize: () => Promise<void>;
  refresh: () => Promise<void>;
  reloadConnections: () => Promise<void>;
  switchConnection: (profileId: string) => Promise<void>;
  setSelectedProject: (projectId: string | null) => void;
  setThreadVisible: (threadId: string, visible: boolean) => void;
  setThemeMode: (mode: ThemeMode) => void;
  loadThread: (threadId: string) => Promise<void>;
  refreshThreadTail: (threadId: string) => Promise<void>;
  loadOlderThread: (threadId: string) => Promise<void>;
  startTask: (projectId: string, text: string, attachments: LocalAttachment[],
    preferences: TurnPreferences, clientMessageId?: string) => Promise<string | null>;
  submitMessage: (threadId: string, text: string, attachments: LocalAttachment[],
    preferences: TurnPreferences, clientMessageId?: string) => Promise<boolean>;
  retryOutbox: (clientMessageId?: string) => Promise<void>;
  discardOutbox: (clientMessageId: string) => Promise<void>;
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
const threadCacheTimers = new Map<string, ReturnType<typeof setTimeout>>();
const notificationChains = new Map<string, Promise<void>>();
const visibleThreads = new Set<string>();
const threadLoadPromises = new Map<string, Promise<void>>();
const threadTailQueue = new CoalescingKeyedQueue();
const olderThreadPromises = new Map<string, Promise<void>>();
const hydratedThreads = new Set<string>();
const pendingCatalogThreads = new Set<string>();
const outboxDrains = new Map<string, Promise<OutboxDrainResult>>();

export const useAppStore = create<AppState>((set, get) => ({
  ready: false, refreshing: false, error: null, themeMode: "system", connections: [],
  activeConnection: null, projects: [], threads: [], modelsByTarget: {}, pendingRequests: {},
  outbox: [], unreadThreadIds: {}, selectedProjectId: null,

  initialize: async () => {
    const [connections, themeMode] = await Promise.all([listConnections(), loadThemeMode()]);
    const activeConnection = connections.find((item) => item.active) ?? connections[0] ?? null;
    const [projects, threads, outbox, unreadThreadIds] = activeConnection ? await Promise.all([
      loadCachedProjects(activeConnection.profileId), loadCachedThreads(activeConnection.profileId),
      listOutbox(activeConnection.profileId), loadUnreadThreadIds(activeConnection.profileId),
    ]) : [[], [], [], []];
    set({ ready: true, connections, activeConnection, projects, threads, themeMode,
      outbox, unreadThreadIds: unreadRecord(unreadThreadIds),
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
      const [projects, threads, outbox, unreadThreadIds] = activeConnection ? await Promise.all([
        loadCachedProjects(activeConnection.profileId), loadCachedThreads(activeConnection.profileId),
        listOutbox(activeConnection.profileId), loadUnreadThreadIds(activeConnection.profileId),
      ]) : [[], [], [], []];
      set({ connections, activeConnection, projects, threads, modelsByTarget: {},
        pendingRequests: {}, outbox, unreadThreadIds: unreadRecord(unreadThreadIds),
        selectedProjectId: projects[0]?.id ?? null });
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
    visibleThreads.clear();
    const [projects, threads, outbox, unreadThreadIds] = activeConnection ? await Promise.all([
      loadCachedProjects(profileId), loadCachedThreads(profileId),
      listOutbox(profileId), loadUnreadThreadIds(profileId),
    ]) : [[], [], [], []];
    set({ connections, activeConnection, projects, threads, modelsByTarget: {}, pendingRequests: {},
      outbox, unreadThreadIds: unreadRecord(unreadThreadIds),
      selectedProjectId: projects[0]?.id ?? null, error: null });
    await get().refresh();
  },

  setSelectedProject: (selectedProjectId) => set({ selectedProjectId }),
  setThreadVisible: (threadId, visible) => {
    const connection = get().activeConnection;
    if (!connection) return;
    const key = threadKey(connection.profileId, threadId);
    if (visible) {
      visibleThreads.add(key);
      set((state) => ({ unreadThreadIds: withoutUnread(state.unreadThreadIds, threadId) }));
      void markThreadRead(connection.profileId, threadId).catch(() => undefined);
    } else {
      visibleThreads.delete(key);
    }
  },
  setThemeMode: (themeMode) => { set({ themeMode }); void saveThemeMode(themeMode); },
  loadThread: async (threadId) => loadOfficialThread(threadId, set, get),
  refreshThreadTail: async (threadId) => queueThreadTailRefresh(threadId, set, get),
  loadOlderThread: async (threadId) => loadOlderOfficialThread(threadId, set, get),

  startTask: async (projectId, text, attachments, preferences, suppliedId) => {
    const connection = requireConnection(get());
    requireProject(get(), projectId);
    const clientMessageId = suppliedId ?? Crypto.randomUUID();
    await enqueueOutbox({ profileId: connection.profileId, clientMessageId,
      kind: "create_task", projectId, threadId: null,
      payload: { text, attachments, preferences } });
    await syncOutboxState(connection.profileId, set, get);
    const result = await drainProfileOutbox(connection, get().projects, set, get);
    return result.completed.get(clientMessageId) ?? null;
  },

  submitMessage: async (threadId, text, attachments, preferences, suppliedId) => {
    const connection = requireConnection(get());
    const record = requireThread(get(), threadId);
    const project = requireProject(get(), record.projectId);
    const clientMessageId = suppliedId ?? Crypto.randomUUID();
    insertOptimisticTurn(connection.profileId, record, clientMessageId, text, attachments, set, get);
    try {
      await enqueueOutbox({ profileId: connection.profileId, clientMessageId,
        kind: "submit_message", projectId: project.id, threadId,
        payload: { text, attachments, preferences } });
      await syncOutboxState(connection.profileId, set, get);
      const result = await drainProfileOutbox(connection, get().projects, set, get);
      const sent = result.completed.has(clientMessageId);
      if (!sent) removeOptimisticTurn(connection.profileId, threadId, clientMessageId, set, get);
      return sent;
    } catch (error) {
      removeOptimisticTurn(connection.profileId, threadId, clientMessageId, set, get);
      throw error;
    }
  },

  retryOutbox: async (clientMessageId) => {
    const connection = requireConnection(get());
    await retryOutboxItem(connection.profileId, clientMessageId);
    await syncOutboxState(connection.profileId, set, get);
    await drainProfileOutbox(connection, get().projects, set, get);
  },

  discardOutbox: async (clientMessageId) => {
    const connection = requireConnection(get());
    await discardOutboxItem(connection.profileId, clientMessageId);
    await syncOutboxState(connection.profileId, set, get);
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
      ? { ...item, archived } : item),
    unreadThreadIds: archived ? withoutUnread(state.unreadThreadIds, threadId)
      : state.unreadThreadIds }));
    if (archived) void removeThreadRead(connection.profileId, threadId).catch(() => undefined);
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

type OutboxDrainResult = {
  completed: Map<string, string>;
  errors: string[];
};

function drainProfileOutbox(connection: Connection, projects: MobileProject[],
  set: StoreSet, get: StoreGet): Promise<OutboxDrainResult> {
  const active = outboxDrains.get(connection.profileId);
  if (active) {
    return active.then(async (first) => {
      const pending = (await listOutbox(connection.profileId)).some((item) => item.state === "pending");
      if (!pending) return first;
      const next = await drainProfileOutbox(connection, projects, set, get);
      return { completed: new Map([...first.completed, ...next.completed]),
        errors: [...first.errors, ...next.errors] };
    });
  }
  const promise = drainOutboxItems(connection, projects, set, get).finally(() => {
    if (outboxDrains.get(connection.profileId) === promise) {
      outboxDrains.delete(connection.profileId);
    }
  });
  outboxDrains.set(connection.profileId, promise);
  return promise;
}

async function drainOutboxItems(connection: Connection, projects: MobileProject[],
  set: StoreSet, get: StoreGet): Promise<OutboxDrainResult> {
  const result: OutboxDrainResult = { completed: new Map(), errors: [] };
  for (;;) {
    const item = (await listOutbox(connection.profileId))[0];
    if (!item || item.state !== "pending") break;
    await markOutboxProcessing(item.profileId, item.clientMessageId);
    await syncOutboxState(item.profileId, set, get);
    try {
      const project = projects.find((entry) => entry.id === item.projectId);
      if (!project) throw new Error("发送队列对应的项目已不存在");
      const client = bindClient(connection, project.workspaceId, set, get);
      await client.connect();
      const input = await materializeUserInput(connection, project, item.clientMessageId,
        item.payload.text, item.payload.attachments);
      let threadId = item.threadId;
      let submissionThread: ThreadRecord["thread"] | null = null;
      if (item.kind === "create_task") {
        const source = mobileOutboxThreadSource(item);
        let thread = threadId ? await client.resumeThreadForSubmissionIfExists(threadId) : null;
        if (!thread) {
          const discovered = await client.findThreadBySource(source);
          // thread/list 可能先暴露尚无 rollout 的 Thread；它仍不能 resume。
          thread = discovered && discovered.id !== threadId
            ? await client.resumeThreadForSubmissionIfExists(discovered.id) : null;
        }
        if (!thread) {
          thread = (await client.startThread(project.cwd, item.payload.preferences.model,
            source)).thread;
        }
        submissionThread = thread;
        threadId = thread.id;
        await setOutboxThread(item.profileId, item.clientMessageId, threadId);
        pendingCatalogThreads.add(threadKey(item.profileId, threadId));
        await saveOutboxThread(item.profileId, project, thread, set, get);
      }
      if (!threadId) throw new Error("发送队列缺少官方 Thread ID");
      const submission = { clientMessageId: item.clientMessageId, input,
        preferences: item.payload.preferences, projectId: project.id };
      if (submissionThread) await client.submitNewThread(submissionThread, submission);
      else await client.submit({ ...submission, threadId });
      await completeOutbox(item.profileId, item.clientMessageId);
      result.completed.set(item.clientMessageId, threadId);
      await saveLastTurnPreferences(item.profileId, item.payload.preferences).catch(() => undefined);
      await queueThreadTailRefresh(threadId, set, get).catch(() => undefined);
      if (item.kind === "create_task" && item.payload.text.trim()) {
        const current = get().threads.find((record) => record.thread.id === threadId);
        if (current && !current.thread.name?.trim()) {
          void generateAndSetThreadTitle({ threadId, cwd: project.cwd, prompt: item.payload.text,
            serviceTier: item.payload.preferences.serviceTier, client, get });
        }
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "发送失败";
      await failOutbox(item.profileId, item.clientMessageId, message);
      result.errors.push(`${item.clientMessageId}: ${message}`);
      break;
    } finally {
      await syncOutboxState(item.profileId, set, get);
    }
  }
  return result;
}

function mobileOutboxThreadSource(item: NativeOutboxItem): string {
  return `tyrs-hand-mobile:${item.profileId}:${item.clientMessageId}`;
}

async function saveOutboxThread(profileId: string, project: MobileProject,
  thread: ThreadRecord["thread"], set: StoreSet, get: StoreGet): Promise<void> {
  const current = get().threads.find((record) => record.thread.id === thread.id);
  const record: ThreadRecord = current
    ? { ...current, thread: { ...thread,
      turns: thread.turns.length > 0 ? thread.turns : current.thread.turns } }
    : { thread, archived: false, workspaceId: project.workspaceId, projectId: project.id,
      history: { kind: "loaded", olderCursor: null, tailOlderCursor: null,
        hasLoadedOldest: true } };
  await saveAndSetThread(profileId, record, set, get);
}

async function syncOutboxState(profileId: string, set: StoreSet, get: StoreGet): Promise<void> {
  const outbox = await listOutbox(profileId);
  if (get().activeConnection?.profileId === profileId) set({ outbox });
}

async function refreshProfile(connection: Connection, set: StoreSet, get: StoreGet): Promise<void> {
  let projectCatalog: Awaited<ReturnType<typeof projectsForConnection>>;
  try {
    projectCatalog = await projectsForConnection(connection);
    await saveProjects(connection.profileId, projectCatalog.projects);
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
    await retryOutboxItem(connection.profileId);
    const outbox = await drainProfileOutbox(connection, projectCatalog.projects, set, get);
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
    const unreadThreadIds = await reconcileThreadReads(connection.profileId,
      merged.filter((record) => !record.archived).map((record) => record.thread.id));
    await replaceCachedThreads(connection.profileId, merged);
    if (get().activeConnection?.profileId !== connection.profileId) return;
    set({ threads: merged, modelsByTarget: catalogs,
      unreadThreadIds: unreadRecord(unreadThreadIds.filter((threadId) =>
        !visibleThreads.has(threadKey(connection.profileId, threadId)))),
      error: [...outbox.errors, ...recovery.errors].length > 0
        ? `恢复发送失败：${[...outbox.errors, ...recovery.errors].join("；")}` : null });
  } catch (error) {
    if (get().activeConnection?.profileId === connection.profileId) {
      set({ error: error instanceof Error ? error.message : "刷新失败" });
    }
  }
}

async function projectsForConnection(connection: Connection): Promise<{
  projects: MobileProject[];
}> {
  const configured = await listSSHProjects(connection.profileId);
  return { projects: configured.map((project) => ({ id: project.id,
    workspaceId: null, name: project.remotePath.split("/").filter(Boolean).at(-1) ??
      `${connection.user}@${connection.host}`, relativePath: project.remotePath,
    cwd: project.remotePath, kind: "ssh", availabilityStatus: "available",
    branch: null, dirty: false })) };
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
    const streaming = new StreamingTextQueue((delta) =>
      applyStreamingDelta(connection.profileId, delta, set, get));
    client.subscribe((event) => {
      const threadId = eventThreadId(event);
      if (threadId) syncPendingRequests(client, threadId, set);
      if (get().activeConnection?.profileId !== connection.profileId) return;
      if (event.method === "thread/name/updated") {
        const name = eventThreadName(event);
        if (threadId && name !== null) {
          void applyObservedThreadName(connection.profileId, threadId, name, set, get);
        }
      } else if (threadId && isDirectThreadNotification(event)) {
        const delta = notificationStreamingDelta(event);
        if (delta) streaming.enqueue(delta);
        else enqueueThreadNotification(connection.profileId, threadId, event, streaming, set, get);
        if (event.method === "turn/started" || event.method === "turn/completed") {
          observeUnread(connection.profileId, threadId, set, get);
        }
      } else if (threadId && event.method !== "serverRequest/resolved" &&
        isServerRequestMethod(event.method)) {
        observeUnread(connection.profileId, threadId, set, get);
      } else if (threadId && (event.method.startsWith("item/") || event.method.startsWith("turn/") ||
        event.method === "serverRequest/resolved")) {
        scheduleThreadRefresh(connection.profileId, threadId, get);
      } else {
        scheduleProfileRefresh(connection.profileId, get);
      }
    });
  }
  return client;
}

function enqueueThreadNotification(profileId: string, threadId: string,
  event: ServerNotification, streaming: StreamingTextQueue, set: StoreSet, get: StoreGet): void {
  const key = threadKey(profileId, threadId);
  const previous = notificationChains.get(key) ?? Promise.resolve();
  const next = previous.catch(() => undefined).then(async () => {
    if (event.method === "item/completed") {
      await streaming.flushItem(threadId, event.params.turnId, event.params.item.id);
      streaming.discardItem(threadId, event.params.turnId, event.params.item.id);
    } else if (event.method === "turn/completed") {
      await streaming.flushTurn(threadId, event.params.turn.id);
      streaming.discardTurn(threadId, event.params.turn.id);
    }
    applyThreadNotification(profileId, threadId, event, set, get);
    if (event.method === "turn/completed") scheduleFinalThreadRefresh(profileId, threadId, get);
  }).finally(() => {
    if (notificationChains.get(key) === next) notificationChains.delete(key);
  });
  notificationChains.set(key, next);
}

function applyThreadNotification(profileId: string, threadId: string,
  event: ServerNotification, set: StoreSet, get: StoreGet): void {
  if (get().activeConnection?.profileId !== profileId) return;
  const current = get().threads.find((record) => record.thread.id === threadId);
  if (!current) {
    scheduleProfileRefresh(profileId, get);
    return;
  }
  const reduced = reduceThreadNotification(current, event);
  if (reduced.changed) {
    setThreadRecord(profileId, reduced.record, set, get);
    if (reduced.terminal) flushThreadCache(profileId, threadId, get);
    else scheduleThreadCache(profileId, threadId, get);
  }
  if (reduced.needsRefresh) scheduleThreadRefresh(profileId, threadId, get);
}

function applyStreamingDelta(profileId: string, delta: StreamingDelta,
  set: StoreSet, get: StoreGet): void {
  const event = streamingDeltaNotification(delta);
  applyThreadNotification(profileId, delta.threadId, event, set, get);
}

function notificationStreamingDelta(event: ServerNotification): StreamingDelta | null {
  switch (event.method) {
  case "item/agentMessage/delta": return { threadId: event.params.threadId,
    turnId: event.params.turnId, itemId: event.params.itemId, target: "agent", index: 0,
    delta: event.params.delta };
  case "item/plan/delta": return { threadId: event.params.threadId,
    turnId: event.params.turnId, itemId: event.params.itemId, target: "plan", index: 0,
    delta: event.params.delta };
  case "item/reasoning/summaryTextDelta": return { threadId: event.params.threadId,
    turnId: event.params.turnId, itemId: event.params.itemId, target: "reasoningSummary",
    index: event.params.summaryIndex, delta: event.params.delta };
  case "item/reasoning/textDelta": return { threadId: event.params.threadId,
    turnId: event.params.turnId, itemId: event.params.itemId, target: "reasoningContent",
    index: event.params.contentIndex, delta: event.params.delta };
  default: return null;
  }
}

function streamingDeltaNotification(delta: StreamingDelta): ServerNotification {
  const base = { threadId: delta.threadId, turnId: delta.turnId, itemId: delta.itemId,
    delta: delta.delta };
  switch (delta.target) {
  case "agent": return { method: "item/agentMessage/delta", params: base };
  case "plan": return { method: "item/plan/delta", params: base };
  case "reasoningSummary": return { method: "item/reasoning/summaryTextDelta",
    params: { ...base, summaryIndex: delta.index } };
  case "reasoningContent": return { method: "item/reasoning/textDelta",
    params: { ...base, contentIndex: delta.index } };
  }
}

function observeUnread(profileId: string, threadId: string, set: StoreSet, get: StoreGet): void {
  if (visibleThreads.has(threadKey(profileId, threadId))) {
    if (get().unreadThreadIds[threadId]) {
      set((state) => ({ unreadThreadIds: withoutUnread(state.unreadThreadIds, threadId) }));
    }
    void markThreadRead(profileId, threadId).catch(() => undefined);
    return;
  }
  set((state) => state.unreadThreadIds[threadId] ? {}
    : { unreadThreadIds: { ...state.unreadThreadIds, [threadId]: true } });
  void markThreadUnread(profileId, threadId).catch(() => undefined);
}

function isServerRequestMethod(method: string): boolean {
  return method.endsWith("/requestApproval") || method === "item/tool/requestUserInput" ||
    method === "mcpServer/elicitation/request";
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

function scheduleThreadCache(profileId: string, threadId: string, get: StoreGet): void {
  const key = `${profileId}:${threadId}:cache`;
  if (threadCacheTimers.has(key)) return;
  threadCacheTimers.set(key, setTimeout(() => {
    threadCacheTimers.delete(key);
    if (get().activeConnection?.profileId !== profileId) return;
    const record = get().threads.find((item) => item.thread.id === threadId);
    if (record) void saveThreadRecord(profileId, record).catch(() => undefined);
  }, 250));
}

function flushThreadCache(profileId: string, threadId: string, get: StoreGet): void {
  const key = `${profileId}:${threadId}:cache`;
  const timer = threadCacheTimers.get(key);
  if (timer) clearTimeout(timer);
  threadCacheTimers.delete(key);
  if (get().activeConnection?.profileId !== profileId) return;
  const record = get().threads.find((item) => item.thread.id === threadId);
  if (record) void saveThreadRecord(profileId, record).catch(() => undefined);
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

function insertOptimisticTurn(profileId: string, record: ThreadRecord, clientMessageId: string,
  text: string, attachments: LocalAttachment[], set: StoreSet, get: StoreGet): void {
  const content: Extract<ThreadItem, { type: "userMessage" }>["content"] = [];
  if (text.trim()) content.push(textInput(text.trim()));
  content.push(...attachments.map((attachment) => ({ type: "mention" as const,
    name: attachment.name, path: attachment.uri })));
  const nowSeconds = Date.now() / 1000;
  const turn: Turn = {
    id: `provisional:${clientMessageId}`,
    items: [{ type: "userMessage", id: `provisional-user:${clientMessageId}`,
      clientId: clientMessageId, content }],
    itemsView: "full",
    status: "inProgress",
    error: null,
    startedAt: nowSeconds,
    completedAt: null,
    durationMs: null,
  };
  const next: ThreadRecord = { ...record, thread: { ...record.thread,
    status: { type: "active", activeFlags: [] },
    recencyAt: nowSeconds, updatedAt: nowSeconds,
    turns: [...record.thread.turns, turn] } };
  setThreadRecord(profileId, next, set, get);
}

function removeOptimisticTurn(profileId: string, threadId: string, clientMessageId: string,
  set: StoreSet, get: StoreGet): void {
  if (get().activeConnection?.profileId !== profileId) return;
  const current = get().threads.find((record) => record.thread.id === threadId);
  if (!current) return;
  const provisionalId = `provisional:${clientMessageId}`;
  const turns = current.thread.turns.filter((turn) => turn.id !== provisionalId);
  if (turns.length === current.thread.turns.length) return;
  const active = turns.some((turn) => turn.status === "inProgress");
  setThreadRecord(profileId, { ...current, thread: { ...current.thread, turns,
    status: active ? current.thread.status : { type: "idle" } } }, set, get);
}

function unreadRecord(threadIds: readonly string[]): Record<string, true> {
  return Object.fromEntries(threadIds.map((threadId) => [threadId, true])) as Record<string, true>;
}

function withoutUnread(current: Record<string, true>, threadId: string): Record<string, true> {
  if (!current[threadId]) return current;
  const next = { ...current };
  delete next[threadId];
  return next;
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
