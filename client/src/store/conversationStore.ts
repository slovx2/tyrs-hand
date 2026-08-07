import { create } from "zustand";

import { ClientApi } from "@/api/client";
import { loadCachedTurnsBefore, loadConversationWindow, saveConversationSnapshot,
  saveConversationTurn, saveConversationTurnPage, saveSegmentActivityPage, updateHydrationState,
  isSegmentCacheComplete,
  type CachedConversationWindow } from "@/db/conversationCache";
import type { Connection } from "@/db/connections";
import { isAppActive } from "@/sync/appLifecycle";
import type { ConversationSnapshotResponse, ConversationTurn, RunSegment, TurnRun } from "@/types/protocol";

export type ConversationView = {
  session: ConversationSnapshotResponse["session"];
  settings: ConversationSnapshotResponse["settings"];
  currentRun: ConversationSnapshotResponse["currentRun"];
  turns: ConversationTurn[];
  hasMoreBefore: boolean;
  snapshotCursor: number;
};

export type ConversationEntry = {
  view: ConversationView | null;
  status: "idle" | "loading" | "ready" | "offline" | "error";
  refreshing: boolean;
  error: string | null;
  appliedCursor: number;
};

type ConversationState = {
  entries: Record<string, ConversationEntry>;
  open: (connection: Connection, sessionId: string) => Promise<void>;
  refresh: (connection: Connection, sessionId: string) => Promise<void>;
  loadOlder: (connection: Connection, sessionId: string) => Promise<void>;
  refreshTurn: (connection: Connection, sessionId: string, turnId: string,
    replacedTurnId?: string) => Promise<void>;
  noteCursor: (connection: Connection, sessionId: string, cursor: number) => void;
  close: (connection: Connection, sessionId: string) => void;
};

const pendingRefreshes = new Map<string, Promise<void>>();
const hydrationRuns = new Map<string, Promise<void>>();
const turnRefreshGenerations = new Map<string, number>();
const turnRefreshWrites = new Map<string, Promise<void>>();
let nextTurnRefreshGeneration = 0;

function key(connection: Connection, sessionId: string): string {
  return `${connection.serverId}:${sessionId}`;
}

function turnKey(connection: Connection, sessionId: string, turnId: string): string {
  return `${key(connection, sessionId)}:${turnId}`;
}

function emptyEntry(): ConversationEntry {
  return { view: null, status: "idle", refreshing: false, error: null, appliedCursor: 0 };
}

function toView(snapshot: ConversationSnapshotResponse | CachedConversationWindow): ConversationView {
  return { session: snapshot.session, settings: snapshot.settings, currentRun: snapshot.currentRun,
    turns: snapshot.turns.items, hasMoreBefore: snapshot.turns.hasMoreBefore,
    snapshotCursor: snapshot.snapshotCursor };
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "加载会话失败";
}

export const useConversationStore = create<ConversationState>((set, get) => {
  const commitView = (entryKey: string, view: ConversationView, status: ConversationEntry["status"]) => {
    set((state) => {
      const current = state.entries[entryKey] ?? emptyEntry();
      return { entries: { ...state.entries, [entryKey]: { ...current, view,
        status, refreshing: false, error: null,
        appliedCursor: Math.max(current.appliedCursor, view.snapshotCursor) } } };
    });
  };

  const commitSnapshot = (entryKey: string, view: ConversationView,
    status: ConversationEntry["status"]) => {
    set((state) => {
      const current = state.entries[entryKey] ?? emptyEntry();
      if (view.snapshotCursor < current.appliedCursor ||
        (current.view && view.snapshotCursor < current.view.snapshotCursor)) return state;
      const sameSnapshot = current.view?.snapshotCursor === view.snapshotCursor;
      if (sameSnapshot && current.status === status && !current.refreshing && current.error === null) return state;
      return { entries: { ...state.entries, [entryKey]: { ...current,
        view: sameSnapshot ? current.view : view, status, refreshing: false, error: null,
        appliedCursor: Math.max(current.appliedCursor, view.snapshotCursor) } } };
    });
  };

  const refresh = async (connection: Connection, sessionId: string): Promise<void> => {
    const entryKey = key(connection, sessionId);
    const existing = pendingRefreshes.get(entryKey);
    if (existing) return existing;
    set((state) => ({ entries: { ...state.entries, [entryKey]: {
      ...(state.entries[entryKey] ?? emptyEntry()), refreshing: true, error: null,
    } } }));
    const pending = (async () => {
      try {
        const snapshot = await new ClientApi(connection).getSessionSnapshot(sessionId);
        const current = get().entries[entryKey] ?? emptyEntry();
        if (snapshot.snapshotCursor < current.appliedCursor) return;
        await saveConversationSnapshot(connection.serverId, snapshot);
        commitSnapshot(entryKey, toView(snapshot), "ready");
        void hydrateConversation(connection, snapshot);
      } catch (error) {
        set((state) => {
          const current = state.entries[entryKey] ?? emptyEntry();
          return { entries: { ...state.entries, [entryKey]: { ...current, refreshing: false,
            status: current.view ? "offline" : "error", error: errorText(error) } } };
        });
      }
    })().finally(() => pendingRefreshes.delete(entryKey));
    pendingRefreshes.set(entryKey, pending);
    return pending;
  };

  return {
    entries: {},
    open: async (connection, sessionId) => {
      const entryKey = key(connection, sessionId);
      set((state) => ({ entries: { ...state.entries, [entryKey]: {
        ...(state.entries[entryKey] ?? emptyEntry()), status: "loading", error: null,
      } } }));
      const network = refresh(connection, sessionId);
      const cached = await loadConversationWindow(connection.serverId, sessionId);
      if (cached) commitSnapshot(entryKey, toView(cached), "ready");
      await network;
    },
    refresh,
    loadOlder: async (connection, sessionId) => {
      const entryKey = key(connection, sessionId);
      const current = get().entries[entryKey];
      const first = current?.view?.turns[0];
      if (!current?.view || !first || !current.view.hasMoreBefore) return;
      let older = await loadCachedTurnsBefore(connection.serverId, sessionId, first.anchorSeq, 20);
      if (older.length === 0) {
        const cursor = globalThis.btoa(String(first.anchorSeq));
        const page = await new ClientApi(connection).listTurns(sessionId, { beforeCursor: cursor, limit: 20 });
        await saveConversationTurnPage(connection.serverId, sessionId, page.items,
          page.nextCursor, page.hasMoreBefore);
        older = page.items;
      }
      if (older.length === 0) {
        const view = { ...current.view, hasMoreBefore: false };
        commitView(entryKey, view, current.status);
        return;
      }
      const existing = new Map(current.view.turns.map((turn) => [`${turn.kind}:${turn.id}`, turn]));
      for (const turn of older) existing.set(`${turn.kind}:${turn.id}`, turn);
      const remaining = await loadCachedTurnsBefore(connection.serverId, sessionId,
        older[0]!.anchorSeq, 1);
      const cacheState = await loadConversationWindow(connection.serverId, sessionId, 1);
      const view = { ...current.view,
        turns: [...existing.values()].sort((left, right) => left.anchorSeq - right.anchorSeq),
        hasMoreBefore: remaining.length > 0 || (cacheState ? !cacheState.turnsComplete : older.length === 20) };
      commitView(entryKey, view, current.status);
    },
    refreshTurn: async (connection, sessionId, turnId, replacedTurnId) => {
      const entryKey = key(connection, sessionId);
      const refreshKey = turnKey(connection, sessionId, turnId);
      const generation = ++nextTurnRefreshGeneration;
      turnRefreshGenerations.set(refreshKey, generation);
      const replacedRefreshKey = replacedTurnId && replacedTurnId !== turnId ?
        turnKey(connection, sessionId, replacedTurnId) : null;
      const replacedGeneration = replacedRefreshKey ? ++nextTurnRefreshGeneration : null;
      if (replacedRefreshKey && replacedGeneration) {
        turnRefreshGenerations.set(replacedRefreshKey, replacedGeneration);
      }
      let turn: ConversationTurn;
      try {
        turn = await new ClientApi(connection).getTurn(sessionId, turnId);
      } catch (error) {
        if (turnRefreshGenerations.get(refreshKey) === generation) {
          turnRefreshGenerations.delete(refreshKey);
        }
        if (replacedRefreshKey && turnRefreshGenerations.get(replacedRefreshKey) === replacedGeneration) {
          turnRefreshGenerations.delete(replacedRefreshKey);
        }
        throw error;
      }
      if (turnRefreshGenerations.get(refreshKey) !== generation) {
        if (replacedRefreshKey && turnRefreshGenerations.get(replacedRefreshKey) === replacedGeneration) {
          turnRefreshGenerations.delete(replacedRefreshKey);
        }
        return;
      }
      const previousWrite = turnRefreshWrites.get(refreshKey) ?? Promise.resolve();
      const replacedWrite = replacedRefreshKey ? turnRefreshWrites.get(replacedRefreshKey) : undefined;
      const write = previousWrite.catch(() => undefined).then(async () => {
        if (turnRefreshGenerations.get(refreshKey) !== generation) return;
        await replacedWrite?.catch(() => undefined);
        if (turnRefreshGenerations.get(refreshKey) !== generation) return;
        await saveConversationTurn(connection.serverId, sessionId, turn, replacedTurnId);
        if (turnRefreshGenerations.get(refreshKey) !== generation) return;
        const current = get().entries[entryKey];
        if (!current?.view) return;
        const turns = [...current.view.turns.filter((item) => item.id !== turn.id &&
          item.id !== replacedTurnId), turn]
          .sort((left, right) => left.anchorSeq - right.anchorSeq);
        commitView(entryKey, { ...current.view, turns }, current.status);
      });
      turnRefreshWrites.set(refreshKey, write);
      try {
        await write;
      } finally {
        if (turnRefreshWrites.get(refreshKey) === write) turnRefreshWrites.delete(refreshKey);
        if (turnRefreshGenerations.get(refreshKey) === generation) {
          turnRefreshGenerations.delete(refreshKey);
        }
        if (replacedRefreshKey && turnRefreshGenerations.get(replacedRefreshKey) === replacedGeneration) {
          turnRefreshGenerations.delete(replacedRefreshKey);
        }
      }
    },
    noteCursor: (connection, sessionId, cursor) => {
      const entryKey = key(connection, sessionId);
      set((state) => {
        const current = state.entries[entryKey] ?? emptyEntry();
        if (cursor <= current.appliedCursor) return state;
        return { entries: { ...state.entries, [entryKey]: { ...current, appliedCursor: cursor } } };
      });
    },
    close: (connection, sessionId) => {
      const prefix = `${key(connection, sessionId)}:`;
      for (const refreshKey of turnRefreshGenerations.keys()) {
        if (refreshKey.startsWith(prefix)) turnRefreshGenerations.delete(refreshKey);
      }
    },
  };
});

type SegmentRef = { run: TurnRun; segment: RunSegment };

async function hydrateConversation(connection: Connection,
  initial: ConversationSnapshotResponse): Promise<void> {
  const entryKey = key(connection, initial.session.id);
  const existing = hydrationRuns.get(entryKey);
  if (existing) return existing;
  const promise = (async () => {
    await updateHydrationState(connection.serverId, initial.session.id, "running");
    const api = new ClientApi(connection);
    const segments = new Map<string, SegmentRef>();
    collectSegments(initial.turns.items, segments);
    let cursor = initial.turns.nextCursor;
    let hasMore = initial.turns.hasMoreBefore;
    while (hasMore) {
      if (!isAppActive()) {
        await updateHydrationState(connection.serverId, initial.session.id, "paused");
        return;
      }
      const page = await api.listTurns(initial.session.id, { beforeCursor: cursor, limit: 50 });
      await saveConversationTurnPage(connection.serverId, initial.session.id, page.items,
        page.nextCursor, page.hasMoreBefore);
      collectSegments(page.items, segments);
      cursor = page.nextCursor;
      hasMore = page.hasMoreBefore;
    }
    const queue = [...segments.values()];
    const workers = [0, 1].map(async () => {
      while (queue.length > 0) {
        if (!isAppActive()) return;
        const ref = queue.shift();
        if (ref) await hydrateSegment(api, connection, initial.session.id, ref);
      }
    });
    await Promise.all(workers);
    await updateHydrationState(connection.serverId, initial.session.id,
      isAppActive() ? "complete" : "paused");
  })().catch(async () => {
    await updateHydrationState(connection.serverId, initial.session.id, "paused").catch(() => undefined);
  }).finally(() => hydrationRuns.delete(entryKey));
  hydrationRuns.set(entryKey, promise);
  return promise;
}

function collectSegments(turns: ConversationTurn[], target: Map<string, SegmentRef>): void {
  for (const turn of turns) for (const run of turn.runs) for (const segment of run.segments) {
    target.set(segment.id, { run, segment });
  }
}

async function hydrateSegment(api: ClientApi, connection: Connection, sessionId: string,
  { run, segment }: SegmentRef): Promise<void> {
  const terminal = ["completed", "failed", "canceled"].includes(run.status);
  if (terminal && await isSegmentCacheComplete(connection.serverId, segment.id)) return;
  let page = await api.listRunActivities(run.id, segment.id, { limit: 100 });
  await saveSegmentActivityPage(connection.serverId, sessionId, run.id, segment.id, page,
    terminal && !page.hasMoreBefore);
  while (terminal && page.hasMoreBefore && page.activities[0]) {
    page = await api.listRunActivities(run.id, segment.id,
      { beforeActivitySeq: page.activities[0].firstEventSequence, limit: 100 });
    await saveSegmentActivityPage(connection.serverId, sessionId, run.id, segment.id, page,
      !page.hasMoreBefore);
  }
}
