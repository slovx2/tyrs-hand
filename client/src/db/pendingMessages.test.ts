import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/preview/config", () => ({ isPreviewMode: true }));
vi.mock("./database", () => ({ getDatabase: vi.fn(), runDatabaseWrite: vi.fn() }));

import { listPendingMessagePreviews, removePendingMessagePreview,
  savePendingMessagePreview } from "./pendingMessages";

describe("待远端确认消息贴片", () => {
  const base = { profileId: "profile-pending", threadId: "thread-1", projectId: "project-1",
    text: "消息", attachments: [] };

  beforeEach(async () => {
    await removePendingMessagePreview(base.profileId, "pending-a");
    await removePendingMessagePreview(base.profileId, "pending-b");
  });

  it("支持多贴片并按 clientMessageId 精准删除", async () => {
    await savePendingMessagePreview({ ...base, clientMessageId: "pending-a", text: "第一条" });
    await savePendingMessagePreview({ ...base, clientMessageId: "pending-b", text: "第二条" });

    expect((await listPendingMessagePreviews(base.profileId)).map((item) => item.clientMessageId))
      .toEqual(["pending-a", "pending-b"]);

    await removePendingMessagePreview(base.profileId, "pending-a");
    expect((await listPendingMessagePreviews(base.profileId)).map((item) => item.clientMessageId))
      .toEqual(["pending-b"]);
    await removePendingMessagePreview(base.profileId, "pending-b");
  });
});
