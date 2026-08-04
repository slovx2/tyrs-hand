import { create } from "zustand";

import { ClientApi } from "@/api/client";
import { loadCachedBootstrap, loadCachedSessions, saveBootstrap, saveSessions } from "@/db/cache";
import { listConnections, setActiveConnection, type Connection } from "@/db/connections";
import { loadThemeMode, saveThemeMode } from "@/db/settings";
import type { Bootstrap, Session } from "@/types/protocol";
import type { ThemeMode } from "@/theme/tokens";

type AppState = {
  ready: boolean;
  refreshing: boolean;
  error: string | null;
  themeMode: ThemeMode;
  connections: Connection[];
  activeConnection: Connection | null;
  bootstrap: Bootstrap | null;
  sessions: Session[];
  selectedProjectId: string | null;
  initialize: () => Promise<void>;
  refresh: () => Promise<void>;
  reloadConnections: () => Promise<void>;
  switchConnection: (serverId: string) => Promise<void>;
  setSelectedProject: (projectId: string | null) => void;
  setThemeMode: (mode: ThemeMode) => void;
  upsertSession: (session: Session) => void;
};

let refreshPromise: Promise<void> | null = null;
let refreshQueued = false;

export const useAppStore = create<AppState>((set, get) => ({
  ready: false,
  refreshing: false,
  error: null,
  themeMode: "system",
  connections: [],
  activeConnection: null,
  bootstrap: null,
  sessions: [],
  selectedProjectId: null,

  initialize: async () => {
    const [connections, themeMode] = await Promise.all([listConnections(), loadThemeMode()]);
    const activeConnection = connections.find((item) => item.active) ?? connections[0] ?? null;
    const bootstrap = activeConnection ? await loadCachedBootstrap(activeConnection.serverId) : null;
    const sessions = activeConnection ? await loadCachedSessions(activeConnection.serverId) : [];
    set({ ready: true, connections, activeConnection, bootstrap, sessions, themeMode,
      selectedProjectId: bootstrap?.projects[0]?.id ?? null });
    if (activeConnection) void get().refresh();
  },

  refresh: () => {
    refreshQueued = true;
    if (refreshPromise) return refreshPromise;
    set({ refreshing: true, error: null });
    refreshPromise = (async () => {
      while (refreshQueued) {
        refreshQueued = false;
        const connection = get().activeConnection;
        if (!connection) continue;
        try {
          const api = new ClientApi(connection);
          const bootstrap = await api.bootstrap();
          if (bootstrap.serverId !== connection.serverId) throw new Error("服务器与当前连接不匹配");
          const page = await api.listSessions();
          await saveBootstrap(connection.serverId, bootstrap);
          await saveSessions(connection.serverId, page.sessions);
          if (get().activeConnection?.serverId !== connection.serverId) continue;
          set({ bootstrap, sessions: page.sessions, error: null,
            selectedProjectId: get().selectedProjectId ?? bootstrap.projects[0]?.id ?? null });
        } catch (error) {
          if (get().activeConnection?.serverId === connection.serverId) {
            set({ error: error instanceof Error ? error.message : "刷新失败" });
          }
        }
      }
    })().finally(() => {
      refreshPromise = null;
      set({ refreshing: false });
    });
    return refreshPromise;
  },

  reloadConnections: async () => {
    const connections = await listConnections();
    const current = get().activeConnection;
    if (current && !connections.some((item) => item.serverId === current.serverId)) {
      const activeConnection = connections.find((item) => item.active) ?? connections[0] ?? null;
      set({ connections, activeConnection, bootstrap: null, sessions: [], selectedProjectId: null });
      return;
    }
    set({ connections });
  },

  switchConnection: async (serverId) => {
    await setActiveConnection(serverId);
    const connections = await listConnections();
    const activeConnection = connections.find((item) => item.serverId === serverId) ?? null;
    const bootstrap = activeConnection ? await loadCachedBootstrap(serverId) : null;
    const sessions = activeConnection ? await loadCachedSessions(serverId) : [];
    set({ connections, activeConnection, bootstrap, sessions, selectedProjectId: null });
    await get().refresh();
  },

  setSelectedProject: (selectedProjectId) => set({ selectedProjectId }),
  setThemeMode: (themeMode) => { set({ themeMode }); void saveThemeMode(themeMode); },
  upsertSession: (session) => set((state) => ({
    sessions: [session, ...state.sessions.filter((item) => item.id !== session.id)],
  })),
}));
