import { describe, expect, it, vi } from "vitest";

import { loadSelectedProjectId, saveSelectedProjectId } from "./settings";

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
