import { extractExpoPathFromURL } from "expo-router/build/fork/extractPathFromURL";
import { describe, expect, it } from "vitest";
import { redirectPairingSystemPath } from "./pairingSystemPath";

describe("配对链接经过实际 Expo 原生路由", () => {
  it.each([true, false])("冷启动或连续扫码保留编码参数：initial=%s", (initial) => {
    const fingerprint = "SHA256:Nlq3qkjF+MNxRfTi3gSi6tGhgmSF+Lc2WKy/dNqfFbY";
    const query = new URLSearchParams({ v: "4", engine: "claude-code",
      sshHostKeyFingerprint: fingerprint, workerName: "Worker + A&B 100%" });
    const uri = `tyrshand://device-pair?${query}`;
    expect(() => extractExpoPathFromURL([], uri)).toThrow(URIError);
    const previous = extractExpoPathFromURL([], `tyrshand://device-pair?${
      new URLSearchParams({ sshHostKeyFingerprint: fingerprint })}`);
    expect(new URL(previous, "https://route.test").searchParams.get("sshHostKeyFingerprint"))
      .not.toBe(fingerprint);
    const path = extractExpoPathFromURL([], redirectPairingSystemPath({ path: uri, initial }));
    const params = new URL(path, "https://route.test").searchParams;
    expect(params.get("sshHostKeyFingerprint")).toBe(fingerprint);
    expect(params.get("workerName")).toBe("Worker + A&B 100%");
    expect(params.get("engine")).toBe("claude-code");
  });

  it("其他系统链接保持原样", () => {
    for (const path of ["/session/abc", "tyrshand://automations", "untrusted://device-pair?v=4"]) {
      expect(redirectPairingSystemPath({ path, initial: false })).toBe(path);
    }
  });
});
