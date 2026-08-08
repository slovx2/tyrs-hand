import { describe, expect, it } from "vitest";

import { normalizeNativeTransportError } from "./nativeError";

describe("normalizeNativeTransportError", () => {
  it("removes Expo and Go bridge wrappers while preserving the actionable cause", () => {
    const error = new Error(`Call to function 'TyrsSSHTransport.openAppServer' has been rejected.
→ Caused by: go.Universe$proxyerror: SSH 握手失败: unable to authenticate`);
    expect(normalizeNativeTransportError(error).message)
      .toBe("SSH 握手失败: unable to authenticate");
  });

  it("keeps ordinary JavaScript failures unchanged", () => {
    expect(normalizeNativeTransportError(new Error("私钥不存在")).message).toBe("私钥不存在");
  });
});
