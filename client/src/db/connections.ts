import * as SecureStore from "expo-secure-store";

import { isPreviewMode, isPreviewServerId } from "@/preview/config";
import { runDatabaseWrite, withDatabaseTransaction } from "./database";

type ConnectionBase = {
  profileId: string;
  name: string;
  active: boolean;
};

export type SSHConnection = ConnectionBase & {
  kind: "ssh";
  host: string;
  port: number;
  user: string;
  keyRef: string;
  hostFingerprint: string | null;
};

export type Connection = SSHConnection;

type ConnectionRow = {
  profile_id: string;
  kind: "ssh";
  name: string;
  active: number;
  control_server_id: string | null;
  control_base_url: string | null;
  control_device_id: string | null;
  ssh_host: string | null;
  ssh_port: number | null;
  ssh_user: string | null;
  ssh_key_ref: string | null;
  ssh_host_fingerprint: string | null;
};

const sshPrivateKeyKey = (keyRef: string) => `tyrs-hand.ssh-private-key.${keyRef}`;
const sshPassphraseKey = (keyRef: string) => `tyrs-hand.ssh-passphrase.${keyRef}`;
const deviceOnly = { keychainAccessible: SecureStore.WHEN_UNLOCKED_THIS_DEVICE_ONLY } as const;

export async function listConnections(): Promise<Connection[]> {
  if (isPreviewMode) {
    const { listPreviewConnections } = await import("@/preview/runtime");
    return listPreviewConnections();
  }
  const { getDatabase } = await import("./database");
  const database = await getDatabase();
  const rows = await database.getAllAsync<ConnectionRow>(
    "SELECT * FROM connection_profiles ORDER BY active DESC,name",
  );
  return rows.map(connectionFromRow);
}

export async function saveSSHConnection(input: Omit<SSHConnection, "active"> & {
  privateKey: string;
  passphrase?: string;
}): Promise<void> {
  await SecureStore.setItemAsync(sshPrivateKeyKey(input.keyRef), input.privateKey, deviceOnly);
  if (input.passphrase) {
    await SecureStore.setItemAsync(sshPassphraseKey(input.keyRef), input.passphrase, deviceOnly);
  }
  try {
    const now = new Date().toISOString();
    await withDatabaseTransaction(async (database) => {
      const count = await database.getFirstAsync<{ count: number }>(
        "SELECT count(*) count FROM connection_profiles",
      );
      await database.runAsync(`INSERT INTO connection_profiles(profile_id,kind,name,active,
        ssh_host,ssh_port,ssh_user,ssh_key_ref,ssh_host_fingerprint,created_at,updated_at)
        VALUES (?,'ssh',?,?,?,?,?,?,?,?,?)`, input.profileId,
      input.name, count?.count === 0 ? 1 : 0, input.host, input.port, input.user, input.keyRef,
      input.hostFingerprint, now, now);
    });
  } catch (error) {
    await SecureStore.deleteItemAsync(sshPrivateKeyKey(input.keyRef));
    await SecureStore.deleteItemAsync(sshPassphraseKey(input.keyRef));
    throw error;
  }
}

export async function getSSHCredentials(connection: SSHConnection): Promise<{
  privateKey: string;
  passphrase: string | null;
}> {
  const privateKey = await SecureStore.getItemAsync(sshPrivateKeyKey(connection.keyRef));
  if (!privateKey) throw new Error("SSH 私钥不存在，请重新导入");
  return { privateKey, passphrase: await SecureStore.getItemAsync(sshPassphraseKey(connection.keyRef)) };
}

export async function setActiveConnection(profileId: string): Promise<void> {
  if (isPreviewMode && isPreviewServerId(profileId)) {
    const { setPreviewActiveConnection } = await import("@/preview/runtime");
    setPreviewActiveConnection(profileId);
    return;
  }
  await withDatabaseTransaction(async (database) => {
    await database.runAsync("UPDATE connection_profiles SET active=0");
    await database.runAsync("UPDATE connection_profiles SET active=1,updated_at=? WHERE profile_id=?",
      new Date().toISOString(), profileId);
  });
}

export async function updateSSHHostFingerprint(profileId: string, fingerprint: string): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(`UPDATE connection_profiles
    SET ssh_host_fingerprint=?,updated_at=? WHERE profile_id=? AND kind='ssh'`, fingerprint,
  new Date().toISOString(), profileId));
}

export async function removeConnection(profileId: string): Promise<void> {
  if (isPreviewMode && isPreviewServerId(profileId)) {
    const { removePreviewConnection } = await import("@/preview/runtime");
    removePreviewConnection(profileId);
    return;
  }
  const connections = await listConnections();
  const connection = connections.find((item) => item.profileId === profileId);
  await runDatabaseWrite((database) => database.runAsync(
    "DELETE FROM connection_profiles WHERE profile_id=?", profileId));
  if (connection) {
    await SecureStore.deleteItemAsync(sshPrivateKeyKey(connection.keyRef));
    await SecureStore.deleteItemAsync(sshPassphraseKey(connection.keyRef));
  }
}

export async function renameConnection(profileId: string, name: string): Promise<void> {
  if (isPreviewMode && isPreviewServerId(profileId)) {
    const { renamePreviewConnection } = await import("@/preview/runtime");
    renamePreviewConnection(profileId, name);
    return;
  }
  await runDatabaseWrite((database) => database.runAsync(
    "UPDATE connection_profiles SET name=?,updated_at=? WHERE profile_id=?",
    name.trim(), new Date().toISOString(), profileId));
}

function connectionFromRow(row: ConnectionRow): Connection {
  const base = { profileId: row.profile_id, name: row.name, active: row.active === 1 };
  if (row.kind === "ssh" && row.ssh_host && row.ssh_port && row.ssh_user && row.ssh_key_ref) {
    return { ...base, kind: "ssh", host: row.ssh_host, port: row.ssh_port, user: row.ssh_user,
      keyRef: row.ssh_key_ref, hostFingerprint: row.ssh_host_fingerprint };
  }
  throw new Error(`连接 ${row.profile_id} 的持久化数据不完整`);
}
