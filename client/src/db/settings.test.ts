import { describe, expect, it, vi } from "vitest";

import { loadLiveConnectionSoundsEnabled, loadLiveConversationId, loadSelectedProjectId,
  saveLiveConnectionSoundsEnabled, saveLiveConversationId, saveSelectedProjectId } from "./settings";

const database = vi.hoisted(() => ({
  getDatabase: vi.fn(),
  runDatabaseWrite: vi.fn(),
}));

vi.mock("./database", () => database);

describe("会话项目选择持久化", () => {
  it("读取已保存项目，空值回退为 null", async () => {
    database.getDatabase.mockResolvedValue({
      getFirstAsync: vi.fn().mockResolvedValueOnce({ value: " project-2 " }),
    });
    await expect(loadSelectedProjectId("machine-1")).resolves.toBe("project-2");

    database.getDatabase.mockResolvedValue({
      getFirstAsync: vi.fn().mockResolvedValueOnce({ value: "" }),
    });
    await expect(loadSelectedProjectId("machine-1")).resolves.toBeNull();
  });

  it("保存项目或清空当前机器的选择", async () => {
    const runAsync = vi.fn();
    database.runDatabaseWrite.mockImplementation((operation: (db: unknown) => unknown) =>
      operation({ runAsync }));
    await saveSelectedProjectId("machine-1", "project-2");
    await saveSelectedProjectId("machine-1", null);
    expect(runAsync).toHaveBeenNthCalledWith(1, expect.stringContaining("ON CONFLICT"),
      "selectedProject:machine-1", "project-2");
    expect(runAsync).toHaveBeenNthCalledWith(2, expect.stringContaining("ON CONFLICT"),
      "selectedProject:machine-1", "");
  });
});

describe("Live conversation 持久化", () => {
  it("读取已保存 conversation，空值回退为 null", async () => {
    database.getDatabase.mockResolvedValue({
      getFirstAsync: vi.fn().mockResolvedValueOnce({ value: " conv-1 " }),
    });
    await expect(loadLiveConversationId("machine-1")).resolves.toBe("conv-1");
    database.getDatabase.mockResolvedValue({
      getFirstAsync: vi.fn().mockResolvedValueOnce({ value: "" }),
    });
    await expect(loadLiveConversationId("machine-1")).resolves.toBeNull();
  });

  it("保存或清空当前机器的 Live conversation", async () => {
    const runAsync = vi.fn();
    database.runDatabaseWrite.mockImplementation((operation: (db: unknown) => unknown) =>
      operation({ runAsync }));
    await saveLiveConversationId("machine-1", "conv-2");
    await saveLiveConversationId("machine-1", null);
    expect(runAsync).toHaveBeenNthCalledWith(1, expect.stringContaining("ON CONFLICT"),
      "liveConversation:machine-1", "conv-2");
    expect(runAsync).toHaveBeenNthCalledWith(2, expect.stringContaining("ON CONFLICT"),
      "liveConversation:machine-1", "");
  });
});

describe("Live 连接提示音设置", () => {
  it("没有保存值时默认开启，保存关闭后读取为关闭", async () => {
    database.getDatabase.mockResolvedValue({
      getFirstAsync: vi.fn().mockResolvedValueOnce(undefined),
    });
    await expect(loadLiveConnectionSoundsEnabled()).resolves.toBe(true);

    database.getDatabase.mockResolvedValue({
      getFirstAsync: vi.fn().mockResolvedValueOnce({ value: "0" }),
    });
    await expect(loadLiveConnectionSoundsEnabled()).resolves.toBe(false);
  });

  it("持久化开启和关闭状态", async () => {
    const runAsync = vi.fn();
    database.runDatabaseWrite.mockImplementation((operation: (db: unknown) => unknown) =>
      operation({ runAsync }));
    await saveLiveConnectionSoundsEnabled(false);
    await saveLiveConnectionSoundsEnabled(true);
    expect(runAsync).toHaveBeenNthCalledWith(1, expect.stringContaining("ON CONFLICT"),
      "liveConnectionSounds", "0");
    expect(runAsync).toHaveBeenNthCalledWith(2, expect.stringContaining("ON CONFLICT"),
      "liveConnectionSounds", "1");
  });
});
