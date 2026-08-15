import { beforeEach, describe, expect, it, vi } from "vitest";

import { removeControlMachineLink, saveControlMachineLink, saveSSHConnection } from "./connections";

const database = vi.hoisted(() => ({
  getFirstAsync: vi.fn(),
  runAsync: vi.fn(),
}));
const secureStore = vi.hoisted(() => ({
  setItemAsync: vi.fn(),
  deleteItemAsync: vi.fn(),
}));

vi.mock("expo-crypto", () => ({ randomUUID: () => "generated-profile" }));
vi.mock("expo-secure-store", () => ({
  WHEN_UNLOCKED_THIS_DEVICE_ONLY: 1,
  ...secureStore,
}));
vi.mock("@/preview/config", () => ({
  isPreviewMode: false,
  isPreviewServerId: () => false,
}));
vi.mock("./database", () => ({
  withDatabaseTransaction: async (callback: (value: typeof database) => Promise<void>) =>
    callback(database),
  runDatabaseWrite: async (callback: (value: typeof database) => Promise<unknown>) =>
    callback(database),
}));

const input = {
  kind: "ssh" as const,
  profileId: "profile-1",
  name: "Worker",
  host: "192.0.2.10",
  port: 2222,
  user: "developer",
  keyRef: "key-1",
  hostFingerprint: "SHA256:test",
  privateKey: "private-key",
  passphrase: "secret",
};

describe("SSH connection persistence", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    database.getFirstAsync.mockResolvedValueOnce(null).mockResolvedValueOnce({ count: 1 });
    database.runAsync.mockResolvedValue(undefined);
    secureStore.setItemAsync.mockResolvedValue(undefined);
    secureStore.deleteItemAsync.mockResolvedValue(undefined);
  });

  it("keeps SQL placeholders aligned with persisted values", async () => {
    await saveSSHConnection(input);

    const [statement, ...parameters] = database.runAsync.mock.calls[0] as [string, ...unknown[]];
    expect(statement.match(/\?/g)).toHaveLength(parameters.length);
    expect(parameters).toHaveLength(11);
    expect(parameters.slice(0, 2)).toEqual(["profile-1", "Worker"]);
  });

  it("removes device-only credentials when the database transaction fails", async () => {
    database.getFirstAsync.mockReset().mockResolvedValueOnce(null).mockResolvedValueOnce({ count: 1 });
    database.runAsync.mockRejectedValueOnce(new Error("insert failed"));

    await expect(saveSSHConnection(input)).rejects.toThrow("insert failed");
    expect(secureStore.deleteItemAsync).toHaveBeenCalledWith(
      "tyrs-hand.ssh-private-key.key-1",
    );
    expect(secureStore.deleteItemAsync).toHaveBeenCalledWith(
      "tyrs-hand.ssh-passphrase.key-1",
    );
  });

  it("扫码先发生时把 SSH 补到相同 Host Key 的机器 profile", async () => {
    database.getFirstAsync.mockReset().mockResolvedValueOnce({ profile_id: "paired-profile" });
    const profileId = await saveControlMachineLink({ profileId: "new-profile", name: "Worker",
      machineFingerprint: "SHA256:test", serverId: "server-1", baseUrl: "https://control.test",
      workerId: "worker-1", workerName: "Worker", deviceId: "device-1" });
    expect(profileId).toBe("paired-profile");
    expect(database.runAsync).toHaveBeenCalledTimes(2);

    vi.clearAllMocks();
    database.getFirstAsync.mockResolvedValueOnce({ profile_id: "paired-profile", ssh_key_ref: null });
    database.runAsync.mockResolvedValue(undefined);
    secureStore.setItemAsync.mockResolvedValue(undefined);
    expect(await saveSSHConnection(input)).toBe("paired-profile");
    expect(database.runAsync.mock.calls[0]?.[0]).toContain("UPDATE connection_profiles");
  });

  it("SSH 先发生时扫码复用相同指纹，不创建第二张机器卡", async () => {
    expect(await saveSSHConnection(input)).toBe("profile-1");
    vi.clearAllMocks();
    database.getFirstAsync.mockResolvedValueOnce({ profile_id: "profile-1" });
    database.runAsync.mockResolvedValue(undefined);
    expect(await saveControlMachineLink({ profileId: "scan-profile", name: "Worker",
      machineFingerprint: "SHA256:test", serverId: "server-1", baseUrl: "https://control.test",
      workerId: "worker-1", workerName: "Worker", deviceId: "device-1" })).toBe("profile-1");
    expect(database.runAsync.mock.calls.some((call) =>
      String(call[0]).includes("INSERT INTO connection_profiles"))).toBe(false);
  });

  it("不同 Host Key 保持为不同机器", async () => {
    database.getFirstAsync.mockReset().mockResolvedValueOnce(null).mockResolvedValueOnce({ count: 1 });
    await saveControlMachineLink({ profileId: "different-profile", name: "Other",
      machineFingerprint: "SHA256:other", serverId: "server-1", baseUrl: "https://control.test",
      workerId: "worker-2", workerName: "Other", deviceId: "device-1" });
    const insert = database.runAsync.mock.calls.find((call) =>
      String(call[0]).includes("INSERT INTO connection_profiles"));
    expect(insert?.[1]).toBe("different-profile");
  });

  it("移除 Control 的最后一台机器时清理设备 Token", async () => {
    database.getFirstAsync.mockReset().mockResolvedValueOnce({ count: 0 });

    await removeControlMachineLink("profile-1", "server-1", "worker-1");

    expect(secureStore.deleteItemAsync).toHaveBeenCalledWith(
      "tyrs-hand.control-device-token.server-1",
    );
  });

  it("同一 Control 仍有关联机器时保留设备 Token", async () => {
    database.getFirstAsync.mockReset().mockResolvedValueOnce({ count: 1 });

    await removeControlMachineLink("profile-1", "server-1", "worker-1");

    expect(secureStore.deleteItemAsync).not.toHaveBeenCalledWith(
      "tyrs-hand.control-device-token.server-1",
    );
  });
});
