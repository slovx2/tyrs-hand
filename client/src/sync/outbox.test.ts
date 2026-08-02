import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Connection } from "@/db/connections";
import { processOutbox, recoverFailedOutbox } from "./outbox";

type TestRow = Record<string, string | null> & { status: string };

const state = vi.hoisted(() => ({ rows: [] as TestRow[] }));
const api = vi.hoisted(() => ({
  createSession: vi.fn(),
  sendMessage: vi.fn(),
  upload: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  ClientApi: class {
    createSession = api.createSession;
    sendMessage = api.sendMessage;
    upload = api.upload;
  },
}));

vi.mock("@/db/database", () => {
  const database = {
    getAllAsync: vi.fn(async (_sql: string, serverId: string, sessionId?: string) =>
      state.rows.filter((row) => row.server_id === serverId &&
        (sessionId === undefined || row.session_id === sessionId))),
    runAsync: vi.fn(async (sql: string, ...parameters: unknown[]) => {
      if (sql.includes("status='pending'") && sql.includes("status='failed'")) {
        const [, serverId] = parameters;
        for (const row of state.rows) {
          if (row.server_id === serverId && row.status === "failed") {
            row.status = "pending";
            row.error = null;
          }
        }
        return;
      }
      if (sql.includes("status='uploading'")) {
        const [, serverId, localId] = parameters;
        updateRow(serverId, localId, { status: "uploading", error: null });
        return;
      }
      if (sql.includes("status='sending'")) {
        const [, serverId, localId] = parameters;
        updateRow(serverId, localId, { status: "sending" });
        return;
      }
      if (sql.includes("status='failed'")) {
        const [error, , serverId, localId] = parameters;
        updateRow(serverId, localId, { status: "failed", error: String(error) });
        return;
      }
      if (sql.includes("DELETE FROM outbox")) {
        const [serverId, localId] = parameters;
        state.rows = state.rows.filter((row) =>
          row.server_id !== serverId || row.local_id !== localId);
      }
    }),
  };

  function updateRow(serverId: unknown, localId: unknown, values: Partial<TestRow>) {
    const row = state.rows.find((candidate) =>
      candidate.server_id === serverId && candidate.local_id === localId);
    if (row) Object.assign(row, values);
  }

  return {
    getDatabase: vi.fn(async () => database),
    runDatabaseWrite: vi.fn(async (operation: (value: typeof database) => Promise<unknown>) =>
      operation(database)),
  };
});

const primary: Connection = {
  serverId: "primary",
  baseUrl: "http://primary.test",
  name: "Primary",
  deviceId: "device-primary",
  active: true,
};

function messageRow(serverId: string, localId: string, status: string): TestRow {
  return {
    server_id: serverId,
    local_id: localId,
    kind: "send_message",
    session_id: "session-1",
    project_id: null,
    status,
    payload: JSON.stringify({ text: "离线消息", attachments: [] }),
    error: status === "failed" ? "offline" : null,
  };
}

describe("outbox 自动恢复", () => {
  beforeEach(() => {
    state.rows = [];
    vi.clearAllMocks();
  });

  it("恢复当前 Control 的失败消息，并保持 localId 幂等", async () => {
    state.rows = [
      messageRow("primary", "local-1", "pending"),
      messageRow("secondary", "local-2", "failed"),
    ];
    api.sendMessage.mockRejectedValueOnce(new Error("network offline"));

    await processOutbox(primary);

    expect(state.rows[0]).toMatchObject({ status: "failed", error: "network offline" });
    expect(state.rows[1]).toMatchObject({ status: "failed", error: "offline" });

    await recoverFailedOutbox(primary.serverId);

    expect(state.rows[0]).toMatchObject({ status: "pending", error: null });
    expect(state.rows[1]).toMatchObject({ status: "failed", error: "offline" });

    api.sendMessage.mockResolvedValueOnce({ deduplicated: false });
    await processOutbox(primary);
    await processOutbox(primary);

    expect(state.rows).toEqual([expect.objectContaining({ server_id: "secondary" })]);
    expect(api.sendMessage).toHaveBeenCalledTimes(2);
    expect(api.sendMessage).toHaveBeenLastCalledWith("session-1", {
      localId: "local-1",
      text: "离线消息",
      attachmentIds: [],
    });
  });
});
