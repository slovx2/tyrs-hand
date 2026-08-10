import { describe, expect, it } from "vitest";

import type { PendingSubmission } from "./submissions";
import { recoverPendingProfileSubmissions } from "./submissionRecovery";

const preferences = { model: "gpt-test", effort: "high", serviceTier: null,
  collaborationMode: "default" };

describe("持久提交冷启动恢复", () => {
  it("按项目路由到正确 App Server，串行恢复并保留已物化附件", async () => {
    const records = [
      pending("message-a", "project-a", "prepared"),
      pending("message-b", "project-b", "unknown", [{ type: "localImage",
        path: "/remote/cache/image.png", detail: "auto" }]),
    ];
    let active = 0;
    let maxActive = 0;
    const calls: { workspaceId: string | null; input: unknown }[] = [];
    const result = await recoverPendingProfileSubmissions({
      profileId: "profile-routing",
      projects: [{ id: "project-a", workspaceId: "workspace-a" },
        { id: "project-b", workspaceId: "workspace-b" }],
      loadPending: async () => records,
      clientFor: (workspaceId) => ({
        connect: async () => undefined,
        recoverSubmission: async (input) => {
          active++;
          maxActive = Math.max(maxActive, active);
          await Promise.resolve();
          calls.push({ workspaceId, input });
          active--;
          return { threadId: input.threadId, turnId: "turn", deduplicated: false };
        },
      }),
    });

    expect(result).toEqual({ recovered: ["message-a", "message-b"], errors: [] });
    expect(maxActive).toBe(1);
    expect(calls.map((call) => call.workspaceId)).toEqual(["workspace-a", "workspace-b"]);
    expect(calls[1]?.input).toMatchObject({ clientMessageId: "message-b",
      input: [{ type: "text", text: "message-b" },
        { type: "localImage", path: "/remote/cache/image.png" }] });
  });

  it("同一 profile 的并发 bootstrap 复用一次恢复，坏记录不阻断后续记录", async () => {
    let loads = 0;
    let recoveries = 0;
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const input = {
      profileId: "profile-coalesced",
      projects: [{ id: "project-good", workspaceId: null }],
      loadPending: async () => {
        loads++;
        return [pending("message-bad", "missing", "prepared"),
          pending("message-good", "project-good", "unknown")];
      },
      clientFor: () => ({
        connect: async () => undefined,
        recoverSubmission: async (submission: { threadId: string; clientMessageId: string }) => {
          recoveries++;
          await gate;
          return { threadId: submission.threadId, turnId: "turn", deduplicated: false };
        },
      }),
    };

    const first = recoverPendingProfileSubmissions(input);
    const second = recoverPendingProfileSubmissions(input);
    expect(first).toBe(second);
    release();
    const [left, right] = await Promise.all([first, second]);

    expect(left).toEqual(right);
    expect(loads).toBe(1);
    expect(recoveries).toBe(1);
    expect(left.recovered).toEqual(["message-good"]);
    expect(left.errors[0]).toContain("项目已不存在");
  });
});

function pending(clientMessageId: string, projectId: string,
  state: PendingSubmission["state"], extraInput: unknown[] = []): PendingSubmission {
  return { profileId: "profile", clientMessageId, threadId: `thread-${clientMessageId}`,
    projectId, state, error: state === "unknown" ? "connection lost" : null,
    payload: { input: [{ type: "text", text: clientMessageId, text_elements: [] }, ...extraInput],
      preferences } };
}
