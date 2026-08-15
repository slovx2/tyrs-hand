import * as SecureStore from "expo-secure-store";

import { isPreviewMode, isPreviewServerId } from "@/preview/config";
import { runDatabaseWrite, withDatabaseTransaction } from "./database";

export type ControlMachineLink = {
  serverId: string;
  baseUrl: string;
  workerId: string;
  workerName: string;
  deviceId: string;
};

type ConnectionBase = {
  profileId: string;
  name: string;
  active: boolean;
  machineFingerprint: string;
  controls: ControlMachineLink[];
};

export type SSHConnection = ConnectionBase & {
  kind: "ssh";
  host: string;
  port: number;
  user: string;
  keyRef: string;
  hostFingerprint: string | null;
};

export type ControlOnlyConnection = ConnectionBase & { kind: "control" };
export type Connection = SSHConnection | ControlOnlyConnection;

type ConnectionRow = {
  profile_id: string;
  name: string;
  active: number;
  machine_fingerprint: string;
  ssh_host: string | null;
  ssh_port: number | null;
  ssh_user: string | null;
  ssh_key_ref: string | null;
  ssh_host_fingerprint: string | null;
};

type ControlLinkRow = {
  profile_id: string;
  server_id: string;
  base_url: string;
  worker_id: string;
  worker_name: string;
  device_id: string;
};

export type SaveSSHConnectionInput = {
  kind: "ssh";
  profileId: string;
  name: string;
  host: string;
  port: number;
  user: string;
  keyRef: string;
  hostFingerprint: string;
  privateKey: string;
  passphrase?: string;
};

export type SaveControlMachineInput = {
  profileId: string;
  name: string;
  machineFingerprint: string;
  serverId: string;
  baseUrl: string;
  workerId: string;
  workerName: string;
  deviceId: string;
};

const sshPrivateKeyKey = (keyRef: string) => `tyrs-hand.ssh-private-key.${keyRef}`;
const sshPassphraseKey = (keyRef: string) => `tyrs-hand.ssh-passphrase.${keyRef}`;
const controlDeviceTokenKey = (serverId: string) => `tyrs-hand.control-device-token.${serverId}`;
const pairingClaimTokenKey = (pairingId: string) => `tyrs-hand.pairing-claim.${pairingId}`;
const deviceOnly = { keychainAccessible: SecureStore.WHEN_UNLOCKED_THIS_DEVICE_ONLY } as const;

export async function listConnections(): Promise<Connection[]> {
  if (isPreviewMode) {
    const { listPreviewConnections } = await import("@/preview/runtime");
    return listPreviewConnections();
  }
  const { getDatabase } = await import("./database");
  const database = await getDatabase();
  const [rows, linkRows] = await Promise.all([
    database.getAllAsync<ConnectionRow>("SELECT * FROM connection_profiles ORDER BY active DESC,name"),
    database.getAllAsync<ControlLinkRow>("SELECT * FROM control_machine_links ORDER BY worker_name"),
  ]);
  const links = new Map<string, ControlMachineLink[]>();
  for (const row of linkRows) {
    const values = links.get(row.profile_id) ?? [];
    values.push({ serverId: row.server_id, baseUrl: row.base_url, workerId: row.worker_id,
      workerName: row.worker_name, deviceId: row.device_id });
    links.set(row.profile_id, values);
  }
  return rows.map((row) => connectionFromRow(row, links.get(row.profile_id) ?? []));
}

export async function saveSSHConnection(input: SaveSSHConnectionInput): Promise<string> {
  await SecureStore.setItemAsync(sshPrivateKeyKey(input.keyRef), input.privateKey, deviceOnly);
  if (input.passphrase) {
    await SecureStore.setItemAsync(sshPassphraseKey(input.keyRef), input.passphrase, deviceOnly);
  }
  let savedProfileId = input.profileId;
  let replacedKeyRef: string | null = null;
  try {
    const now = new Date().toISOString();
    await withDatabaseTransaction(async (database) => {
      const existing = await database.getFirstAsync<{
        profile_id: string;
        ssh_key_ref: string | null;
      }>("SELECT profile_id,ssh_key_ref FROM connection_profiles WHERE machine_fingerprint=?",
        input.hostFingerprint);
      if (existing) {
        savedProfileId = existing.profile_id;
        replacedKeyRef = existing.ssh_key_ref;
        await database.runAsync(`UPDATE connection_profiles SET name=?,ssh_host=?,ssh_port=?,
          ssh_user=?,ssh_key_ref=?,ssh_host_fingerprint=?,updated_at=? WHERE profile_id=?`,
        input.name, input.host, input.port, input.user, input.keyRef, input.hostFingerprint,
        now, savedProfileId);
        return;
      }
      const count = await database.getFirstAsync<{ count: number }>(
        "SELECT count(*) count FROM connection_profiles");
      await database.runAsync(`INSERT INTO connection_profiles(profile_id,kind,name,active,
        machine_fingerprint,ssh_host,ssh_port,ssh_user,ssh_key_ref,ssh_host_fingerprint,
        created_at,updated_at) VALUES (?,'machine',?,?,?,?,?,?,?,?,?,?)`, input.profileId,
      input.name, count?.count === 0 ? 1 : 0, input.hostFingerprint, input.host, input.port,
      input.user, input.keyRef, input.hostFingerprint, now, now);
    });
    if (replacedKeyRef && replacedKeyRef !== input.keyRef) {
      await SecureStore.deleteItemAsync(sshPrivateKeyKey(replacedKeyRef));
      await SecureStore.deleteItemAsync(sshPassphraseKey(replacedKeyRef));
    }
    return savedProfileId;
  } catch (error) {
    await SecureStore.deleteItemAsync(sshPrivateKeyKey(input.keyRef));
    await SecureStore.deleteItemAsync(sshPassphraseKey(input.keyRef));
    throw error;
  }
}

export async function saveControlMachineLink(input: SaveControlMachineInput): Promise<string> {
  let savedProfileId = input.profileId;
  const now = new Date().toISOString();
  await withDatabaseTransaction(async (database) => {
    const existing = await database.getFirstAsync<{ profile_id: string }>(
      "SELECT profile_id FROM connection_profiles WHERE machine_fingerprint=?",
      input.machineFingerprint);
    if (existing) {
      savedProfileId = existing.profile_id;
    } else {
      const count = await database.getFirstAsync<{ count: number }>(
        "SELECT count(*) count FROM connection_profiles");
      await database.runAsync(`INSERT INTO connection_profiles(profile_id,kind,name,active,
        machine_fingerprint,created_at,updated_at) VALUES (?,'machine',?,?,?,?,?)`,
      input.profileId, input.name, count?.count === 0 ? 1 : 0, input.machineFingerprint, now, now);
    }
    await database.runAsync("DELETE FROM control_machine_links WHERE profile_id=? AND server_id=?",
      savedProfileId, input.serverId);
    await database.runAsync(`INSERT INTO control_machine_links(profile_id,server_id,base_url,
      worker_id,worker_name,device_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)
      ON CONFLICT(server_id,worker_id) DO UPDATE SET profile_id=excluded.profile_id,
      base_url=excluded.base_url,worker_name=excluded.worker_name,device_id=excluded.device_id,
      updated_at=excluded.updated_at`, savedProfileId, input.serverId,
    input.baseUrl.replace(/\/$/, ""), input.workerId, input.workerName, input.deviceId, now, now);
  });
  return savedProfileId;
}

export async function getSSHCredentials(connection: SSHConnection): Promise<{
  privateKey: string;
  passphrase: string | null;
}> {
  const privateKey = await SecureStore.getItemAsync(sshPrivateKeyKey(connection.keyRef));
  if (!privateKey) throw new Error("SSH 私钥不存在，请重新导入");
  return { privateKey, passphrase: await SecureStore.getItemAsync(sshPassphraseKey(connection.keyRef)) };
}

export async function getControlDeviceToken(serverId: string): Promise<string | null> {
  return SecureStore.getItemAsync(controlDeviceTokenKey(serverId));
}

export async function saveControlDeviceToken(serverId: string, token: string): Promise<void> {
  await SecureStore.setItemAsync(controlDeviceTokenKey(serverId), token, deviceOnly);
}

export async function deleteControlDeviceToken(serverId: string): Promise<void> {
  await SecureStore.deleteItemAsync(controlDeviceTokenKey(serverId));
}

export async function savePairingClaimToken(pairingId: string, token: string): Promise<void> {
  await SecureStore.setItemAsync(pairingClaimTokenKey(pairingId), token, deviceOnly);
}

export async function getPairingClaimToken(pairingId: string): Promise<string | null> {
  return SecureStore.getItemAsync(pairingClaimTokenKey(pairingId));
}

export async function deletePairingClaimToken(pairingId: string): Promise<void> {
  await SecureStore.deleteItemAsync(pairingClaimTokenKey(pairingId));
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
  await withDatabaseTransaction(async (database) => {
    const target = await database.getFirstAsync<{ profile_id: string }>(
      "SELECT profile_id FROM connection_profiles WHERE machine_fingerprint=?", fingerprint);
    if (!target || target.profile_id === profileId) {
      await database.runAsync(`UPDATE connection_profiles SET machine_fingerprint=?,
        ssh_host_fingerprint=?,updated_at=? WHERE profile_id=?`, fingerprint, fingerprint,
      new Date().toISOString(), profileId);
      return;
    }
    // 旧版未验证 SSH profile 与扫码先创建的 profile 相遇时，保留 SSH profile ID 及其缓存。
    await database.runAsync(`UPDATE control_machine_links SET profile_id=?
      WHERE profile_id=?`, profileId, target.profile_id);
    await database.runAsync("DELETE FROM connection_profiles WHERE profile_id=?", target.profile_id);
    await database.runAsync(`UPDATE connection_profiles SET machine_fingerprint=?,
      ssh_host_fingerprint=?,updated_at=? WHERE profile_id=?`, fingerprint, fingerprint,
    new Date().toISOString(), profileId);
  });
}

export async function removeControlMachineLink(profileId: string, serverId: string,
  workerId: string): Promise<void> {
  let remaining = 0;
  await withDatabaseTransaction(async (database) => {
    await database.runAsync(`DELETE FROM control_machine_links
      WHERE profile_id=? AND server_id=? AND worker_id=?`, profileId, serverId, workerId);
    remaining = (await database.getFirstAsync<{ count: number }>(
      "SELECT count(*) count FROM control_machine_links WHERE server_id=?", serverId))?.count ?? 0;
  });
  if (remaining === 0) await SecureStore.deleteItemAsync(controlDeviceTokenKey(serverId));
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
  if (connection?.kind === "ssh") {
    await SecureStore.deleteItemAsync(sshPrivateKeyKey(connection.keyRef));
    await SecureStore.deleteItemAsync(sshPassphraseKey(connection.keyRef));
  }
  for (const serverId of new Set(connection?.controls.map((item) => item.serverId) ?? [])) {
    const { getDatabase } = await import("./database");
    const database = await getDatabase();
    const count = await database.getFirstAsync<{ count: number }>(
      "SELECT count(*) count FROM control_machine_links WHERE server_id=?", serverId);
    if (!count?.count) await SecureStore.deleteItemAsync(controlDeviceTokenKey(serverId));
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

function connectionFromRow(row: ConnectionRow, controls: ControlMachineLink[]): Connection {
  const base = { profileId: row.profile_id, name: row.name, active: row.active === 1,
    machineFingerprint: row.machine_fingerprint, controls };
  if (row.ssh_host && row.ssh_port && row.ssh_user && row.ssh_key_ref) {
    return { ...base, kind: "ssh", host: row.ssh_host, port: row.ssh_port, user: row.ssh_user,
      keyRef: row.ssh_key_ref, hostFingerprint: row.ssh_host_fingerprint };
  }
  return { ...base, kind: "control" };
}
