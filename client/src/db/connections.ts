import * as SecureStore from "expo-secure-store";

import { isPreviewMode, isPreviewServerId } from "@/preview/config";
import { getDatabase, runDatabaseWrite, withDatabaseTransaction } from "./database";

export type Connection = {
  serverId: string;
  baseUrl: string;
  name: string;
  deviceId: string;
  active: boolean;
};

const tokenKey = (serverId: string) => `tyrs-hand.device-token.${serverId}`;

export async function listConnections(): Promise<Connection[]> {
  if (isPreviewMode) {
    const { listPreviewConnections } = await import("@/preview/runtime");
    return listPreviewConnections();
  }
  const database = await getDatabase();
  const rows = await database.getAllAsync<{
    server_id: string; base_url: string; name: string; device_id: string; active: number;
  }>("SELECT server_id,base_url,name,device_id,active FROM connections ORDER BY active DESC,name");
  return rows.map((row) => ({ serverId: row.server_id, baseUrl: row.base_url,
    name: row.name, deviceId: row.device_id, active: row.active === 1 }));
}

export async function saveConnection(connection: Omit<Connection, "active">, token: string): Promise<void> {
  await SecureStore.setItemAsync(tokenKey(connection.serverId), token,
    { keychainAccessible: SecureStore.WHEN_UNLOCKED_THIS_DEVICE_ONLY });
  const now = new Date().toISOString();
  await withDatabaseTransaction(async (database) => {
    const count = await database.getFirstAsync<{ count: number }>("SELECT count(*) count FROM connections");
    const active = count?.count === 0 ? 1 : 0;
    await database.runAsync(`INSERT INTO connections(server_id,base_url,name,device_id,active,created_at,updated_at)
      VALUES (?,?,?,?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET base_url=excluded.base_url,
      name=excluded.name,device_id=excluded.device_id,updated_at=excluded.updated_at`,
    connection.serverId, connection.baseUrl.replace(/\/$/, ""), connection.name,
    connection.deviceId, active, now, now);
    await database.runAsync("INSERT OR IGNORE INTO sync_state(server_id) VALUES (?)", connection.serverId);
  });
}

export async function getToken(serverId: string): Promise<string | null> {
  if (isPreviewMode && isPreviewServerId(serverId)) return "preview-device-token";
  return SecureStore.getItemAsync(tokenKey(serverId));
}

export async function setActiveConnection(serverId: string): Promise<void> {
  if (isPreviewMode && isPreviewServerId(serverId)) {
    const { setPreviewActiveConnection } = await import("@/preview/runtime");
    setPreviewActiveConnection(serverId);
    return;
  }
  await withDatabaseTransaction(async (database) => {
    await database.runAsync("UPDATE connections SET active=0");
    await database.runAsync("UPDATE connections SET active=1,updated_at=? WHERE server_id=?",
      new Date().toISOString(), serverId);
  });
}

export async function removeConnection(serverId: string): Promise<void> {
  if (isPreviewMode && isPreviewServerId(serverId)) {
    const { removePreviewConnection } = await import("@/preview/runtime");
    removePreviewConnection(serverId);
    return;
  }
  await runDatabaseWrite((database) => database.runAsync(
    "DELETE FROM connections WHERE server_id=?", serverId));
  await SecureStore.deleteItemAsync(tokenKey(serverId));
}

export async function renameConnection(serverId: string, name: string): Promise<void> {
  if (isPreviewMode && isPreviewServerId(serverId)) {
    const { renamePreviewConnection } = await import("@/preview/runtime");
    renamePreviewConnection(serverId, name);
    return;
  }
  await runDatabaseWrite((database) => database.runAsync(
    "UPDATE connections SET name=?,updated_at=? WHERE server_id=?",
    name.trim(), new Date().toISOString(), serverId));
}
