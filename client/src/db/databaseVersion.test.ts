import { describe, expect, it, vi } from "vitest";

import { DATABASE_VERSION, needsThreadHistoryCacheReset } from "./database";

vi.mock("expo-sqlite", () => ({}));

describe("数据库 v8 Outbox 迁移", () => {
  it("只让可升级的官方协议缓存执行一次失效", () => {
    expect(DATABASE_VERSION).toBe(8);
    expect(needsThreadHistoryCacheReset(3)).toBe(false);
    expect(needsThreadHistoryCacheReset(4)).toBe(true);
    expect(needsThreadHistoryCacheReset(5)).toBe(true);
    expect(needsThreadHistoryCacheReset(6)).toBe(true);
    expect(needsThreadHistoryCacheReset(7)).toBe(false);
    expect(needsThreadHistoryCacheReset(8)).toBe(false);
  });
});
