import { z } from "zod";

import { getControlToken, type ControlConnection } from "@/db/connections";
import { isPreviewMode, isPreviewServerId } from "@/preview/config";
import { appServerTunnelSchema, controlBootstrapSchema, materializedAttachmentSchema } from "@/types/control";

const problemSchema = z.object({ title: z.string(), detail: z.string().optional() });

export class ControlApiError extends Error {
  constructor(message: string, readonly status: number) {
    super(message);
  }
}

export class ControlApi {
  constructor(readonly connection: ControlConnection) {}

  bootstrap() {
    return this.request("/bootstrap", controlBootstrapSchema);
  }

  createAppServerTunnel(workspaceId: string) {
    return this.request("/tunnels", appServerTunnelSchema, {
      method: "POST",
      body: JSON.stringify({ workspaceId }),
    });
  }

  materializeAttachment(workspaceId: string, clientId: string, asset: {
    uri: string;
    name: string;
    mimeType: string | null;
  }) {
    const form = new FormData();
    form.append("workspaceId", workspaceId);
    form.append("clientId", clientId);
    form.append("file", { uri: asset.uri, name: asset.name,
      type: asset.mimeType ?? "application/octet-stream" } as unknown as Blob);
    return this.request("/materializations", z.object({
      attachment: materializedAttachmentSchema,
      deduplicated: z.boolean(),
    }), { method: "POST", body: form });
  }

  putPushToken(token: string, platform: "ios" | "android", appEnvironment: string) {
    return this.request("/device/push-token", z.unknown(), {
      method: "PUT", body: JSON.stringify({ token, platform, appEnvironment }),
    });
  }

  deleteDevice() {
    return this.request("/device", z.unknown(), { method: "DELETE" });
  }

  private async request<Result>(path: string, schema: z.ZodType<Result>, init?: RequestInit): Promise<Result> {
    if (isPreviewMode && isPreviewServerId(this.connection.serverId)) {
      const { requestPreview } = await import("@/preview/runtime");
      return schema.parse(await requestPreview(this.connection.serverId, path, init));
    }
    const token = await getControlToken(this.connection);
    if (!token) throw new ControlApiError("设备凭证不存在，请重新连接", 401);
    const headers = new Headers(init?.headers);
    headers.set("Authorization", `Bearer ${token}`);
    if (init?.body && !(init.body instanceof FormData)) headers.set("Content-Type", "application/json");
    const response = await fetch(`${this.connection.baseUrl}/api/v1/client${path}`, { ...init, headers });
    if (!response.ok) {
      const parsed = problemSchema.safeParse(await response.json().catch(() => ({})));
      throw new ControlApiError(parsed.success ? parsed.data.detail ?? parsed.data.title :
        `Control 请求失败（${response.status}）`, response.status);
    }
    if (response.status === 204) return undefined as Result;
    return schema.parse(await response.json());
  }
}
