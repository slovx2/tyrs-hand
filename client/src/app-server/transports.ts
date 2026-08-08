import { ControlApi } from "@/api/control";
import type { Connection, ControlConnection, SSHConnection } from "@/db/connections";
import { isPreviewMode, isPreviewServerId } from "@/preview/config";
import type { AppServerSocket, SocketFactory } from "./jsonRpc";

export type AppServerTarget = {
  connection: Connection;
  workspaceId?: string;
};

export function createSocketFactory(target: AppServerTarget): SocketFactory {
  return target.connection.kind === "control"
    ? controlSocketFactory(target.connection, requireWorkspaceId(target.workspaceId))
    : sshSocketFactory(target.connection);
}

function controlSocketFactory(connection: ControlConnection, workspaceId: string): SocketFactory {
  if (isPreviewMode && isPreviewServerId(connection.serverId)) {
    return async () => {
      const { createPreviewAppServerSocket } = await import("@/preview/runtime");
      return createPreviewAppServerSocket(connection.serverId);
    };
  }
  return async () => {
    const tunnel = await new ControlApi(connection).createAppServerTunnel(workspaceId);
    const base = connection.baseUrl.replace(/^http:/, "ws:").replace(/^https:/, "wss:");
    return new WebSocket(`${base}${tunnel.websocketPath}`) as unknown as AppServerSocket;
  };
}

function sshSocketFactory(connection: SSHConnection): SocketFactory {
  return async () => {
    const { openSSHAppServer } = await import("@/native/sshTransport");
    const endpoint = await openSSHAppServer(connection);
    return new WebSocket(endpoint.url) as unknown as AppServerSocket;
  };
}

function requireWorkspaceId(workspaceId: string | undefined): string {
  if (!workspaceId) throw new Error("Control App Server 连接缺少 Workspace ID");
  return workspaceId;
}
