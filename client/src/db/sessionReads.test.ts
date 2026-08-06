import { describe, expect, it } from "vitest";

import type { Session } from "@/types/protocol";
import { sessionHasUnread, sessionListIndicator, visibleSessionReadSnapshot,
  type SessionReadState } from "./sessionReadStatus";

const baseSession: Session = {
  id: "10000000-0000-4000-8000-000000000001",
  workspaceId: "10000000-0000-4000-8000-000000000002",
  projectId: "10000000-0000-4000-8000-000000000003",
  agentProfileId: "10000000-0000-4000-8000-000000000004",
  title: "会话",
  lifecycleState: "active",
  historyCompleteness: "complete",
  model: null,
  reasoningEffort: null,
  serviceTier: "standard",
  collaborationMode: "default",
  settingsVersion: 1,
  lastMessageSeq: 0,
  isRunning: false,
  hasRunIssue: false,
  lastAgentMessageSeq: 0,
  pendingInteractiveId: null,
  lastActivityAt: "2026-08-05T00:00:00.000Z",
  createdAt: "2026-08-05T00:00:00.000Z",
  updatedAt: "2026-08-05T00:00:00.000Z",
};

const readState: SessionReadState = {
  lastReadAgentSeq: 3,
  lastReadInteractiveId: "20000000-0000-4000-8000-000000000001",
};

describe("sessionHasUnread", () => {
  it("新正式回答超过本地水位时未读", () => {
    expect(sessionHasUnread({ ...baseSession, lastAgentMessageSeq: 4 }, readState)).toBe(true);
  });

  it("新的待回答交互请求未读", () => {
    expect(sessionHasUnread({ ...baseSession,
      pendingInteractiveId: "20000000-0000-4000-8000-000000000002" }, readState)).toBe(true);
  });

  it("已读取的回答和交互不再提示", () => {
    expect(sessionHasUnread({ ...baseSession, lastAgentMessageSeq: 3,
      pendingInteractiveId: readState.lastReadInteractiveId }, readState)).toBe(false);
  });

  it("交互解决后不再提示", () => {
    expect(sessionHasUnread({ ...baseSession, pendingInteractiveId: null }, readState)).toBe(false);
  });

  it("运行状态优先于未读", () => {
    expect(sessionListIndicator({ ...baseSession, isRunning: true,
      lastAgentMessageSeq: 4 }, readState)).toBe("running");
  });

  it("停止或报错状态优先于未读", () => {
    expect(sessionListIndicator({ ...baseSession, hasRunIssue: true,
      lastAgentMessageSeq: 4 }, readState)).toBe("issue");
  });

  it("运行状态优先于旧的停止或报错状态", () => {
    expect(sessionListIndicator({ ...baseSession, isRunning: true,
      hasRunIssue: true }, readState)).toBe("running");
  });

  it("没有运行和未读时不显示状态", () => {
    expect(sessionListIndicator(baseSession, readState)).toBeNull();
  });

  it("详情打开期间优先使用外层列表的最新会话水位", () => {
    const detailSnapshot = { ...baseSession, isRunning: true, lastAgentMessageSeq: 3 };
    const completedListSnapshot = { ...baseSession, isRunning: false, lastAgentMessageSeq: 4 };

    expect(visibleSessionReadSnapshot(completedListSnapshot, detailSnapshot))
      .toBe(completedListSnapshot);
  });
});
