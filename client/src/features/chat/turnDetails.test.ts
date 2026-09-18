import { describe, expect, it, vi } from "vitest";
import type { OfficialItemPage } from "@/app-server/officialClient";
import type { MobileTurn } from "@/app-server/types";
import { TurnDetails, turnExpanded, turnWithDetails } from "./turnDetails";

const turn = (status: MobileTurn["status"] = "completed"): MobileTurn => ({ id: "turn", status,
  items: [], itemsView: "summary", error: null, startedAt: null, completedAt: null, durationMs: null });
const message = (id: string, text = id) => ({ type: "agentMessage" as const, id, text,
  phase: "commentary" as const, memoryCitation: null });

describe("页面级详情分页", () => {
  it("单轮首屏只取一页，重复展开复用，更多按钮推进游标且去重", async () => {
    const loader = vi.fn(async (_id: string, cursor: string | null) => ({
      items: cursor ? [message("a"), message("b")] : [message("a")],
      nextCursor: cursor ? null : "next" }));
    const details = new TurnDetails(loader);
    details.toggle(turn(), true);
    await Promise.all([details.load(turn()), details.load(turn())]);
    expect(loader).toHaveBeenCalledTimes(1);
    details.toggle(turn(), true);
    details.toggle(turn(), true);
    expect(loader).toHaveBeenCalledTimes(1);
    await details.load(turn());
    expect(loader).toHaveBeenLastCalledWith("turn", "next", "asc");
    expect(details.snapshot().get("turn")?.items.map((item) => item.id)).toEqual(["a", "b"]);
  });

  it("运行态从尾部加载，旧页前插，完成时忽略运行中的展开状态", async () => {
    const loader = vi.fn(async (_id: string, cursor: string | null) => ({
      items: [message(cursor ? "old" : "new")], nextCursor: cursor ? null : "older" }));
    const details = new TurnDetails(loader);
    const running = turn("inProgress");
    await details.load(running);
    expect(loader).toHaveBeenLastCalledWith("turn", null, "desc");
    await details.load(running);
    expect(details.snapshot().get("turn")?.items.map((item) => item.id)).toEqual(["old", "new"]);
    expect(turnExpanded(running, details.snapshot().get("turn"))).toBe(true);
    expect(turnExpanded(turn(), details.snapshot().get("turn"))).toBe(false);
  });

  it("离开页面后返回的请求不能提交", async () => {
    let resolve!: (page: OfficialItemPage) => void;
    const details = new TurnDetails(() => new Promise((done) => { resolve = done; }));
    const request = details.load(turn());
    await Promise.resolve();
    details.dispose();
    const before = details.snapshot();
    resolve({ items: [message("late")], nextCursor: null });
    await request;
    expect(details.snapshot()).toBe(before);
  });

  it("分页失败允许原游标重试，循环游标局部报错", async () => {
    const loader = vi.fn().mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue({ items: [], nextCursor: "same" });
    const details = new TurnDetails(loader);
    await details.load(turn());
    expect(details.snapshot().get("turn")?.error).toBe("offline");
    await details.load(turn());
    await details.load(turn());
    expect(details.snapshot().get("turn")?.error).toContain("重复游标");
  });

  it("实时文本优先于迟到详情，摘要两端顺序正确，未到底保持部分状态", async () => {
    const details = new TurnDetails(async () => ({ items: [message("live", "old")], nextCursor: "more" }));
    await details.load(turn());
    const value = turn();
    value.items = [{ type: "userMessage", id: "user", clientId: null, content: [] },
      message("live", "new"), { ...message("final"), phase: "final_answer" }];
    const merged = turnWithDetails(value, details.snapshot().get("turn"));
    expect(merged.items.map((item) => item.id)).toEqual(["user", "live", "final"]);
    expect(merged.items[1]).toMatchObject({ text: "new" });
    expect(merged.itemsView).toBe("summary");
  });

  it("打开详情不会把内存中整轮历史混进首个 50 Item 页", async () => {
    const value = turn();
    const items = Array.from({ length: 1000 }, (_, i) => message(String(i)));
    value.items = items;
    const details = new TurnDetails(async () => ({ items: items.slice(0, 50), nextCursor: "more" }));
    const request = details.load(value);
    expect(turnWithDetails(value, details.snapshot().get(value.id)).items).toHaveLength(0);
    await request;
    expect(turnWithDetails(value, details.snapshot().get(value.id)).items).toHaveLength(50);
    const updated = { ...value, items: [...value.items, message("new-live")] };
    expect(turnWithDetails(updated, details.snapshot().get(value.id)).items).toHaveLength(51);
  });

  it("未变化的旧快照不能覆盖详情页的新正文", async () => {
    const value = { ...turn(), items: [message("a", "stale")] };
    const details = new TurnDetails(async () => ({ items: [message("a", "fresh")], nextCursor: null }));
    await details.load(value);
    expect(turnWithDetails(value, details.snapshot().get(value.id)).items[0]).toMatchObject({ text: "fresh" });
    const updated = { ...value, items: [message("a", "new-live")] };
    expect(turnWithDetails(updated, details.snapshot().get(value.id)).items[0]).toMatchObject({ text: "new-live" });
  });

  it("失败与中断默认收起，仍可手动展开", () => {
    const details = new TurnDetails(vi.fn());
    for (const status of ["failed", "interrupted"] as const) {
      const value = turn(status);
      expect(turnExpanded(value)).toBe(false);
      details.toggle(value, false);
      expect(turnExpanded(value, details.snapshot().get(value.id))).toBe(true);
    }
  });
});
