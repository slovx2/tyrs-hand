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

  refresh: async () => {
    const connection = get().activeConnection;
    if (!connection || get().refreshing) return;
    set({ refreshing: true, error: null });
    try {
      const api = new ClientApi(connection);
      const bootstrap = await api.bootstrap();
      if (bootstrap.serverId !== connection.serverId) throw new Error("Control 身份与本地连接不一致");
      const page = await api.listSessions();
      await saveBootstrap(connection.serverId, bootstrap);
      await saveSessions(connection.serverId, page.sessions);
      set({ bootstrap, sessions: page.sessions,
        selectedProjectId: get().selectedProjectId ?? bootstrap.projects[0]?.id ?? null });
    } catch (error) {
      set({ error: error instanceof Error ? error.message : "同步失败" });
    } finally {
      set({ refreshing: false });
    }
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
