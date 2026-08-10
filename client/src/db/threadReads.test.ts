import { describe, expect, it, vi } from "vitest";

import { catalogReadInsertions } from "./threadReads";

vi.mock("@/preview/config", () => ({ isPreviewMode: false }));
vi.mock("./database", () => ({ getDatabase: vi.fn(), withDatabaseTransaction: vi.fn() }));

describe("会话未读目录策略", () => {
  it("首次建立阅读基线时所有现有 Thread 都是已读", () => {
    expect(catalogReadInsertions(["old-a", "old-b"], [], false)).toEqual([
      { threadId: "old-a", hasUnread: 0 },
      { threadId: "old-b", hasUnread: 0 },
    ]);
  });

  it("基线建立后只把目录中新发现的 Thread 标为未读", () => {
    expect(catalogReadInsertions(["known", "desktop-new"], ["known"], true)).toEqual([
      { threadId: "desktop-new", hasUnread: 1 },
    ]);
  });
});
