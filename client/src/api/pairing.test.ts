import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { claimPairing, fetchPairedMachine, parsePairingCode, resolvePairingUri,
  waitForPairing } from "./pairing";

const storage = vi.hoisted(() => ({
  getControlDeviceToken: vi.fn(),
  getPairingClaimToken: vi.fn(),
  saveControlDeviceToken: vi.fn(),
  savePairingClaimToken: vi.fn(),
  deletePairingClaimToken: vi.fn(),
}));

vi.mock("@/db/connections", () => storage);
vi.mock("expo-crypto", () => ({
  randomUUID: () => "33333333-3333-4333-8333-333333333333",
  getRandomBytes: () => new Uint8Array(32).fill(1),
  CryptoDigestAlgorithm: { SHA256: "SHA-256" },
  digestStringAsync: () => Promise.resolve("credential-hash"),
}));

const value = "tyrshand://device-pair?v=3&server=https%3A%2F%2Fcontrol.test" +
  "&serverId=11111111-1111-4111-8111-111111111111" +
  "&pairingId=22222222-2222-4222-8222-222222222222&secret=pairing-secret-value" +
  "&workerId=44444444-4444-4444-8444-444444444444&workerName=Worker" +
  "&sshHostKeyFingerprint=SHA256%3AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
  "&expiresAt=2026-08-15T02%3A00%3A00Z";

describe("定时任务扫码协议", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    storage.getControlDeviceToken.mockResolvedValue(null);
    storage.getPairingClaimToken.mockResolvedValue(null);
    storage.saveControlDeviceToken.mockResolvedValue(undefined);
    storage.savePairingClaimToken.mockResolvedValue(undefined);
    storage.deletePairingClaimToken.mockResolvedValue(undefined);
  });

  afterEach(() => vi.unstubAllGlobals());

  it("只接受带 Worker Host Key 的 v3 二维码", () => {
    const parsed = parsePairingCode(value);
    expect(parsed.workerId).toBe("44444444-4444-4444-8444-444444444444");
    expect(parsed.sshHostKeyFingerprint).toBe(
      "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA");
    expect(() => parsePairingCode(value.replace("v=3", "v=2"))).toThrow(
      "无法识别这个定时任务授权二维码");
  });

  it("连续扫码时优先使用当前路由参数，不重放首次启动二维码", () => {
    const secondID = "55555555-5555-4555-8555-555555555555";
    const second = value.replace("22222222-2222-4222-8222-222222222222", secondID);
    const params = Object.fromEntries(new URL(second).searchParams.entries());

    expect(resolvePairingUri(params, value)).toContain(`pairingId=${secondID}`);
  });

  it("把设备凭证和临时 claim token 写入 SecureStore", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      claimToken: "claim-token-with-enough-length", status: "waiting_confirmation",
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const result = await claimPairing(parsePairingCode(value), "Pixel");
    expect(result.deviceId).toBe("33333333-3333-4333-8333-333333333333");
    expect(storage.saveControlDeviceToken).toHaveBeenCalledTimes(1);
    expect(storage.savePairingClaimToken).toHaveBeenCalledWith(
      "22222222-2222-4222-8222-222222222222", "claim-token-with-enough-length");
  });

  it("拒绝 Control 返回的不同机器指纹", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ items: [{
      workerId: "44444444-4444-4444-8444-444444444444", name: "Worker",
      sshHostKeyFingerprint: "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
      status: "online",
    }] }), { status: 200, headers: { "Content-Type": "application/json" } })));
    await expect(fetchPairedMachine(parsePairingCode(value), "credential")).rejects.toThrow(
      "Control 返回的机器身份与二维码不一致");
  });

  it.each([
    ["rejected", "管理员拒绝了授权"],
    ["expired", "二维码已过期"],
  ])("清理 %s 配对的临时 claim token", async (status, message) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ status }), {
      status: 200, headers: { "Content-Type": "application/json" },
    })));

    await expect(waitForPairing(parsePairingCode(value), "claim-token"))
      .rejects.toThrow(message);
    expect(storage.deletePairingClaimToken).toHaveBeenCalledWith(
      "22222222-2222-4222-8222-222222222222",
    );
  });

  it("App 中断后复用 SecureStore 中的 claim token，不重复 claim", async () => {
    storage.getPairingClaimToken.mockResolvedValue("saved-claim-token");
    storage.getControlDeviceToken.mockResolvedValue(
      "tdv1.33333333-3333-4333-8333-333333333333.saved-device-secret",
    );
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    const result = await claimPairing(parsePairingCode(value), "Pixel");

    expect(result.claimToken).toBe("saved-claim-token");
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
