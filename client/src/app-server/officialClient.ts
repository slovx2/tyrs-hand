import type { ModeKind } from "@codex-app-server/ModeKind";
import type { ReasoningEffort } from "@codex-app-server/ReasoningEffort";
import type { RequestId } from "@codex-app-server/RequestId";
import type { ServerNotification } from "@codex-app-server/ServerNotification";
import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { ModelListResponse } from "@codex-app-server/v2/ModelListResponse";
import type { Thread } from "@codex-app-server/v2/Thread";
import type { ThreadListResponse } from "@codex-app-server/v2/ThreadListResponse";
import type { ThreadReadResponse } from "@codex-app-server/v2/ThreadReadResponse";
import type { ThreadResumeResponse } from "@codex-app-server/v2/ThreadResumeResponse";
import type { ThreadStartResponse } from "@codex-app-server/v2/ThreadStartResponse";
import type { Turn } from "@codex-app-server/v2/Turn";
import type { TurnStartParams } from "@codex-app-server/v2/TurnStartParams";
import type { TurnStartResponse } from "@codex-app-server/v2/TurnStartResponse";
import type { TurnSteerResponse } from "@codex-app-server/v2/TurnSteerResponse";
import type { UserInput } from "@codex-app-server/v2/UserInput";

import { JsonRpcRequestError } from "./jsonRpc";
import type { SubmissionJournal } from "./submissions";

export type TurnPreferences = {
  model: string;
  effort: ReasoningEffort | null;
  serviceTier: string | null;
  collaborationMode: ModeKind;
};

export type SubmitInput = {
  threadId: string;
  clientMessageId: string;
  input: UserInput[];
  preferences: TurnPreferences;
  projectId?: string | null;
};

export type SubmitResult = { threadId: string; turnId: string; deduplicated: boolean };
type EventListener = (event: ServerNotification | ServerRequest) => void;

export interface OfficialRpcClient {
  open(): Promise<unknown>;
  request<Result>(method: string, params?: unknown): Promise<Result>;
  respond(id: RequestId, result: unknown): void;
  respondError(id: RequestId, code: number, message: string, data?: unknown): void;
  onNotification(listener: (notification: ServerNotification) => void): () => void;
  onServerRequest(listener: (request: ServerRequest) => void): () => void;
}

export class SubmissionConfirmationError extends Error {
  readonly canRetry = true;
}

export class OfficialAppServerClient {
  private readonly pendingServerRequests = new Map<string, ServerRequest>();
  private readonly listeners = new Set<EventListener>();
  private readonly submissions = new Map<string, Promise<SubmitResult>>();

  constructor(
    readonly profileId: string,
    private readonly rpc: OfficialRpcClient,
    private readonly journal: SubmissionJournal,
  ) {
    rpc.onNotification((notification) => this.handleNotification(notification));
    rpc.onServerRequest((request) => {
      this.pendingServerRequests.set(String(request.id), request);
      this.emit(request);
    });
  }

  connect() {
    return this.rpc.open();
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
        archived: input.archived ?? false,
        ...(input.cwd ? { cwd: input.cwd } : {}),
      });
      threads.push(...page.data);
      cursor = page.nextCursor;
    } while (cursor);
    return threads;
  }

  async readThread(threadId: string): Promise<Thread> {
    const response = await this.rpc.request<ThreadReadResponse>("thread/read",
      { threadId, includeTurns: true });
    return response.thread;
  }

  async resumeThread(threadId: string): Promise<ThreadResumeResponse> {
    return this.rpc.request("thread/resume", { threadId });
  }

  async startThread(cwd: string, model?: string): Promise<ThreadStartResponse> {
    return this.rpc.request("thread/start", model ? { cwd, model,
      runtimeWorkspaceRoots: [cwd] } : { cwd, runtimeWorkspaceRoots: [cwd] });
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
    const active = this.submissions.get(input.clientMessageId);
    if (active) return active;
    const submission = this.submitOnce(input).finally(() => {
      if (this.submissions.get(input.clientMessageId) === submission) {
        this.submissions.delete(input.clientMessageId);
      }
    });
    this.submissions.set(input.clientMessageId, submission);
    return submission;
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

  private async submitOnce(input: SubmitInput): Promise<SubmitResult> {
    await this.journal.prepare({ profileId: this.profileId, clientMessageId: input.clientMessageId,
      threadId: input.threadId, projectId: input.projectId ?? null,
      payload: { input: input.input, preferences: input.preferences } });
    try {
      await this.resumeThread(input.threadId);
      let thread = await this.readThread(input.threadId);
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
          thread = await this.readThread(input.threadId);
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
      return findClientMessage(await this.readThread(threadId), clientMessageId);
    } catch {
      await this.rpc.open();
      await this.resumeThread(threadId);
      return findClientMessage(await this.readThread(threadId), clientMessageId);
    }
  }

  private handleNotification(notification: ServerNotification): void {
    if (notification.method === "serverRequest/resolved") {
      this.pendingServerRequests.delete(String(notification.params.requestId));
    }
    this.emit(notification);
  }

  private emit(event: ServerNotification | ServerRequest): void {
    for (const listener of this.listeners) listener(event);
  }
}

export function textInput(text: string): UserInput {
  return { type: "text", text, text_elements: [] };
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

function findClientMessage(thread: Thread, clientMessageId: string): Turn | null {
  return thread.turns.find((turn) => turn.items.some((item) =>
    item.type === "userMessage" && item.clientId === clientMessageId)) ?? null;
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
