import type { Connection, SSHConnection } from "@/db/connections";
import { isPreviewMode, isPreviewServerId } from "@/preview/config";
import type { AppServerSocket, SocketFactory } from "./jsonRpc";

export type AppServerTarget = {
  connection: Connection;
  workspaceId?: string;
};

export function createSocketFactory(target: AppServerTarget): SocketFactory {
  if (isPreviewMode && isPreviewServerId(target.connection.profileId)) {
    return async () => {
      const { createPreviewAppServerSocket } = await import("@/preview/runtime");
      return createPreviewAppServerSocket(target.connection.profileId);
    };
  }
  return sshSocketFactory(target.connection);
}

function sshSocketFactory(connection: SSHConnection): SocketFactory {
  return async () => {
    const { openSSHAppServer } = await import("@/native/sshTransport");
    const endpoint = await openSSHAppServer(connection);
    return new WebSocket(endpoint.url) as unknown as AppServerSocket;
  };
}
