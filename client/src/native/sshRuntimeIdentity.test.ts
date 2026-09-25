import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SSHConnection } from "@/db/connections";
import { openSSHAppServer } from "./sshTransport";

const native = vi.hoisted(() => ({ openAppServer: vi.fn(), close: vi.fn() }));
const bind = vi.hoisted(() => vi.fn());
vi.mock("expo-modules-core", () => ({ requireNativeModule: () => native }));
vi.mock("@/db/connections", () => ({ bindRuntimeIdentity: bind,
  getSSHCredentials: async () => ({ privateKey: "virtual-key", passphrase: null }) }));
vi.mock("@/preview/config", () => ({ isPreviewMode: false, isPreviewServerId: () => false }));

const connection: SSHConnection = { kind: "ssh", profileId: "claude-profile", engine: "claude-code",
  workerId: "worker-1", name: "Claude", active: true, machineFingerprint: "fingerprint", controls: [],
  host: "localhost", port: 3333, user: "worker", keyRef: "key", hostFingerprint: "fingerprint" };
const endpoint = { url: "ws://127.0.0.1:1234/token", token: "virtual-token",
  runtime: { workerId: "worker-1", engine: "claude-code", protocolVersion: "0.147.0",
    status: "running", capabilities: [], releaseReady: false } };

beforeEach(() => {
  vi.resetAllMocks();
  native.openAppServer.mockResolvedValue(endpoint);
  native.close.mockResolvedValue(undefined);
  bind.mockResolvedValue(undefined);
});

describe("ISOLATION：手机 SSH 入口身份校验", () => {
  it("只有已保存的 Worker 和引擎匹配才交付 WebSocket", async () => {
    await expect(openSSHAppServer(connection)).resolves.toMatchObject(endpoint);
    expect(bind).toHaveBeenCalledWith("claude-profile", endpoint.runtime);
    expect(native.close).not.toHaveBeenCalled();
  });

  it.each([{ engine: "codex" }, { engine: "unknown" }, { workerId: "worker-2" },
    { protocolVersion: "0.146.0" }, { status: "unavailable" }])("身份不符时关闭隧道：%j", async (change) => {
    native.openAppServer.mockResolvedValue({ ...endpoint, runtime: { ...endpoint.runtime, ...change } });
    await expect(openSSHAppServer(connection)).rejects.toThrow();
    expect(native.close).toHaveBeenCalledWith("claude-profile");
    expect(bind).not.toHaveBeenCalled();
  });

  it("Control 关联校验失败也必须关闭已经建立的隧道", async () => {
    bind.mockRejectedValue(new Error("Control 运行时不一致"));
    await expect(openSSHAppServer(connection)).rejects.toThrow("不一致");
    expect(native.close).toHaveBeenCalledWith("claude-profile");
  });
});
