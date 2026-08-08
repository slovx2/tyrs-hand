import { beforeEach, describe, expect, it, vi } from "vitest";

import { saveSSHConnection, updateSSHRemoteProjectRoot } from "./connections";

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
  remoteProjectRoot: "/workspace",
  privateKey: "private-key",
  passphrase: "secret",
};

describe("SSH connection persistence", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    database.getFirstAsync.mockResolvedValue({ count: 1 });
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

  it("allows saving SSH before a default directory is selected", async () => {
    await saveSSHConnection({ ...input, remoteProjectRoot: "" });

    const [, ...parameters] = database.runAsync.mock.calls[0] as [string, ...unknown[]];
    expect(parameters[8]).toBe("");
  });

  it("stores a separately selected absolute default directory", async () => {
    await updateSSHRemoteProjectRoot("profile-1", "/workspace/project/");

    expect(database.runAsync).toHaveBeenCalledWith(expect.stringContaining(
      "SET ssh_remote_project_root=?"), "/workspace/project", expect.any(String), "profile-1");
    await expect(updateSSHRemoteProjectRoot("profile-1", "relative/path"))
      .rejects.toThrow("默认目录必须是绝对路径");
  });

  it("removes device-only credentials when the database transaction fails", async () => {
    database.runAsync.mockRejectedValueOnce(new Error("insert failed"));

    await expect(saveSSHConnection(input)).rejects.toThrow("insert failed");
    expect(secureStore.deleteItemAsync).toHaveBeenCalledWith(
      "tyrs-hand.ssh-private-key.key-1",
    );
    expect(secureStore.deleteItemAsync).toHaveBeenCalledWith(
      "tyrs-hand.ssh-passphrase.key-1",
    );
  });
});
