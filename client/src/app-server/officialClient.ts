import type { RequestId } from "@codex-app-server/RequestId";
import type { ServerNotification } from "@codex-app-server/ServerNotification";
import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { ModelListResponse } from "@codex-app-server/v2/ModelListResponse";
import type { Thread } from "@codex-app-server/v2/Thread";
import type { ThreadHistoryMode } from "@codex-app-server/v2/ThreadHistoryMode";
import type { ThreadItemsListResponse } from "@codex-app-server/v2/ThreadItemsListResponse";
import type { ThreadListResponse } from "@codex-app-server/v2/ThreadListResponse";
import type { ThreadReadResponse } from "@codex-app-server/v2/ThreadReadResponse";
import type { ThreadResumeResponse } from "@codex-app-server/v2/ThreadResumeResponse";
import type { ThreadStartResponse } from "@codex-app-server/v2/ThreadStartResponse";
import type { ThreadStartParams } from "@codex-app-server/v2/ThreadStartParams";
import type { ThreadTurnsListResponse } from "@codex-app-server/v2/ThreadTurnsListResponse";
import type { Turn } from "@codex-app-server/v2/Turn";
import type { TurnItemsView } from "@codex-app-server/v2/TurnItemsView";
import type { TurnStartParams } from "@codex-app-server/v2/TurnStartParams";
import type { TurnStartResponse } from "@codex-app-server/v2/TurnStartResponse";
import type { TurnSteerResponse } from "@codex-app-server/v2/TurnSteerResponse";
import type { UserInput } from "@codex-app-server/v2/UserInput";

import { JsonRpcRequestError } from "./jsonRpc";
import { projectItemForMobile, projectThreadForMobile, projectTurnForMobile } from "./mobileProjection";
import type { SubmissionJournal } from "./submissions";
import type { ThreadPreferences } from "./types";

export type TurnPreferences = ThreadPreferences;

export type SubmitInput = {
  threadId: string;
  clientMessageId: string;
  input: UserInput[];
  preferences: TurnPreferences;
  projectId?: string | null;
};

export type NewThreadSubmitInput = Omit<SubmitInput, "threadId">;

export type SubmitResult = { threadId: string; turnId: string; deduplicated: boolean };
export type OfficialTurnPage = {
  turns: Turn[];
  nextCursor: string | null;
  backwardsCursor: string | null;
};
export type ResumedThreadPage = {
  thread: Thread;
  page: OfficialTurnPage;
  preferences?: Omit<ThreadPreferences, "collaborationMode">;
};
export type GeneratedThreadTitle = { title: string; description: string };
type EventListener = (event: ServerNotification | ServerRequest) => void;

export const THREAD_PAGE_SIZE = 5;
const RECOVERY_PAGE_SIZE = 20;
const THREAD_ITEM_PAGE_SIZE = 100;
const PAGINATED_HISTORY_PROBE_THREAD_ID = "00000000-0000-7000-8000-000000000000";
const TITLE_MODEL = "gpt-5.6-luna";
const TITLE_TIMEOUT_MS = 30_000;
const TITLE_PROMPT_MAX_CHARS = 2_000;
const TITLE_MAX_CHARS = 36;

export interface OfficialRpcClient {
  open(): Promise<unknown>;
  request<Result>(method: string, params?: unknown): Promise<Result>;
  respond(id: RequestId, result: unknown): void;
  respondError(id: RequestId, code: number, message: string, data?: unknown): void;
  onNotification(listener: (notification: ServerNotification) => void): () => void;
  onServerRequest(listener: (request: ServerRequest) => void): () => void;
  onClose(listener: (error: Error) => void): () => void;
}

export class SubmissionConfirmationError extends Error {
  readonly canRetry = true;
}

export class OfficialAppServerClient {
  private readonly pendingServerRequests = new Map<string, ServerRequest>();
  private readonly listeners = new Set<EventListener>();
  private readonly submissions = new Map<string, Promise<SubmitResult>>();
  private readonly internalThreadIds = new Set<string>();
  private readonly internalNotificationListeners = new Set<(event: ServerNotification) => void>();
  private paginatedHistorySupport: Promise<boolean> | null = null;

  constructor(
    readonly profileId: string,
    private readonly rpc: OfficialRpcClient,
    private readonly journal: SubmissionJournal,
  ) {
    rpc.onNotification((notification) => this.handleNotification(notification));
    rpc.onServerRequest((request) => {
      const threadId = requestThreadId(request);
      if (threadId && this.internalThreadIds.has(threadId)) {
        rpc.respondError(request.id, -32000, "internal title thread does not accept requests");
        return;
      }
      this.pendingServerRequests.set(String(request.id), request);
      this.emit(request);
    });
    rpc.onClose(() => {
      this.pendingServerRequests.clear();
    });
  }

  connect() {
    return this.rpc.open();
  }

  onClose(listener: (error: Error) => void): () => void {
    return this.rpc.onClose(listener);
  }

  subscribe(listener: EventListener): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  async listThreads(input: { cwd?: string; archived?: boolean } = {}): Promise<Thread[]> {
    const threads: Thread[] = [];
    let cursor: string | null = null;
    do {
      const page: ThreadListResponse = await this.rpc.request("thread/list", {
        cursor, limit: 100, sortKey: "updated_at", sortDirection: "desc",
        modelProviders: [],
        archived: input.archived ?? false,
        ...(input.cwd ? { cwd: input.cwd } : {}),
      });
      threads.push(...page.data.map(projectThreadForMobile));
      cursor = page.nextCursor;
    } while (cursor);
    return threads;
  }

  async readThreadMetadata(threadId: string): Promise<Thread> {
    const response = await this.rpc.request<ThreadReadResponse>("thread/read",
      { threadId, includeTurns: false });
    return projectThreadForMobile(response.thread);
  }

  async readThreadMetadataIfExists(threadId: string): Promise<Thread | null> {
    try {
      return await this.readThreadMetadata(threadId);
    } catch (error) {
      if (isUnmaterializedThread(error)) return null;
      throw error;
    }
  }

  async resumeThreadForSubmissionIfExists(threadId: string): Promise<Thread | null> {
    try {
      return (await this.resumeThreadPage(threadId, "summary")).thread;
    } catch (error) {
      if (isUnmaterializedThread(error)) return null;
      throw error;
    }
  }

  async resumeThreadPage(threadId: string, itemsView: TurnItemsView = "full",
    limit = THREAD_PAGE_SIZE, historyMode: ThreadHistoryMode = "legacy"): Promise<ResumedThreadPage> {
    const paginateItems = itemsView === "full" && historyMode === "paginated";
    const response = await this.rpc.request<ThreadResumeResponse>("thread/resume", {
      threadId,
      excludeTurns: true,
      initialTurnsPage: { limit, sortDirection: "desc",
        itemsView: paginateItems ? "notLoaded" : itemsView },
    });
    const shellPage = chronologicalPage(response.initialTurnsPage ?? emptyPage());
    const page = paginateItems
      ? { ...shellPage, turns: await this.hydrateTurnItems(threadId, shellPage.turns) }
      : shellPage;
    return {
      thread: { ...projectThreadForMobile(response.thread), turns: page.turns },
      page,
      preferences: { model: response.model, effort: response.reasoningEffort,
        serviceTier: response.serviceTier },
    };
  }

  async listTurnPage(threadId: string, cursor: string | null, limit = THREAD_PAGE_SIZE,
    itemsView: TurnItemsView = "full",
    historyMode: ThreadHistoryMode = "legacy"): Promise<OfficialTurnPage> {
    const paginateItems = itemsView === "full" && historyMode === "paginated";
    const response = await this.rpc.request<ThreadTurnsListResponse>("thread/turns/list", {
      threadId, cursor, limit, sortDirection: "desc",
      itemsView: paginateItems ? "notLoaded" : itemsView,
    });
    const page = chronologicalPage(response);
    return paginateItems
      ? { ...page, turns: await this.hydrateTurnItems(threadId, page.turns) }
      : page;
  }

  private async hydrateTurnItems(threadId: string, turns: Turn[]): Promise<Turn[]> {
    return Promise.all(turns.map(async (turn) => ({
      ...projectTurnForMobile(turn),
      items: await this.listAllTurnItems(threadId, turn.id),
      itemsView: "full" as const,
    })));
  }

  private async listAllTurnItems(threadId: string, turnId: string): Promise<Turn["items"]> {
    const items: Turn["items"] = [];
    const seen = new Set<string>();
    let cursor: string | null = null;
    do {
      if (cursor && seen.has(cursor)) throw new Error("thread/items/list 返回了重复游标");
      if (cursor) seen.add(cursor);
      const page: ThreadItemsListResponse = await this.rpc.request("thread/items/list", {
        threadId, turnId, cursor, limit: THREAD_ITEM_PAGE_SIZE, sortDirection: "asc",
      });
      items.push(...page.data.map((entry) => projectItemForMobile(entry.item)));
      cursor = page.nextCursor;
    } while (cursor);
    return items;
  }

  async startThread(cwd: string, model?: string, threadSource?: string): Promise<ThreadStartResponse> {
    const historyMode: ThreadHistoryMode = await this.supportsPaginatedHistory()
      ? "paginated" : "legacy";
    const params: ThreadStartParams = model
      ? { cwd, model, runtimeWorkspaceRoots: [cwd], historyMode }
      : { cwd, runtimeWorkspaceRoots: [cwd], historyMode };
    if (threadSource) {
      (params as ThreadStartParams & { threadSource: string }).threadSource = threadSource;
    }
    const response = await this.rpc.request<ThreadStartResponse>("thread/start", params);
    return { ...response, thread: projectThreadForMobile(response.thread) };
  }

  async findThreadBySource(threadSource: string): Promise<Thread | null> {
    for (const thread of await this.listThreads()) {
      if (thread.threadSource === threadSource) return thread;
    }
    return null;
  }

  private supportsPaginatedHistory(): Promise<boolean> {
    if (this.paginatedHistorySupport) return this.paginatedHistorySupport;
    this.paginatedHistorySupport = this.probePaginatedHistorySupport();
    return this.paginatedHistorySupport;
  }

  private async probePaginatedHistorySupport(): Promise<boolean> {
    try {
      await this.rpc.request<ThreadItemsListResponse>("thread/items/list", {
        threadId: PAGINATED_HISTORY_PROBE_THREAD_ID,
        cursor: null,
        limit: 1,
        sortDirection: "asc",
      });
      return true;
    } catch (error) {
      // 不存在的 Thread 返回业务错误，说明方法已实现；-32601 才表示 Store 不支持分页 Item。
      return error instanceof JsonRpcRequestError && error.delivery === "rejected" &&
        error.code !== -32601;
    }
  }

  async listModels(): Promise<ModelListResponse["data"]> {
    const models: ModelListResponse["data"] = [];
    let cursor: string | null = null;
    do {
      const page: ModelListResponse = await this.rpc.request("model/list", { cursor, limit: 100 });
      models.push(...page.data);
      cursor = page.nextCursor;
    } while (cursor);
    return models;
  }

  submit(input: SubmitInput): Promise<SubmitResult> {
    return this.trackSubmission(input, null);
  }

  submitNewThread(thread: Thread, input: NewThreadSubmitInput): Promise<SubmitResult> {
    return this.trackSubmission({ ...input, threadId: thread.id }, thread);
  }

  recoverSubmission(input: SubmitInput): Promise<SubmitResult> {
    const active = this.submissions.get(input.clientMessageId);
    if (active) return active;
    const submission = this.recoverOnce(input).finally(() => {
      if (this.submissions.get(input.clientMessageId) === submission) {
        this.submissions.delete(input.clientMessageId);
      }
    });
    this.submissions.set(input.clientMessageId, submission);
    return submission;
  }

  private trackSubmission(input: SubmitInput, startedThread: Thread | null): Promise<SubmitResult> {
    const active = this.submissions.get(input.clientMessageId);
    if (active) return active;
    const submission = this.submitOnce(input, startedThread).finally(() => {
      if (this.submissions.get(input.clientMessageId) === submission) {
        this.submissions.delete(input.clientMessageId);
      }
    });
    this.submissions.set(input.clientMessageId, submission);
    return submission;
  }

  private async recoverOnce(input: SubmitInput): Promise<SubmitResult> {
    const submitted = await this.findSubmittedTurn(input.threadId, input.clientMessageId);
    if (submitted) return this.completeRecovered(input, input.threadId, submitted.id);
    return this.submitOnce(input, null);
  }

  async interrupt(threadId: string, turnId: string): Promise<void> {
    await this.rpc.request("turn/interrupt", { threadId, turnId });
  }

  async archive(threadId: string): Promise<void> {
    await this.rpc.request("thread/archive", { threadId });
  }

  async unarchive(threadId: string): Promise<Thread> {
    return (await this.rpc.request<{ thread: Thread }>("thread/unarchive", { threadId })).thread;
  }

  async setThreadName(threadId: string, name: string): Promise<void> {
    await this.rpc.request("thread/name/set", { threadId, name });
  }

  async generateThreadTitle(input: { cwd: string; prompt: string;
    serviceTier: string | null }): Promise<GeneratedThreadTitle | null> {
    const prompt = input.prompt.trim();
    if (!prompt) return null;
    const params: ThreadStartParams = {
      model: TITLE_MODEL,
      modelProvider: null,
      allowProviderModelFallback: true,
      cwd: input.cwd,
      runtimeWorkspaceRoots: [],
      approvalPolicy: "never",
      permissions: ":read-only",
      config: titleThreadConfig(),
      personality: null,
      ephemeral: true,
      threadSource: "system",
      experimentalRawEvents: false,
      dynamicTools: null,
      serviceTier: input.serviceTier,
    };
    const response = await this.rpc.request<ThreadStartResponse>("thread/start", params);
    const threadId = response.thread.id;
    this.internalThreadIds.add(threadId);
    try {
      return await this.runTitleTurn(threadId,
        titleGenerationPrompt(sliceCharacters(prompt, TITLE_PROMPT_MAX_CHARS)), input.serviceTier);
    } finally {
      await this.rpc.request("thread/unsubscribe", { threadId }).catch(() => undefined);
      this.internalThreadIds.delete(threadId);
    }
  }

  pendingRequests(threadId?: string): ServerRequest[] {
    return [...this.pendingServerRequests.values()].filter((request) =>
      !threadId || requestThreadId(request) === threadId);
  }

  answerRequest(id: RequestId, result: unknown): boolean {
    if (!this.pendingServerRequests.delete(String(id))) return false;
    this.rpc.respond(id, result);
    return true;
  }

  async dismissPendingRequests(threadId: string): Promise<void> {
    for (const request of this.pendingRequests(threadId)) {
      const result = dismissalResult(request);
      if (result !== undefined) this.answerRequest(request.id, result);
      else {
        this.pendingServerRequests.delete(String(request.id));
        this.rpc.respondError(request.id, -32000, "dismissed before user message");
      }
    }
  }

  private async submitOnce(input: SubmitInput, startedThread: Thread | null): Promise<SubmitResult> {
    await this.journal.prepare({ profileId: this.profileId, clientMessageId: input.clientMessageId,
      threadId: input.threadId, projectId: input.projectId ?? null,
      payload: { input: input.input, preferences: input.preferences } });
    try {
      let thread = startedThread;
      if (!thread) {
        thread = (await this.resumeThreadPage(input.threadId, "summary")).thread;
      }
      const duplicate = findClientMessage(thread, input.clientMessageId);
      if (duplicate) return this.completeRecovered(input, thread.id, duplicate.id);

      for (let attempt = 0; attempt < 2; attempt++) {
        await this.dismissPendingRequests(input.threadId);
        try {
          const result = await this.sendAgainstState(thread, input);
          await this.journal.complete(this.profileId, input.clientMessageId);
          return result;
        } catch (error) {
          if (!isTurnStateMismatch(error) || attempt > 0) throw error;
          thread = await this.readRecentThreadState(input.threadId);
          const submitted = findClientMessage(thread, input.clientMessageId);
          if (submitted) return this.completeRecovered(input, thread.id, submitted.id);
        }
      }
      throw new Error("官方 Turn 状态刷新后仍无法提交");
    } catch (error) {
      if (!(error instanceof JsonRpcRequestError) || error.delivery !== "unknown") throw error;
      await this.journal.markUnknown(this.profileId, input.clientMessageId, error.message);
      const recovered = await this.findSubmittedTurn(input.threadId, input.clientMessageId);
      if (recovered) return this.completeRecovered(input, input.threadId, recovered.id);
      throw new SubmissionConfirmationError("官方历史中未找到这次提交，可使用同一消息 ID 重试");
    }
  }

  private async completeRecovered(input: SubmitInput, threadId: string,
    turnId: string): Promise<SubmitResult> {
    await this.journal.complete(this.profileId, input.clientMessageId);
    return { threadId, turnId, deduplicated: true };
  }

  private async sendAgainstState(thread: Thread, input: SubmitInput): Promise<SubmitResult> {
    const active = latestActiveTurn(thread);
    if (active) {
      const response = await this.rpc.request<TurnSteerResponse>("turn/steer", {
        threadId: thread.id, clientUserMessageId: input.clientMessageId,
        input: input.input, expectedTurnId: active.id,
      });
      return { threadId: thread.id, turnId: response.turnId, deduplicated: false };
    }
    const params: TurnStartParams = {
      threadId: thread.id,
      clientUserMessageId: input.clientMessageId,
      input: input.input,
      model: input.preferences.model,
      effort: input.preferences.effort,
      serviceTier: input.preferences.serviceTier,
      collaborationMode: {
        mode: input.preferences.collaborationMode,
        settings: { model: input.preferences.model, reasoning_effort: input.preferences.effort,
          developer_instructions: null },
      },
    };
    const response = await this.rpc.request<TurnStartResponse>("turn/start", params);
    return { threadId: thread.id, turnId: response.turn.id, deduplicated: false };
  }

  private async findSubmittedTurn(threadId: string, clientMessageId: string): Promise<Turn | null> {
    try {
      return await this.scanSubmittedTurn(threadId, clientMessageId);
    } catch {
      await this.rpc.open();
      return this.scanSubmittedTurn(threadId, clientMessageId, true);
    }
  }

  private async readRecentThreadState(threadId: string): Promise<Thread> {
    const [metadata, page] = await Promise.all([
      this.readThreadMetadata(threadId),
      this.listTurnPage(threadId, null, THREAD_PAGE_SIZE, "summary"),
    ]);
    return { ...metadata, turns: page.turns };
  }

  private async scanSubmittedTurn(threadId: string, clientMessageId: string,
    resume = false): Promise<Turn | null> {
    let thread: Thread;
    let cursor: string | null;
    if (resume) {
      const resumed = await this.resumeThreadPage(threadId, "summary", RECOVERY_PAGE_SIZE);
      thread = resumed.thread;
      cursor = resumed.page.nextCursor;
    } else {
      const page = await this.listTurnPage(threadId, null, RECOVERY_PAGE_SIZE, "summary");
      thread = { ...(await this.readThreadMetadata(threadId)), turns: page.turns };
      cursor = page.nextCursor;
    }
    const recent = findClientMessage(thread, clientMessageId);
    if (recent) return recent;
    const seen = new Set<string>();
    while (cursor) {
      if (seen.has(cursor)) throw new Error("thread/turns/list 返回了重复游标");
      seen.add(cursor);
      const page = await this.listTurnPage(threadId, cursor, RECOVERY_PAGE_SIZE, "summary");
      const submitted = page.turns.find((turn) => hasClientMessage(turn, clientMessageId));
      if (submitted) return submitted;
      cursor = page.nextCursor;
    }
    return null;
  }

  private runTitleTurn(threadId: string, prompt: string,
    serviceTier: string | null): Promise<GeneratedThreadTitle | null> {
    let turnId: string | null = null;
    let output = "";
    let settled = false;
    return new Promise<GeneratedThreadTitle | null>((resolve, reject) => {
      const finish = (result: GeneratedThreadTitle | null, error?: Error) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        this.internalNotificationListeners.delete(onNotification);
        if (error) reject(error); else resolve(result);
      };
      const onNotification = (notification: ServerNotification) => {
        const event = notification as { method: string; params: unknown };
        const params = event.params as { threadId?: unknown; turnId?: unknown;
          delta?: unknown; item?: unknown; turn?: unknown };
        if (params?.threadId !== threadId) return;
        if (typeof params.turnId === "string" && turnId && params.turnId !== turnId) return;
        if (event.method === "turn/started") {
          const started = params.turn as { id?: unknown } | undefined;
          if (typeof started?.id === "string") turnId = started.id;
        } else if (event.method === "item/agentMessage/delta" &&
          typeof params.delta === "string") {
          output += params.delta;
        } else if (event.method === "item/completed") {
          const item = params.item as { type?: unknown; text?: unknown } | undefined;
          if (item?.type === "agentMessage" && typeof item.text === "string") output = item.text;
        } else if (event.method === "turn/completed") {
          const turn = params.turn as { id?: unknown; status?: unknown; error?: {
            message?: unknown } | null } | undefined;
          if (typeof turn?.id !== "string" || turnId && turn.id !== turnId) return;
          turnId = turn.id;
          if (turn.status !== "completed") {
            finish(null, new Error(`Luna 标题 Turn 终态为 ${String(turn.status)}`));
            return;
          }
          try {
            finish(parseGeneratedTitle(output));
          } catch (error) {
            finish(null, error instanceof Error ? error : new Error("Luna 标题输出无效"));
          }
        }
      };
      const timer = setTimeout(() => {
        if (turnId) void this.rpc.request("turn/interrupt", { threadId, turnId })
          .catch(() => undefined);
        finish(null, new Error("等待 Luna 标题超时"));
      }, TITLE_TIMEOUT_MS);
      this.internalNotificationListeners.add(onNotification);
      const params: TurnStartParams = {
        threadId,
        input: [textInput(prompt)],
        cwd: null,
        approvalPolicy: null,
        permissions: ":read-only",
        runtimeWorkspaceRoots: [],
        model: null,
        effort: null,
        serviceTier,
        summary: "auto",
        personality: null,
        outputSchema: titleOutputSchema(),
        collaborationMode: null,
      };
      void this.rpc.request<TurnStartResponse>("turn/start", params).then((response) => {
        turnId = response.turn.id;
      }).catch((error) => finish(null,
        error instanceof Error ? error : new Error("无法启动 Luna 标题 Turn")));
    });
  }

  private handleNotification(notification: ServerNotification): void {
    if (notification.method === "serverRequest/resolved") {
      this.pendingServerRequests.delete(String(notification.params.requestId));
    }
    const ephemeralThreadId = ephemeralStartedThreadId(notification);
    if (ephemeralThreadId) this.internalThreadIds.add(ephemeralThreadId);
    for (const listener of this.internalNotificationListeners) listener(notification);
    const threadId = notificationThreadId(notification);
    if (ephemeralThreadId || threadId && this.internalThreadIds.has(threadId)) return;
    this.emit(notification);
  }

  private emit(event: ServerNotification | ServerRequest): void {
    for (const listener of this.listeners) listener(event);
  }
}

export function textInput(text: string): UserInput {
  return { type: "text", text, text_elements: [] };
}

export function normalizeGeneratedTitle(value: string): string | null {
  let title = value.split(/\r?\n/, 1)[0]?.trim() ?? "";
  title = title.replace(/^title[:\s]+/i, "")
    .replace(/^[`"'“”‘’]+|[`"'“”‘’]+$/g, "")
    .replace(/\s+/g, " ").replace(/[.?!。？！]+$/, "").trim();
  if (!title) return null;
  const characters = Array.from(title);
  return characters.length <= TITLE_MAX_CHARS
    ? title : `${characters.slice(0, TITLE_MAX_CHARS - 1).join("").trimEnd()}…`;
}

function parseGeneratedTitle(raw: string): GeneratedThreadTitle | null {
  if (!raw.trim()) throw new Error("Luna 标题 Turn 没有最终输出");
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    throw new Error("Luna 标题不符合结构化输出");
  }
  if (!value || typeof value !== "object") throw new Error("Luna 标题不符合结构化输出");
  const result = value as { title?: unknown; description?: unknown };
  const title = typeof result.title === "string" ? normalizeGeneratedTitle(result.title) : null;
  if (!title || typeof result.description !== "string" || !result.description.trim()) {
    throw new Error("Luna 标题不符合结构化输出");
  }
  return { title, description: sliceCharacters(result.description.replace(/\s+/g, " ").trim(), 100) };
}

function titleOutputSchema(): NonNullable<TurnStartParams["outputSchema"]> {
  return { type: "object", additionalProperties: false, required: ["title", "description"],
    properties: {
      title: { type: "string", minLength: 1, maxLength: TITLE_MAX_CHARS },
      description: { type: "string", minLength: 1, maxLength: 100 },
    } };
}

function titleThreadConfig(): NonNullable<ThreadStartParams["config"]> {
  return {
    model_reasoning_effort: "low",
    "features.enable_fanout": false,
    "features.hooks": false,
    "features.multi_agent": false,
    "features.multi_agent_v2": false,
    "features.plugins": false,
    "features.tool_suggest": false,
    "features.apps": false,
    apps: { _default: { enabled: false, destructive_enabled: false, open_world_enabled: false } },
    web_search: "disabled",
  };
}

function titleGenerationPrompt(prompt: string): string {
  return [
    "You are a helpful assistant. You will be presented with a user prompt, and your job is to provide a short title for a task that will be created from that prompt.",
    "The tasks typically have to do with coding-related tasks, for example requests for bug fixes or questions about a codebase. The title you generate will be shown in the UI to represent the prompt.",
    "Generate a concise UI title (up to 36 characters) for this task.",
    "Fill the structured title field with plain text.",
    "Fill the structured description field with a compact, search-oriented summary (up to 100 characters). Include concrete project names, code areas, artifacts, people, or recurring responsibility terms when relevant so the thread is easy to retrieve by keyword.",
    "Do not include quotes, markdown, formatting characters, or trailing punctuation in either value.",
    "If the task includes a ticket reference (e.g. ABC-123), include it verbatim.",
    "",
    "Generate a clear, informative task title based solely on the prompt provided. Follow the rules below to ensure consistency, readability, and usefulness.",
    "",
    "How to write a good title:",
    "Generate a single-line title that captures the question or core change requested. The title should be easy to scan and useful in changelogs or review queues.",
    "- Use an imperative verb first: \"Add\", \"Fix\", \"Update\", \"Refactor\", \"Remove\", \"Locate\", \"Find\", etc.",
    "- Keep it under 36 characters and under 5 words where possible.",
    "If the user's prompt is already a short clear title, reuse it verbatim.",
    "- Capitalize only the first word (unless locale requires otherwise).",
    "- Write the title in the user's locale.",
    "- Do not use punctuation at the end.",
    "- Output the title as plain text with no surrounding quotes or backticks.",
    "- Use precise, non-redundant language.",
    "- Translate fixed phrases into the user's locale, but leave code terms in English unless a widely adopted translation exists.",
    "- If the user provides a title explicitly, reuse it (translated if needed) and skip generation logic.",
    "- Make it clear when the user is requesting changes versus asking a question.",
    "- Do NOT respond to the user, answer questions, or attempt to solve the problem; just write a title that can represent the user's query.",
    "",
    "Examples:",
    "- User: \"Can we add dark-mode support to the settings page?\" -> Add dark-mode support",
    "- User: \"Fehlerbehebung: Beim Anmelden erscheint 500.\" (de-DE) -> Login-Fehler 500 beheben",
    "- User: \"Refactoriser le composant sidebar pour réduire le code dupliqué.\" (fr-FR) -> Refactoriser composant sidebar",
    "- User: \"How do I fix our login bug?\" -> Troubleshoot login bug",
    "- User: \"Where in the codebase is foo_bar created\" -> Locate foo_bar",
    "- User: \"what's 2+2\" -> Calculate 2+2",
    "",
    "User prompt:",
    prompt,
  ].join("\n");
}

function sliceCharacters(value: string, limit: number): string {
  return Array.from(value).slice(0, limit).join("");
}

function notificationThreadId(notification: ServerNotification): string | null {
  const params = notification.params as { threadId?: unknown; thread?: { id?: unknown } };
  if (typeof params?.threadId === "string") return params.threadId;
  return typeof params?.thread?.id === "string" ? params.thread.id : null;
}

function ephemeralStartedThreadId(notification: ServerNotification): string | null {
  if (notification.method !== "thread/started") return null;
  const params = notification.params as { thread?: { id?: unknown; ephemeral?: unknown } };
  return params.thread?.ephemeral === true && typeof params.thread.id === "string"
    ? params.thread.id : null;
}

export function latestActiveTurn(thread: Thread): Turn | null {
  return [...thread.turns].reverse().find((turn) => turn.status === "inProgress") ?? null;
}

export function latestCompletedPlan(thread: Thread): { turnId: string; itemId: string; text: string } | null {
  for (const turn of [...thread.turns].reverse()) {
    if (turn.status !== "completed") continue;
    const plan = [...turn.items].reverse().find((item) => item.type === "plan");
    return plan?.type === "plan" ? { turnId: turn.id, itemId: plan.id, text: plan.text } : null;
  }
  return null;
}

export function latestExecutablePlan(thread: Thread): {
  turnId: string;
  itemId: string;
  text: string;
} | null {
  const turn = thread.turns.at(-1);
  if (!turn || turn.status !== "completed") return null;
  const plan = [...turn.items].reverse().find((item) => item.type === "plan");
  return plan?.type === "plan" ? { turnId: turn.id, itemId: plan.id, text: plan.text } : null;
}

function findClientMessage(thread: Thread, clientMessageId: string): Turn | null {
  return thread.turns.find((turn) => hasClientMessage(turn, clientMessageId)) ?? null;
}

function hasClientMessage(turn: Turn, clientMessageId: string): boolean {
  return turn.items.some((item) =>
    item.type === "userMessage" && item.clientId === clientMessageId);
}

function chronologicalPage(page: ThreadTurnsListResponse): OfficialTurnPage {
  return {
    turns: [...page.data].reverse().map(projectTurnForMobile),
    nextCursor: page.nextCursor,
    backwardsCursor: page.backwardsCursor,
  };
}

function emptyPage(): ThreadTurnsListResponse {
  return { data: [], nextCursor: null, backwardsCursor: null };
}

function requestThreadId(request: ServerRequest): string | null {
  const params = request.params as { threadId?: unknown };
  return typeof params.threadId === "string" ? params.threadId : null;
}

function dismissalResult(request: ServerRequest): unknown | undefined {
  switch (request.method) {
  case "item/commandExecution/requestApproval": return { decision: "decline" };
  case "item/fileChange/requestApproval": return { decision: "decline" };
  case "item/tool/requestUserInput": return { answers: {} };
  case "mcpServer/elicitation/request": return { action: "cancel", content: null, _meta: null };
  case "item/permissions/requestApproval": return { permissions: {}, scope: "turn" };
  case "applyPatchApproval": return { decision: "abort" };
  case "execCommandApproval": return { decision: "abort" };
  default: return undefined;
  }
}

function isTurnStateMismatch(error: unknown): error is JsonRpcRequestError {
  if (!(error instanceof JsonRpcRequestError) || error.delivery !== "rejected") return false;
  const message = error.message.toLowerCase();
  return message.includes("no active turn") || message.includes("active turn already") ||
    message.includes("already has an active turn") || message.includes("expected active turn") ||
    message.includes("expectedturnid");
}

function isUnmaterializedThread(error: unknown): error is JsonRpcRequestError {
  return error instanceof JsonRpcRequestError && error.delivery === "rejected" &&
    (error.method === "thread/read" || error.method === "thread/resume") &&
    error.message.toLowerCase().includes("no rollout found for thread id");
}
