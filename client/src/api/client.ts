import { z } from "zod";

import { getToken, type Connection } from "@/db/connections";
import { isPreviewMode, isPreviewServerId } from "@/preview/config";
import {
  attachmentSchema,
  bootstrapSchema,
  messageSchema,
  runSnapshotSchema,
  sessionSettingsSchema,
  sessionSchema,
  type SessionSettings,
} from "@/types/protocol";

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly resetRequired = false,
  ) {
    super(message);
  }
}

const problemSchema = z.object({
  title: z.string(),
  detail: z.string().optional(),
  resetRequired: z.boolean().optional(),
});

export class ClientApi {
  constructor(readonly connection: Connection) {}

  private async request<T>(path: string, schema: z.ZodType<T>, init?: RequestInit): Promise<T> {
    if (isPreviewMode && isPreviewServerId(this.connection.serverId)) {
      const { requestPreview } = await import("@/preview/runtime");
      return schema.parse(await requestPreview(this.connection.serverId, path, init));
    }
    const token = await getToken(this.connection.serverId);
    if (!token) throw new ApiError("设备凭证不存在，请重新连接", 401);
    const headers = new Headers(init?.headers);
    headers.set("Authorization", `Bearer ${token}`);
    if (init?.body && !(init.body instanceof FormData)) headers.set("Content-Type", "application/json");
    const response = await fetch(`${this.connection.baseUrl}/api/v1/client${path}`, { ...init, headers });
    if (!response.ok) {
      const parsed = problemSchema.safeParse(await response.json().catch(() => ({})));
      throw new ApiError(parsed.success ? parsed.data.detail ?? parsed.data.title : `请求失败（${response.status}）`,
        response.status, parsed.success && parsed.data.resetRequired === true);
    }
    if (response.status === 204) return undefined as T;
    return schema.parse(await response.json());
  }

  bootstrap() {
    return this.request("/bootstrap", bootstrapSchema);
  }

  listSessions(cursor = "", projectId?: string, lifecycle?: string) {
    const query = new URLSearchParams({ limit: "50" });
    if (cursor) query.set("cursor", cursor);
    if (projectId) query.set("projectId", projectId);
    if (lifecycle) query.set("lifecycle", lifecycle);
    return this.request(`/sessions?${query}`, z.object({
      sessions: z.array(sessionSchema), nextCursor: z.string(),
    }));
  }

  createSession(input: {
    projectId: string;
    settings: SessionSettings;
    initialMessage: { localId: string; text: string; attachmentIds: string[] };
  }) {
    return this.request("/sessions", z.object({ session: sessionSchema, deduplicated: z.boolean() }), {
      method: "POST", body: JSON.stringify(input),
    });
  }

  getSession(id: string) {
    return this.request(`/sessions/${id}`, z.object({
      session: sessionSchema,
      settings: sessionSettingsSchema,
      currentRun: runSnapshotSchema.nullable(),
    }));
  }

  patchSession(id: string, input: Record<string, unknown>) {
    return this.request(`/sessions/${id}`, sessionSchema,
      { method: "PATCH", body: JSON.stringify(input) });
  }

  listMessages(id: string, options: { beforeSeq?: number; afterSeq?: number; limit?: number }) {
    const query = new URLSearchParams({ limit: String(options.limit ?? 100) });
    if (options.beforeSeq !== undefined) query.set("beforeSeq", String(options.beforeSeq));
    if (options.afterSeq !== undefined) query.set("afterSeq", String(options.afterSeq));
    return this.request(`/sessions/${id}/messages?${query}`, z.object({
      messages: z.array(messageSchema), lastMessageSeq: z.number().int(),
      hasMoreBefore: z.boolean(), hasMoreAfter: z.boolean(),
    }));
  }

  sendMessage(id: string, input: { localId: string; text: string; attachmentIds: string[] }) {
    return this.request(`/sessions/${id}/messages`, z.object({
      message: messageSchema, intentId: z.string().uuid(), deduplicated: z.boolean(),
    }), { method: "POST", body: JSON.stringify({ ...input, behavior: "steer_if_active" }) });
  }

  upload(localId: string, asset: { uri: string; name: string; mimeType: string | null }) {
    const form = new FormData();
    form.append("localId", localId);
    form.append("file", { uri: asset.uri, name: asset.name,
      type: asset.mimeType ?? "application/octet-stream" } as unknown as Blob);
    return this.request("/uploads", z.object({ attachment: attachmentSchema, deduplicated: z.boolean() }),
      { method: "POST", body: form });
  }

  action(id: string, action: "stop" | "archive" | "restore") {
    return this.request(`/sessions/${id}/${action}`, z.unknown(), { method: "POST" });
  }

  executePlan(id: string, runId: string) {
    return this.request(`/sessions/${id}/plans/${runId}/execute`, z.unknown(), { method: "POST" });
  }

  answerInteractive(id: string, answer: unknown) {
    return this.request(`/interactive/${id}/answer`, z.unknown(),
      { method: "POST", body: JSON.stringify({ answer }) });
  }

  putPushToken(token: string, platform: "ios" | "android", appEnvironment: string) {
    return this.request("/device/push-token", z.unknown(), { method: "PUT",
      body: JSON.stringify({ token, platform, appEnvironment }) });
  }

  deleteDevice() {
    return this.request("/device", z.unknown(), { method: "DELETE" });
  }

  sync(afterCursor: number) {
    return this.request(`/sync?afterCursor=${afterCursor}`, z.object({
      updates: z.array(z.object({ kind: z.literal("durable"), cursor: z.number().int(),
        sessionId: z.string().uuid().nullable(), type: z.string(), entityType: z.string().optional(),
        entityId: z.string(), entitySeq: z.number().int().nullable(),
        entityVersion: z.number().int().optional(), payload: z.unknown(), createdAt: z.string() })),
      nextCursor: z.number().int(), hasMore: z.boolean(), latestCursor: z.number().int(),
    }));
  }
}
