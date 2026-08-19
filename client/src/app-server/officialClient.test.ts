import type { RequestId } from "@codex-app-server/RequestId";
import type { ServerNotification } from "@codex-app-server/ServerNotification";
import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { Thread } from "@codex-app-server/v2/Thread";
import { describe, expect, it } from "vitest";

import { JsonRpcRequestError } from "./jsonRpc";
import { latestCompletedPlan, latestExecutablePlan, normalizeGeneratedTitle,
  OfficialAppServerClient, textInput, type OfficialRpcClient } from "./officialClient";
import type { SubmissionJournal } from "./submissions";

class FakeRpc implements OfficialRpcClient {
  readonly calls: { method: string; params: unknown }[] = [];
  readonly responses: { id: RequestId; result: unknown }[] = [];
  readonly timeline: string[] = [];
  notificationListener: ((notification: ServerNotification) => void) | null = null;
  requestListener: ((request: ServerRequest) => void) | null = null;
  closeListeners = new Set<(error: Error) => void>();
  opens = 0;

  constructor(readonly handler: (method: string, params: unknown) => unknown | Promise<unknown>) {}

  async open(): Promise<unknown> { this.opens++; return {}; }
  async request<Result>(method: string, params?: unknown): Promise<Result> {
    this.calls.push({ method, params });
    this.timeline.push(method);
    return await this.handler(method, params) as Result;
  }
  respond(id: RequestId, result: unknown): void {
    this.responses.push({ id, result });
    this.timeline.push(`respond:${String(id)}`);
  }
  respondError(id: RequestId): void { this.timeline.push(`error:${String(id)}`); }
  onNotification(listener: (notification: ServerNotification) => void): () => void {
    this.notificationListener = listener;
    return () => { this.notificationListener = null; };
  }
  onServerRequest(listener: (request: ServerRequest) => void): () => void {
    this.requestListener = listener;
    return () => { this.requestListener = null; };
  }
  onClose(listener: (error: Error) => void): () => void {
    this.closeListeners.add(listener);
    return () => { this.closeListeners.delete(listener); };
  }
  emitRequest(request: ServerRequest): void { this.requestListener?.(request); }
  emitNotification(notification: ServerNotification): void { this.notificationListener?.(notification); }
  emitClose(error = new Error("network lost")): void {
    for (const listener of this.closeListeners) listener(error);
  }
}

class MemoryJournal implements SubmissionJournal {
  prepared = 0;
  unknown = 0;
  completed = 0;
  async prepare(): Promise<void> { this.prepared++; }
  async setThread(): Promise<void> {}
  async markUnknown(): Promise<void> { this.unknown++; }
  async complete(): Promise<void> { this.completed++; }
}

const preferences = { model: "gpt-test", effort: "high" as const, serviceTier: null,
  collaborationMode: "default" as const };

describe("OfficialAppServerClient", () => {
  it("App Server 断线时清理旧交互请求，并允许新连接重新登记请求", () => {
    const rpc = new FakeRpc(() => undefined);
    const client = new OfficialAppServerClient("profile-1", rpc, new MemoryJournal());
    const closeErrors: string[] = [];
    client.onClose((error) => closeErrors.push(error.message));
    const request = (id: string): ServerRequest => ({ id,
      method: "item/tool/requestUserInput", params: {
        threadId: "thread-1", turnId: "turn-1", itemId: `item-${id}`,
        questions: [], isBlocking: true, autoResolutionMs: null,
      } });

    rpc.emitRequest(request("old"));
    expect(client.pendingRequests("thread-1")).toHaveLength(1);

    rpc.emitClose(new Error("SSH App Server 连接已断开"));

    expect(client.pendingRequests("thread-1")).toEqual([]);
    expect(client.answerRequest("old", { answers: {} })).toBe(false);
    expect(closeErrors).toEqual(["SSH App Server 连接已断开"]);

    rpc.emitRequest(request("new"));
    expect(client.pendingRequests("thread-1").map((item) => String(item.id))).toEqual(["new"]);
  });

  it("使用受限 Luna 临时线程生成结构化标题并在结束后取消订阅", async () => {
    let rpc!: FakeRpc;
    rpc = new FakeRpc((method, params) => {
      if (method === "thread/start") {
        expect(params).toMatchObject({ model: "gpt-5.6-luna", ephemeral: true,
          approvalPolicy: "never", permissions: ":read-only", runtimeWorkspaceRoots: [] });
        return { thread: { ...officialThread([]), id: "title-thread", ephemeral: true } };
      }
      if (method === "turn/start") {
        expect(params).toMatchObject({ threadId: "title-thread", serviceTier: "fast",
          outputSchema: { required: ["title", "description"], properties: {
            title: { maxLength: 36 }, description: { maxLength: 100 },
          } } });
        queueMicrotask(() => {
          rpc.emitNotification({ method: "item/completed", params: { threadId: "title-thread",
            turnId: "title-turn", item: { type: "agentMessage", id: "title-item",
              text: JSON.stringify({ title: "修复移动端协议回归", description: "移动端协议和滚动" }),
              phase: "final_answer", memoryCitation: null }, completedAtMs: 1 } });
          rpc.emitNotification({ method: "turn/completed", params: { threadId: "title-thread",
            turn: officialTurn("title-turn", "completed", []) } });
        });
        return { turn: officialTurn("title-turn", "inProgress", []) };
      }
      if (method === "thread/unsubscribe") return { status: "notLoaded" };
      throw new Error(`unexpected ${method}`);
    });
    const client = new OfficialAppServerClient("profile-1", rpc, new MemoryJournal());
    const visible: string[] = [];
    client.subscribe((event) => visible.push(event.method));

    await expect(client.generateThreadTitle({ cwd: "/workspace", prompt: "修复移动端问题",
      serviceTier: "fast" })).resolves.toEqual({ title: "修复移动端协议回归",
      description: "移动端协议和滚动" });

    expect(rpc.calls.map((call) => call.method)).toEqual([
      "thread/start", "turn/start", "thread/unsubscribe",
    ]);
    expect(visible).toEqual([]);
  });

  it("标题规范化为单行、无尾部标点且最多 36 个字符", () => {
    expect(normalizeGeneratedTitle("  `修复 移动端滚动。`  ")).toBe("修复 移动端滚动");
    expect(Array.from(normalizeGeneratedTitle("一".repeat(50)) ?? "")).toHaveLength(36);
    expect(normalizeGeneratedTitle("一".repeat(50))?.endsWith("…")).toBe(true);
  });

  it("thread/list 显式传空 modelProviders 以列出所有 Provider", async () => {
    const rpc = new FakeRpc((method, params) => {
      if (method !== "thread/list") throw new Error(`unexpected ${method}`);
      expect(params).toMatchObject({ modelProviders: [], archived: false,
        sortKey: "updated_at", sortDirection: "desc" });
      return { data: [officialThread([])], nextCursor: null };
    });
    const client = new OfficialAppServerClient("profile-1", rpc, new MemoryJournal());

    expect(await client.listThreads()).toHaveLength(1);
  });

  it("仅把明确缺少 rollout 的 thread/read 视为未物化 Thread", async () => {
    const missingRpc = new FakeRpc((method) => {
      throw new JsonRpcRequestError("No rollout found for thread id thread-phantom",
        method, "rejected", -32004);
    });
    const missingClient = new OfficialAppServerClient("profile-1", missingRpc,
      new MemoryJournal());

    await expect(missingClient.readThreadMetadataIfExists("thread-phantom")).resolves.toBeNull();

    const networkRpc = new FakeRpc((method) => {
      throw new JsonRpcRequestError("thread/read 响应超时", method, "unknown");
    });
    const networkClient = new OfficialAppServerClient("profile-1", networkRpc,
      new MemoryJournal());

    await expect(networkClient.readThreadMetadataIfExists("thread-unknown"))
      .rejects.toMatchObject({ delivery: "unknown", method: "thread/read" });
  });

  it("提交恢复仅吞掉 thread/resume 明确缺少 rollout 的错误", async () => {
    const missingRpc = new FakeRpc((method) => {
      throw new JsonRpcRequestError("no rollout found for thread id thread-phantom",
        method, "rejected", -32004);
    });
    const missingClient = new OfficialAppServerClient("profile-1", missingRpc,
      new MemoryJournal());

    await expect(missingClient.resumeThreadForSubmissionIfExists("thread-phantom"))
      .resolves.toBeNull();

    const networkRpc = new FakeRpc((method) => {
      throw new JsonRpcRequestError("thread/resume 响应超时", method, "unknown");
    });
    const networkClient = new OfficialAppServerClient("profile-1", networkRpc,
      new MemoryJournal());

    await expect(networkClient.resumeThreadForSubmissionIfExists("thread-unknown"))
      .rejects.toMatchObject({ delivery: "unknown", method: "thread/resume" });
  });

  it("legacy resume 直接请求 full，并把倒序响应转成时间正序", async () => {
    const turns = Array.from({ length: 7 }, (_, index) =>
      officialTurn(`turn-${index + 1}`, "completed", [{ type: "agentMessage",
        id: `item-${index + 1}`, text: `answer-${index + 1}`,
        phase: "final_answer", memoryCitation: null }]));
    const rpc = new FakeRpc((method, params) => {
      if (method === "thread/resume") {
        expect(params).toEqual({ threadId: "thread-1", excludeTurns: true,
          initialTurnsPage: { limit: 5, sortDirection: "desc", itemsView: "full" } });
        return resumeResult(officialThread(turns), turns.slice(-5), "older-1");
      }
      throw new Error(`unexpected ${method}`);
    });
    const client = new OfficialAppServerClient("profile-1", rpc, new MemoryJournal());

    const resumed = await client.resumeThreadPage("thread-1");

    expect(resumed.thread.turns.map((turn) => turn.id)).toEqual([
      "turn-3", "turn-4", "turn-5", "turn-6", "turn-7",
    ]);
    expect(resumed.thread.turns.map((turn) => turn.items[0]?.id)).toEqual([
      "item-3", "item-4", "item-5", "item-6", "item-7",
    ]);
    expect(resumed.page.nextCursor).toBe("older-1");
  });

  it("legacy 旧页沿用官方游标、直接请求 full 并保持时间正序", async () => {
    const turns = [officialTurn("turn-1", "completed", [{ type: "agentMessage", id: "item-1",
      text: "one", phase: "final_answer", memoryCitation: null }]),
    officialTurn("turn-2", "completed", [{ type: "agentMessage", id: "item-2",
      text: "two", phase: "final_answer", memoryCitation: null }])];
    const rpc = new FakeRpc((method, params) => {
      if (method === "thread/turns/list") {
        expect(params).toEqual({ threadId: "thread-1", cursor: "older-1", limit: 5,
          sortDirection: "desc", itemsView: "full" });
        return turnPage(turns, null);
      }
      throw new Error(`unexpected ${method}`);
    });
    const client = new OfficialAppServerClient("profile-1", rpc, new MemoryJournal());

    const page = await client.listTurnPage("thread-1", "older-1");

    expect(page.turns.map((turn) => turn.id)).toEqual(["turn-1", "turn-2"]);
    expect(page.nextCursor).toBeNull();
  });

  it("paginated 会话先读取 Turn 壳，再按 Turn 读取完整 Item", async () => {
    const turns = [officialTurn("turn-1", "completed", [{ type: "agentMessage", id: "item-1",
      text: "one", phase: "final_answer", memoryCitation: null }])];
    const rpc = new FakeRpc((method, params) => {
      if (method === "thread/turns/list") {
        expect(params).toEqual({ threadId: "thread-1", cursor: null, limit: 5,
          sortDirection: "desc", itemsView: "notLoaded" });
        return turnPage(turns.map(withoutItems), null);
      }
      if (method === "thread/items/list") return itemsForTurn(turns, params);
      throw new Error(`unexpected ${method}`);
    });
    const client = new OfficialAppServerClient("profile-1", rpc, new MemoryJournal());

    const page = await client.listTurnPage("thread-1", null, 5, "full", "paginated");

    expect(page.turns[0]?.items.map((item) => item.id)).toEqual(["item-1"]);
  });

  it("新会话仅在 Item 分页探测成功时使用 paginated history", async () => {
    const createClient = (probeCode: number, expectedMode: "legacy" | "paginated") => {
      const rpc = new FakeRpc((method, params) => {
        if (method === "thread/items/list") {
          throw new JsonRpcRequestError("probe", method, "rejected", probeCode);
        }
        if (method === "thread/start") {
          expect(params).toMatchObject({ cwd: "/workspace", model: "gpt-test",
            runtimeWorkspaceRoots: ["/workspace"], historyMode: expectedMode });
          return { thread: { ...officialThread([]), historyMode: expectedMode } };
        }
        throw new Error(`unexpected ${method}`);
      });
      return { client: new OfficialAppServerClient("profile-1", rpc, new MemoryJournal()), rpc };
    };
    const unsupported = createClient(-32601, "legacy");
    const supported = createClient(-32004, "paginated");

    await unsupported.client.startThread("/workspace", "gpt-test");
    await supported.client.startThread("/workspace", "gpt-test");

    expect(unsupported.rpc.calls.map((call) => call.method)).toEqual([
      "thread/items/list", "thread/start",
    ]);
    expect(supported.rpc.calls.map((call) => call.method)).toEqual([
      "thread/items/list", "thread/start",
    ]);
  });

  it("Outbox 创建 Thread 时携带稳定 source，并能据此恢复未知响应", async () => {
    const source = "tyrs-hand-mobile:profile-1:message-1";
    const rpc = new FakeRpc((method, params) => {
      if (method === "thread/items/list") {
        throw new JsonRpcRequestError("probe", method, "rejected", -32601);
      }
      if (method === "thread/start") {
        expect(params).toMatchObject({ threadSource: source });
        return { thread: { ...officialThread([]), threadSource: source } };
      }
      if (method === "thread/list") {
        return { data: [{ ...officialThread([]), threadSource: source }], nextCursor: null };
      }
      throw new Error(`unexpected ${method}`);
    });
    const client = new OfficialAppServerClient("profile-1", rpc, new MemoryJournal());

    await client.startThread("/workspace", "gpt-test", source);
    expect((await client.findThreadBySource(source))?.threadSource).toBe(source);
  });

  it("新 Thread 使用 thread/start 返回的内存状态直接 turn/start", async () => {
    const thread = officialThread([]);
    const rpc = new FakeRpc((method) => {
      if (method === "turn/start") {
        return { turn: officialTurn("turn-first", "inProgress", []) };
      }
      throw new Error(`新 Thread 首次提交不应调用 ${method}`);
    });
    const client = new OfficialAppServerClient("profile-1", rpc, new MemoryJournal());

    const result = await client.submitNewThread(thread, { clientMessageId: "message-first",
      input: [textInput("hello")], preferences });

    expect(result).toMatchObject({ threadId: thread.id, turnId: "turn-first" });
    expect(rpc.calls.map((call) => call.method)).toEqual(["turn/start"]);
    expect(rpc.calls[0]?.params).toMatchObject({
      threadId: thread.id, clientUserMessageId: "message-first",
    });
  });

  it("Plan 未回答时先清空 requestUserInput，再 steer 当前 Turn", async () => {
    const thread = officialThread([officialTurn("turn-plan", "inProgress", [])]);
    const rpc = new FakeRpc((method) => {
      if (method === "thread/resume") return resumeResult(thread);
      if (method === "turn/steer") return { turnId: "turn-plan" };
      throw new Error(`unexpected ${method}`);
    });
    const client = new OfficialAppServerClient("profile-1", rpc, new MemoryJournal());
    rpc.emitRequest({ id: "question-1", method: "item/tool/requestUserInput", params: {
      threadId: thread.id, turnId: "turn-plan", itemId: "item-question", questions: [],
      isBlocking: true, autoResolutionMs: null,
    } });

    await client.submit({ threadId: thread.id, clientMessageId: "message-1",
      input: [textInput("continue")], preferences });

    expect(rpc.responses).toEqual([{ id: "question-1", result: { answers: {} } }]);
    expect(rpc.timeline.indexOf("respond:question-1")).toBeLessThan(rpc.timeline.indexOf("turn/steer"));
    expect(rpc.calls.find((call) => call.method === "turn/steer")?.params).toMatchObject({
      expectedTurnId: "turn-plan", clientUserMessageId: "message-1",
    });
  });

  it("expectedTurnId 不匹配时刷新官方状态并只重试一次", async () => {
    let steers = 0;
    const rpc = new FakeRpc((method) => {
      if (method === "thread/resume") return resumeResult(officialThread([
        officialTurn("turn-old", "inProgress", []),
      ]));
      if (method === "thread/read") return { thread: officialThread([]) };
      if (method === "thread/turns/list") return turnPage([
        officialTurn("turn-new", "inProgress", []),
      ]);
      if (method === "turn/steer" && steers++ === 0) {
        throw new JsonRpcRequestError("expected active turn id `turn-old` but found `turn-new`",
          "turn/steer", "rejected", -32600);
      }
      if (method === "turn/steer") return { turnId: "turn-new" };
      throw new Error(`unexpected ${method}`);
    });
    const client = new OfficialAppServerClient("profile-1", rpc, new MemoryJournal());

    const result = await client.submit({ threadId: "thread-1", clientMessageId: "message-2",
      input: [textInput("steer")], preferences });

    expect(result.turnId).toBe("turn-new");
    expect(rpc.calls.filter((call) => call.method === "turn/steer").map((call) =>
      (call.params as { expectedTurnId: string }).expectedTurnId)).toEqual(["turn-old", "turn-new"]);
    expect(rpc.calls.find((call) => call.method === "thread/read")?.params)
      .toEqual({ threadId: "thread-1", includeTurns: false });
  });

  it("idle 快照在 turn/start 前过期时刷新并改为 steer", async () => {
    const rpc = new FakeRpc((method) => {
      if (method === "thread/resume") return resumeResult(officialThread([]));
      if (method === "thread/read") return { thread: officialThread([]) };
      if (method === "thread/turns/list") return turnPage([
        officialTurn("turn-external", "inProgress", []),
      ]);
      if (method === "turn/start") {
        throw new JsonRpcRequestError("thread already has an active turn",
          "turn/start", "rejected", -32600);
      }
      if (method === "turn/steer") return { turnId: "turn-external" };
      throw new Error(`unexpected ${method}`);
    });
    const client = new OfficialAppServerClient("profile-1", rpc, new MemoryJournal());

    const result = await client.submit({ threadId: "thread-1", clientMessageId: "message-race",
      input: [textInput("join")], preferences });

    expect(result.turnId).toBe("turn-external");
    expect(rpc.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    expect(rpc.calls.find((call) => call.method === "turn/steer")?.params).toMatchObject({
      expectedTurnId: "turn-external", clientUserMessageId: "message-race",
    });
  });

  it("模糊提交先按 userMessage.clientId 恢复，不重发", async () => {
    let recoveryPages = 0;
    const recovered = officialTurn("turn-recovered", "inProgress", [{
      type: "userMessage", id: "item-user", clientId: "message-3",
      content: [textInput("hello")],
    }]);
    const rpc = new FakeRpc((method, params) => {
      if (method === "thread/resume") return resumeResult(officialThread([]));
      if (method === "thread/read") return { thread: officialThread([]) };
      if (method === "thread/turns/list") {
        expect(params).toMatchObject({ limit: 20, itemsView: "summary", sortDirection: "desc" });
        return recoveryPages++ === 0
          ? turnPage([officialTurn("turn-recent", "completed", [])], "older-recovery")
          : turnPage([recovered]);
      }
      if (method === "turn/start") {
        throw new JsonRpcRequestError("network lost", "turn/start", "unknown");
      }
      throw new Error(`unexpected ${method}`);
    });
    const journal = new MemoryJournal();
    const client = new OfficialAppServerClient("profile-1", rpc, journal);

    const result = await client.submit({ threadId: "thread-1", clientMessageId: "message-3",
      input: [textInput("hello")], preferences });

    expect(result).toMatchObject({ turnId: "turn-recovered", deduplicated: true });
    expect(rpc.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    expect(rpc.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(2);
    expect(journal.unknown).toBe(1);
  });

  it("resume 阶段断线后重新连接，并用 20 条 summary 首屏恢复", async () => {
    let resumes = 0;
    let lists = 0;
    const recovered = officialTurn("turn-bootstrap-recovered", "inProgress", [{
      type: "userMessage", id: "item-bootstrap", clientId: "message-bootstrap",
      content: [textInput("hello")],
    }]);
    const rpc = new FakeRpc((method, params) => {
      if (method === "thread/resume" && resumes++ === 0) {
        throw new JsonRpcRequestError("network lost", "thread/resume", "unknown");
      }
      if (method === "thread/turns/list" && lists++ === 0) {
        throw new JsonRpcRequestError("still reconnecting", "thread/turns/list", "unknown");
      }
      if (method === "thread/resume") {
        expect(params).toMatchObject({ excludeTurns: true,
          initialTurnsPage: { limit: 20, itemsView: "summary", sortDirection: "desc" } });
        return resumeResult(officialThread([recovered]), [recovered]);
      }
      throw new Error(`提交恢复期间不应调用 ${method}`);
    });
    const journal = new MemoryJournal();
    const client = new OfficialAppServerClient("profile-1", rpc, journal);

    const result = await client.submit({ threadId: "thread-1",
      clientMessageId: "message-bootstrap", input: [textInput("hello")], preferences });

    expect(result).toMatchObject({ turnId: "turn-bootstrap-recovered", deduplicated: true });
    expect(rpc.calls.some((call) => call.method === "turn/start" ||
      call.method === "turn/steer")).toBe(false);
    expect(rpc.opens).toBe(1);
    expect(journal.unknown).toBe(1);
  });

  it("冷启动恢复先扫描完整 summary 历史，已成功的提交不重发", async () => {
    const recovered = officialTurn("turn-persisted", "completed", [{
      type: "userMessage", id: "user-persisted", clientId: "message-persisted",
      content: [textInput("already sent")],
    }]);
    const rpc = new FakeRpc((method, params) => {
      if (method === "thread/turns/list") {
        expect(params).toMatchObject({ limit: 20, itemsView: "summary" });
        return turnPage([recovered]);
      }
      if (method === "thread/read") return { thread: officialThread([]) };
      throw new Error(`冷启动去重不应调用 ${method}`);
    });
    const journal = new MemoryJournal();
    const client = new OfficialAppServerClient("profile-1", rpc, journal);

    const result = await client.recoverSubmission({ threadId: "thread-1",
      clientMessageId: "message-persisted", projectId: "project-1",
      input: [textInput("already sent")], preferences });

    expect(result).toMatchObject({ turnId: "turn-persisted", deduplicated: true });
    expect(rpc.calls.some((call) => call.method === "turn/start" ||
      call.method === "turn/steer")).toBe(false);
    expect(journal.prepared).toBe(0);
    expect(journal.completed).toBe(1);
  });

  it("冷启动历史中未找到时，用原 payload 和同一 client id 重试", async () => {
    const materializedImage = { type: "localImage" as const,
      path: "/remote/cache/sha256.png", detail: "auto" as const };
    const rpc = new FakeRpc((method, params) => {
      if (method === "thread/turns/list") return turnPage([]);
      if (method === "thread/read") return { thread: officialThread([]) };
      if (method === "thread/resume") return resumeResult(officialThread([]));
      if (method === "turn/start") {
        expect(params).toMatchObject({ clientUserMessageId: "message-retry",
          input: [textInput("retry"), materializedImage] });
        return { turn: officialTurn("turn-retry", "inProgress", []) };
      }
      throw new Error(`unexpected ${method}`);
    });
    const journal = new MemoryJournal();
    const client = new OfficialAppServerClient("profile-1", rpc, journal);

    const result = await client.recoverSubmission({ threadId: "thread-1",
      clientMessageId: "message-retry", projectId: "project-1",
      input: [textInput("retry"), materializedImage], preferences });

    expect(result).toMatchObject({ turnId: "turn-retry", deduplicated: false });
    expect(rpc.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
    expect(journal.prepared).toBe(1);
    expect(journal.completed).toBe(1);
  });

  it("Plan 双击使用同一 client id 只提交一次", async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const rpc = new FakeRpc(async (method) => {
      if (method === "thread/resume") return resumeResult(officialThread([]));
      if (method === "turn/start") { await gate; return { turn: officialTurn("turn-1", "inProgress", []) }; }
      throw new Error(`unexpected ${method}`);
    });
    const client = new OfficialAppServerClient("profile-1", rpc, new MemoryJournal());
    const input = { threadId: "thread-1", clientMessageId: "plan:thread-1:item-plan",
      input: [textInput("PLEASE IMPLEMENT THIS PLAN:\nplan")], preferences };
    const first = client.submit(input);
    const second = client.submit(input);
    release();

    expect(await first).toEqual(await second);
    expect(rpc.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  });

  it("其他端已回答的 Server Request 不再重复回应", () => {
    const rpc = new FakeRpc(() => ({}));
    const client = new OfficialAppServerClient("profile-1", rpc, new MemoryJournal());
    rpc.emitRequest({ id: 9, method: "item/fileChange/requestApproval", params: {
      threadId: "thread-1", turnId: "turn-1", itemId: "item-1", reason: null,
      grantRoot: null, startedAtMs: 1,
    } });
    rpc.emitNotification({ method: "serverRequest/resolved", params: {
      threadId: "thread-1", requestId: 9,
    } });
    expect(client.answerRequest(9, { decision: "accept" })).toBe(false);
    expect(rpc.responses).toHaveLength(0);
  });

  it("执行计划只从最新已完成 Turn 的 Plan Item 派生", () => {
    const thread = officialThread([
      officialTurn("turn-old", "completed", [{ type: "plan", id: "plan-old", text: "old" }]),
      officialTurn("turn-running", "inProgress", [{ type: "plan", id: "plan-running", text: "draft" }]),
      officialTurn("turn-new", "completed", [{ type: "agentMessage", id: "answer", text: "done",
        phase: "final_answer", memoryCitation: null }, { type: "plan", id: "plan-new", text: "new" }]),
    ]);
    expect(latestCompletedPlan(thread)).toEqual({ turnId: "turn-new", itemId: "plan-new", text: "new" });
    expect(latestExecutablePlan(thread)).toEqual({ turnId: "turn-new", itemId: "plan-new",
      text: "new" });

    const continued = officialThread([...thread.turns,
      officialTurn("turn-implementation", "inProgress", [])]);
    expect(latestCompletedPlan(continued)).toEqual({ turnId: "turn-new", itemId: "plan-new",
      text: "new" });
    expect(latestExecutablePlan(continued)).toBeNull();
  });
});

function officialThread(turns: Thread["turns"]): Thread {
  return { id: "thread-1", sessionId: "session-1", forkedFromId: null, parentThreadId: null,
    preview: "thread", ephemeral: false, section: null, sectionEnteredAt: null, modelProvider: "openai",
    createdAt: 1, updatedAt: 2, recencyAt: 2, status: { type: turns.some((turn) =>
      turn.status === "inProgress") ? "active" : "idle", ...(turns.some((turn) =>
      turn.status === "inProgress") ? { activeFlags: [] } : {}) } as Thread["status"], path: null,
    cwd: "/workspace", cliVersion: "0.147.0", source: "appServer", threadSource: null,
    agentNickname: null, agentRole: null, gitInfo: null, name: null, turns, extra: null,
    historyMode: "legacy", canAcceptDirectInput: true };
}

function officialTurn(id: string, status: Thread["turns"][number]["status"],
  items: Thread["turns"][number]["items"]): Thread["turns"][number] {
  return { id, status, items, itemsView: "full", error: null, startedAt: 1,
    completedAt: status === "inProgress" ? null : 2, durationMs: null };
}

function turnPage(turns: Thread["turns"], nextCursor: string | null = null) {
  return { data: [...turns].reverse(), nextCursor,
    backwardsCursor: turns.length > 0 ? `back:${turns.at(-1)!.id}` : null };
}

function resumeResult(thread: Thread, turns = thread.turns,
  nextCursor: string | null = null) {
  return { thread: { ...thread, turns: [] }, initialTurnsPage: turnPage(turns, nextCursor) };
}

function withoutItems(turn: Thread["turns"][number]): Thread["turns"][number] {
  return { ...turn, items: [], itemsView: "notLoaded" };
}

function itemsForTurn(turns: Thread["turns"], params: unknown) {
  expect(params).toMatchObject({ threadId: "thread-1", cursor: null, limit: 100,
    sortDirection: "asc" });
  const turnId = (params as { turnId: string }).turnId;
  const turn = turns.find((item) => item.id === turnId);
  return { data: (turn?.items ?? []).map((item) => ({ turnId, item })),
    nextCursor: null, backwardsCursor: null };
}
