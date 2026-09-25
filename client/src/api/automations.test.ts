import { beforeEach, describe, expect, it, vi } from "vitest";

import { getScheduledTask, listScheduledTaskRuns, listScheduledTasks, revokeScheduledTaskMachine } from "./automations";

const getControlDeviceToken = vi.hoisted(() => vi.fn());
vi.mock("@/db/connections", () => ({ getControlDeviceToken }));

const link = {
  engine: "codex" as const,
  serverId: "server-1",
  baseUrl: "https://control.test",
  workerId: "worker-1",
  workerName: "Worker",
  deviceId: "device-1",
};

describe("定时任务只读 API", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getControlDeviceToken.mockResolvedValue("device-token");
  });

  it("Control 不可达时直接返回错误，不读取本地缓存", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("offline")));
    await expect(listScheduledTasks(link)).rejects.toThrow(
      "无法连接 Control，定时任务数据暂不可用");
  });

  it("只调用 Worker 范围内的任务与游标运行记录接口", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({ items: [] }), {
        status: 200, headers: { "Content-Type": "application/json" },
      })));
    vi.stubGlobal("fetch", fetchMock);
    await listScheduledTasks(link, "active");
    await listScheduledTaskRuns(link, "task-1", "next cursor");
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://control.test/api/v1/client/machines/worker-1/runtimes/codex/scheduled-tasks?limit=100&status=active");
    expect(fetchMock.mock.calls[1]?.[0]).toBe(
      "https://control.test/api/v1/client/machines/worker-1/runtimes/codex/scheduled-tasks/task-1/runs?limit=30&cursor=next%20cursor");
    expect((fetchMock.mock.calls[0]?.[1] as RequestInit).headers).toMatchObject({
      Authorization: "Bearer device-token",
    });
  });

  it("Claude 授权只访问 Claude 路径，并拒绝另一个引擎的任务", async () => {
    const claude = { ...link, engine: "claude-code" as const };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [{ engine: "codex" }] })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ engine: "codex" })))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(listScheduledTasks(claude)).rejects.toThrow("任务引擎与连接不一致");
    await expect(getScheduledTask(claude, "task")).rejects.toThrow("任务引擎与连接不一致");
    await revokeScheduledTaskMachine(claude);
    expect(fetchMock.mock.calls[2]?.[0]).toBe(
      "https://control.test/api/v1/client/machines/worker-1/runtimes/claude-code");
    expect(fetchMock.mock.calls[2]?.[1]).toMatchObject({ method: "DELETE" });
  });
});
