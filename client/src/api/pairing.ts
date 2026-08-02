import * as Crypto from "expo-crypto";
import { Platform } from "react-native";
import { z } from "zod";

const pairingSchema = z.object({
  server: z.string().url(),
  serverId: z.string().uuid(),
  pairingId: z.string().uuid(),
  secret: z.string().min(16),
  expiresAt: z.string().datetime(),
});

export type PairingCode = z.infer<typeof pairingSchema>;

export function parsePairingCode(value: string): PairingCode {
  const url = new URL(value);
  if (url.protocol !== "tyrshand:" || url.hostname !== "device-pair" || url.searchParams.get("v") !== "2") {
    throw new Error("这不是 Tyrs Hand v2 设备二维码");
  }
  return pairingSchema.parse(Object.fromEntries(url.searchParams.entries()));
}

export async function claimPairing(code: PairingCode, name: string) {
  const deviceId = Crypto.randomUUID();
  const secret = Crypto.getRandomBytes(32).reduce((value, byte) => value + byte.toString(16).padStart(2, "0"), "");
  const credential = `tdv1.${deviceId}.${secret}`;
  const credentialHash = await Crypto.digestStringAsync(Crypto.CryptoDigestAlgorithm.SHA256, credential);
  const response = await fetch(`${code.server.replace(/\/$/, "")}/api/v1/client/device-pairings/${code.pairingId}/claim`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ pairingSecret: code.secret, deviceId, name,
      platform: Platform.OS, credentialHash }),
  });
  if (!response.ok) throw new Error("二维码已过期或无法绑定");
  const result = z.object({ claimToken: z.string(), status: z.literal("waiting_confirmation") })
    .parse(await response.json());
  return { ...result, deviceId, credential };
}

export async function waitForPairing(code: PairingCode, claimToken: string): Promise<void> {
  for (;;) {
    const response = await fetch(`${code.server.replace(/\/$/, "")}/api/v1/client/device-pairings/${code.pairingId}/status`,
      { headers: { Authorization: `Pairing ${claimToken}` } });
    if (!response.ok) throw new Error("读取绑定状态失败");
    const state = z.object({ status: z.string() }).parse(await response.json());
    if (state.status === "approved") return;
    if (state.status === "rejected" || state.status === "expired") throw new Error("设备绑定未获批准");
    await new Promise((resolve) => setTimeout(resolve, 1500));
  }
}
