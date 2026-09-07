import { beforeEach, describe, expect, it, vi } from "vitest";

import type { MobileProject } from "@/app-server/types";
import { loadCachedProjects } from "@/db/cache";
import { listConnections, type Connection } from "@/db/connections";
import { loadSelectedProjectId, saveSelectedProjectId } from "@/db/settings";
import { useAppStore } from "./appStore";

vi.mock("expo-crypto", () => ({ randomUUID: vi.fn() }));
vi.mock("@/app-server/registry", () => ({ officialClientFor: vi.fn() }));
vi.mock("@/app-server/attachments", () => ({ materializeUserInput: vi.fn() }));
vi.mock("@/db/database", () => ({ getDatabase: vi.fn(), runDatabaseWrite: vi.fn(),
  withDatabaseTransaction: vi.fn() }));
vi.mock("@/db/cache", () => ({ loadCachedProjects: vi.fn(async () => []),
  loadCachedThreads: vi.fn(async () => []) }));
vi.mock("@/db/connections", () => ({ listConnections: vi.fn(async () => []),
  setActiveConnection: vi.fn(async () => undefined) }));
vi.mock("@/db/settings", () => ({ loadThemeMode: vi.fn(async () => "system"),
  loadSelectedProjectId: vi.fn(async () => null),
  saveSelectedProjectId: vi.fn(async () => undefined) }));
vi.mock("@/app-server/outbox", () => ({ listOutbox: vi.fn(async () => []) }));
vi.mock("@/db/pendingMessages", () => ({ listPendingMessagePreviews: vi.fn(async () => []) }));
vi.mock("@/db/threadReads", () => ({ loadUnreadThreadIds: vi.fn(async () => []) }));

beforeEach(() => {
  vi.resetAllMocks();
  // 这里验证本地选择恢复；后台网络刷新单独覆盖。
  useAppStore.setState({ ...useAppStore.getInitialState(), refresh: vi.fn(async () => undefined) }, true);
});

describe("会话导航状态", () => {
  it("冷启动恢复当前机器上次选择的项目", async () => {
    vi.mocked(listConnections).mockResolvedValue([connection("machine-a")]);
    vi.mocked(loadCachedProjects).mockResolvedValue([project("first"), project("remembered")]);
    vi.mocked(loadSelectedProjectId).mockResolvedValue("remembered");

    await useAppStore.getState().initialize();

    expect(useAppStore.getState().selectedProjectId).toBe("remembered");
    expect(loadSelectedProjectId).toHaveBeenCalledWith("machine-a");
    expect(saveSelectedProjectId).not.toHaveBeenCalled();
  });

  it("已删除项目回退到可用项目，并保存有效选择", async () => {
    vi.mocked(listConnections).mockResolvedValue([connection("machine-a")]);
    vi.mocked(loadCachedProjects).mockResolvedValue([project("available")]);
    vi.mocked(loadSelectedProjectId).mockResolvedValue("deleted");

    await useAppStore.getState().initialize();

    expect(useAppStore.getState().selectedProjectId).toBe("available");
    expect(saveSelectedProjectId).toHaveBeenCalledWith("machine-a", "available");
  });

  it("后发的机器切换完成后，旧机器迟到的缓存不能覆盖新选择", async () => {
    const slowProjects = deferred<MobileProject[]>();
    vi.mocked(listConnections).mockResolvedValue([connection("slow"), connection("fast")]);
    vi.mocked(loadCachedProjects).mockImplementation(async (profileId) =>
      profileId === "slow" ? slowProjects.promise : [project("fast-project")]);
    vi.mocked(loadSelectedProjectId).mockImplementation(async (profileId) => `${profileId}-project`);

    const firstSwitch = useAppStore.getState().switchConnection("slow");
    await vi.waitFor(() => expect(loadCachedProjects).toHaveBeenCalledWith("slow"));
    await useAppStore.getState().switchConnection("fast");
    slowProjects.resolve([project("slow-project")]);
    await firstSwitch;

    expect(useAppStore.getState().activeConnection?.profileId).toBe("fast");
    expect(useAppStore.getState().selectedProjectId).toBe("fast-project");
    expect(useAppStore.getState().projects.map((item) => item.id)).toEqual(["fast-project"]);
    expect(useAppStore.getState().refresh).toHaveBeenCalledTimes(1);
  });

  it("手动选择只写入当前机器的项目偏好", () => {
    useAppStore.setState({ activeConnection: connection("machine-b") });
    useAppStore.getState().setSelectedProject("project-b");
    expect(useAppStore.getState().selectedProjectId).toBe("project-b");
    expect(saveSelectedProjectId).toHaveBeenCalledWith("machine-b", "project-b");
  });
});

function connection(profileId: string): Connection {
  return { kind: "ssh", profileId, name: profileId, active: true,
    machineFingerprint: `test:${profileId}`, controls: [], host: "localhost", port: 22,
    user: "tester", keyRef: "test-key", hostFingerprint: null };
}

function project(id: string): MobileProject {
  return { id, name: id, workspaceId: null, relativePath: `/${id}`, cwd: `/${id}`,
    kind: "ssh", availabilityStatus: "available", branch: null, dirty: false };
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void;
  return { promise: new Promise<T>((done) => { resolve = done; }), resolve };
}
