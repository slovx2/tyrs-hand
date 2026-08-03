import { beforeEach, describe, expect, it } from "vitest";

import { clearListOffsets, loadListOffset, saveListOffset } from "./listPosition";

describe("会话列表位置", () => {
  beforeEach(clearListOffsets);

  it("按 Control、列表和筛选状态隔离", () => {
    saveListOffset("control-a:sessions:active", 420);
    saveListOffset("control-b:sessions:active", 18);
    expect(loadListOffset("control-a:sessions:active")).toBe(420);
    expect(loadListOffset("control-b:sessions:active")).toBe(18);
    expect(loadListOffset("control-a:sessions:archived")).toBe(0);
  });

  it("不会保存负偏移", () => {
    saveListOffset("control-a:sessions:active", -20);
    expect(loadListOffset("control-a:sessions:active")).toBe(0);
  });
});

