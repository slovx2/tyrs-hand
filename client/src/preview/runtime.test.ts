import { beforeEach, describe, expect, it } from "vitest";

import { bootstrapSchema, messageSchema, runSnapshotSchema, sessionSchema } from "@/types/protocol";
import { primaryPreviewServerId, secondaryPreviewServerId } from "./config";
import { previewSessionIds } from "./fixtures";
import {
  listPreviewConnections,
  listPreviewOutbox,
  previewMessages,
  previewSessions,
  requestPreview,
  resetPreviewState,
  setPreviewActiveConnection,
} from "./runtime";

describe("预览模式", () => {
  beforeEach(() => resetPreviewState());

  it("提供可通过生产协议校验的全部 UI 状态", async () => {
    const bootstrap = bootstrapSchema.parse(await requestPreview(primaryPreviewServerId, "/bootstrap"));
    const sessions = previewSessions(primaryPreviewServerId).map((item) => sessionSchema.parse(item));
    expect(bootstrap.projects).toHaveLength(2);
    expect(sessions.some((item) => item.lifecycleState === "archived")).toBe(true);
    expect(listPreviewOutbox(primaryPreviewServerId).map((item) => item.kind))
      .toEqual(expect.arrayContaining(["create_session", "send_message"]));

    const expectedRuns = [
      [previewSessionIds.running, "running"],
      [previewSessionIds.plan, "completed"],
      [previewSessionIds.interactive, "waiting_for_user"],
      [previewSessionIds.secret, "waiting_for_user"],
      [previewSessionIds.failed, "failed"],
    ] as const;
    for (const [sessionId, status] of expectedRuns) {
      const detail = await requestPreview(primaryPreviewServerId, `/sessions/${sessionId}`) as {
        currentRun: unknown;
      };
      expect(runSnapshotSchema.parse(detail.currentRun).status).toBe(status);
    }
    const attachments = previewMessages(primaryPreviewServerId, previewSessionIds.attachments)
      .map((item) => messageSchema.parse(item)).flatMap((item) => item.attachments);
    expect(attachments.map((item) => item.kind)).toEqual(["image", "file"]);
  });

  it("保持多 Control 隔离并支持生产界面的写操作", async () => {
    const primaryCount = previewSessions(primaryPreviewServerId).length;
    const secondaryCount = previewSessions(secondaryPreviewServerId).length;
    setPreviewActiveConnection(secondaryPreviewServerId);
    expect(listPreviewConnections().find((item) => item.active)?.serverId).toBe(secondaryPreviewServerId);

    await requestPreview(primaryPreviewServerId, `/sessions/${previewSessionIds.running}`, {
      method: "PATCH",
      body: JSON.stringify({ title: "预览中修改后的标题" }),
    });
    await requestPreview(primaryPreviewServerId, `/sessions/${previewSessionIds.archived}/restore`,
      { method: "POST" });
    expect(previewSessions(primaryPreviewServerId)).toHaveLength(primaryCount);
    expect(previewSessions(primaryPreviewServerId).find((item) => item.id === previewSessionIds.running)?.title)
      .toBe("预览中修改后的标题");
    expect(previewSessions(primaryPreviewServerId).find((item) => item.id === previewSessionIds.archived)
      ?.lifecycleState).toBe("active");
    expect(previewSessions(secondaryPreviewServerId)).toHaveLength(secondaryCount);
  });
});
