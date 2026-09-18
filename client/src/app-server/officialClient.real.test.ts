import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import type { ThreadItemsListResponse } from "@codex-app-server/v2/ThreadItemsListResponse";
import type { TurnStartResponse } from "@codex-app-server/v2/TurnStartResponse";
import type { ThreadRecord } from "./types";
import { TurnDetails, turnExpanded, turnWithDetails } from "@/features/chat/turnDetails";
import { reduceThreadNotification, isDirectThreadNotification } from "@/store/threadNotificationReducer";
import { mergeTailPage } from "@/store/threadHistory";
import { CodexJsonRpcClient, type AppServerSocket } from "./jsonRpc";
import { OfficialAppServerClient } from "./officialClient";

// 默认 CI 不连接外部服务；显式传入本轮隔离测试服务器的 target.json 才执行。
const targetPath = process.env.TYRS_HAND_REAL_CODEX_TARGET;
describe.skipIf(!targetPath)("真实 Codex 摘要与单轮分页", () => {
  it.skipIf(process.env.TYRS_HAND_REAL_CODEX_WRITE !== "true")("真实工具流与摘要刷新合并，完成自动收起", async () => {
    const target = JSON.parse(readFileSync(targetPath!, "utf8")) as { url: string; threadId: string };
    const rpc = new CodexJsonRpcClient(() => new WebSocket(target.url) as unknown as AppServerSocket);
    const client = new OfficialAppServerClient("real-stream-acceptance", rpc, {
      async prepare() {}, async setThread() {}, async markUnknown() {}, async complete() {},
    });
    await client.connect();
    try {
      const snapshot = await client.resumeThreadPage(target.threadId, "summary", 5, "paginated");
      let record: ThreadRecord = { thread: snapshot.thread, workspaceId: null, projectId: null,
        archived: false, history: { kind: "summary" } };
      let deltas = 0;
      let finish!: () => void;
      const finished = new Promise<void>((resolve) => { finish = resolve; });
      const unsubscribe = rpc.onNotification((event) => {
        if (!isDirectThreadNotification(event) || !("threadId" in event.params) ||
          event.params.threadId !== target.threadId) return;
        record = reduceThreadNotification(record, event).record;
        if (event.method === "item/agentMessage/delta") deltas += 1;
        if (event.method === "turn/completed") finish();
      });
      const started = await rpc.request<TurnStartResponse>("turn/start", { threadId: target.threadId,
        model: "gpt-5.6-sol", effort: "low", input: [{ type: "text", text_elements: [],
          text: "For mobile pagination acceptance, run exactly 52 separate shell tool calls, each executing printf probe. Do not combine the calls. Then reply exactly REAL_MOBILE_STREAM_FINAL. Do not edit files." }] });
      const details = new TurnDetails((turnId, cursor, direction) =>
        client.listTurnItems(target.threadId, turnId, cursor, direction));
      await details.load(started.turn);
      expect(details.snapshot().get(started.turn.id)?.direction).toBe("desc");
      expect(turnExpanded(started.turn)).toBe(true);
      const during = await client.listTurnPage(target.threadId, null, 5, "summary", "paginated");
      record = { ...record, thread: { ...record.thread,
        turns: mergeTailPage(record.thread.turns, during.turns).turns } };
      await finished; unsubscribe();
      const complete = record.thread.turns.find((turn) => turn.id === started.turn.id)!;
      expect(complete.status).toBe("completed");
      expect(deltas).toBeGreaterThan(0);
      expect(turnExpanded(complete, details.snapshot().get(complete.id))).toBe(false);
      const page = await client.listTurnItems(target.threadId, complete.id);
      expect(page.items).toHaveLength(50);
      expect(page.nextCursor).not.toBeNull();
      const next = await client.listTurnItems(target.threadId, complete.id, page.nextCursor);
      const expected = [...page.items, ...next.items].map((item) => item.id);
      const merged = turnWithDetails(complete, details.snapshot().get(complete.id));
      expect(new Set(merged.items.map((item) => item.id)).size).toBe(merged.items.length);
      expect(merged.items.map((item) => item.id)).toEqual(expected);
      console.info("REAL_CODEX_STREAM", JSON.stringify({ deltas, items: expected.length,
        pageSizes: [page.items.length, next.items.length], collapsed: true, mergedInOrder: true }));
    } finally { rpc.close(); }
  }, 240_000);

  it("首屏零详情，逐轮摘要分页，展开只请求一页，服务端游标无重复", async () => {
    const target = JSON.parse(readFileSync(targetPath!, "utf8")) as { url: string; threadId: string };
    const calls: { method: string; params: unknown }[] = [];
    class MeasuredRpc extends CodexJsonRpcClient {
      override request<T>(method: string, params?: unknown): Promise<T> {
        calls.push({ method, params });
        return super.request<T>(method, params);
      }
    }
    const rpc = new MeasuredRpc(() => new WebSocket(target.url) as unknown as AppServerSocket);
    const client = new OfficialAppServerClient("real-acceptance", rpc, {
      async prepare() {}, async setThread() {}, async markUnknown() {}, async complete() {},
    });
    await client.connect();
    try {
      const latest = await client.resumeThreadPage(target.threadId, "summary", 5, "paginated");
      expect(latest.thread.historyMode).toBe("paginated");
      expect(latest.page.turns).toHaveLength(5);
      expect(latest.page.turns.every((turn) => turn.itemsView === "summary" && turn.items.length <= 2)).toBe(true);
      expect(calls.filter((call) => call.method === "thread/items/list")).toHaveLength(0);
      expect(latest.page.nextCursor).not.toBeNull();
      const older = await client.listTurnPage(target.threadId, latest.page.nextCursor, 5, "summary", "paginated");
      expect(older.turns.length).toBeGreaterThan(0);
      expect(new Set([...older.turns, ...latest.page.turns].map((turn) => turn.id)).size)
        .toBe(older.turns.length + latest.page.turns.length);
      const turn = latest.page.turns.at(-1)!;
      const details = new TurnDetails((turnId, cursor, direction) =>
        client.listTurnItems(target.threadId, turnId, cursor, direction));
      expect(turnExpanded(turn)).toBe(false);
      details.toggle(turn, true);
      await details.load(turn);
      expect(calls.filter((call) => call.method === "thread/items/list")).toEqual([
        { method: "thread/items/list", params: { threadId: target.threadId, turnId: turn.id,
          cursor: null, limit: 50, sortDirection: "asc" } },
      ]);
      const loaded = details.snapshot().get(turn.id)!;
      expect(loaded.error).toBeNull();
      expect(loaded.items.length).toBeGreaterThan(2);
      details.toggle(turn, true); details.toggle(turn, true);
      expect(calls.filter((call) => call.method === "thread/items/list")).toHaveLength(1);
      // 使用真实服务端的小页核对游标协议，避免样例恰好不足 50 Item 时跳过翻页断言。
      const items: string[] = [];
      let cursor: string | null = null;
      do {
        const page: ThreadItemsListResponse = await rpc.request("thread/items/list", {
          threadId: target.threadId, turnId: turn.id, cursor, limit: 2, sortDirection: "asc" });
        expect(page.data.every((entry) => entry.turnId === turn.id)).toBe(true);
        if (cursor) expect(page.nextCursor).not.toBe(cursor);
        items.push(...page.data.map((entry) => entry.item.id)); cursor = page.nextCursor;
      } while (cursor);
      expect(new Set(items).size).toBe(items.length);
      expect(items.slice(0, 50)).toEqual(loaded.items.map((item) => item.id));
      const tail = await client.listTurnItems(target.threadId, turn.id, null, "desc");
      expect(tail.items.map((item) => item.id)).toEqual(items.slice(-50));
      console.info("REAL_CODEX_ACCEPTANCE", JSON.stringify({ summaryTurns: latest.page.turns.length,
        olderTurns: older.turns.length, firstScreenItemRequests: 0, expandedItemRequests: 1,
        totalItems: items.length, uniqueAndOrdered: true }));
    } finally { rpc.close(); }
  }, 60_000);
});
