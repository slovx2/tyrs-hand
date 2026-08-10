import { describe, expect, it, vi } from "vitest";
import { discardOutboxItem, enqueueOutbox, failOutbox, listOutbox, markOutboxProcessing,
  retryOutboxItem, setOutboxThread } from "./outbox";

vi.mock("@/preview/config", () => ({ isPreviewMode: true }));
vi.mock("@/db/database", () => ({
  getDatabase: vi.fn(), runDatabaseWrite: vi.fn(),
}));

describe("原生协议 Outbox", () => {
  it("在创建 Thread 前持久化，并以同一消息 ID 恢复状态", async () => {
    const profileId = "outbox-create-profile";
    await enqueueOutbox({ profileId, clientMessageId: "message-1", kind: "create_task",
      projectId: "project-1", threadId: null,
      payload: { text: "hello", attachments: [], preferences: {
        model: "gpt-5.6", effort: "low", serviceTier: "priority",
        collaborationMode: "default",
      } } });
    await enqueueOutbox({ profileId, clientMessageId: "message-1", kind: "create_task",
      projectId: "project-1", threadId: null,
      payload: { text: "duplicate", attachments: [], preferences: {
        model: "gpt-5.6", effort: "low", serviceTier: "priority",
        collaborationMode: "default",
      } } });

    expect(await listOutbox(profileId)).toHaveLength(1);
    await markOutboxProcessing(profileId, "message-1");
    await setOutboxThread(profileId, "message-1", "thread-1");
    await failOutbox(profileId, "message-1", "network lost");
    expect((await listOutbox(profileId))[0]).toMatchObject({
      threadId: "thread-1", state: "failed", attemptCount: 1, error: "network lost",
    });

    await retryOutboxItem(profileId, "message-1");
    expect((await listOutbox(profileId))[0]).toMatchObject({ state: "pending", error: null });
    await discardOutboxItem(profileId, "message-1");
    expect(await listOutbox(profileId)).toEqual([]);
  });
});
