import * as Crypto from "expo-crypto";
import { z } from "zod";

import { deletePairingClaimToken, getControlDeviceToken, getPairingClaimToken,
  saveControlDeviceToken, savePairingClaimToken } from "@/db/connections";

import { engineSchema } from "@/types/runtime";

const pairingSchema = z.object({
  server: z.string().url(),
  serverId: z.string().uuid(),
  pairingId: z.string().uuid(),
  secret: z.string().min(16),
  workerId: z.string().uuid(),
  engine: engineSchema,
  workerName: z.string().min(1),
  sshHostKeyFingerprint: z.string().regex(/^SHA256:[A-Za-z0-9+/]{43}$/),
  expiresAt: z.string().datetime(),
});

export type PairingCode = z.infer<typeof pairingSchema>;

export function resolvePairingUri(params: Record<string, string | string[]>,
  initialUrl: string | null): string {
  const query = new URLSearchParams();
  for (const [key, raw] of Object.entries(params)) {
    const value = Array.isArray(raw) ? raw[0] : raw;
    if (value) query.set(key, value);
  }
  if (query.has("pairingId")) return `tyrshand://device-pair?${query.toString()}`;
  if (initialUrl?.startsWith("tyrshand://device-pair")) return initialUrl;
  throw new Error("缺少定时任务授权二维码参数");
}

export function parsePairingCode(value: string): PairingCode {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    // URL 解析器可能把完整输入写入异常，不能向界面回显关联密钥。
    throw new Error("无法识别这个定时任务授权二维码");
  }
  if (url.protocol !== "tyrshand:" || url.hostname !== "device-pair" ||
    url.searchParams.get("v") !== "4") {
    throw new Error("无法识别这个定时任务授权二维码");
  }
  const result = pairingSchema.safeParse(Object.fromEntries(url.searchParams.entries()));
  if (!result.success) throw new Error("关联二维码参数不完整或格式无效");
  return result.data;
}

function deviceIDFromToken(token: string): string | null {
  const parts = token.split(".");
  return parts.length === 3 && parts[0] === "tdv1" ? parts[1] ?? null : null;
}

async function deviceCredential(serverId: string): Promise<{
  deviceId: string;
  credential: string;
}> {
  const existing = await getControlDeviceToken(serverId);
  const existingID = existing ? deviceIDFromToken(existing) : null;
  if (existing && existingID) return { deviceId: existingID, credential: existing };
  const deviceId = Crypto.randomUUID();
  const secret = Crypto.getRandomBytes(32).reduce((value, byte) =>
    value + byte.toString(16).padStart(2, "0"), "");
  const credential = `tdv1.${deviceId}.${secret}`;
  await saveControlDeviceToken(serverId, credential);
  return { deviceId, credential };
}

export async function claimPairing(code: PairingCode, name: string,
  platform = "unknown"): Promise<{
  claimToken: string;
  deviceId: string;
  credential: string;
}> {
  const savedClaim = await getPairingClaimToken(code.pairingId);
  const device = await deviceCredential(code.serverId);
  if (savedClaim) return { claimToken: savedClaim, ...device };
  const credentialHash = await Crypto.digestStringAsync(
    Crypto.CryptoDigestAlgorithm.SHA256, device.credential);
  const response = await fetch(`${code.server.replace(/\/$/, "")}/api/v1/client/device-pairings/${code.pairingId}/claim`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ pairingSecret: code.secret, deviceId: device.deviceId, name,
      platform, credentialHash }),
  });
  if (!response.ok) throw new Error("二维码已过期、已使用或无法授权");
  const result = z.object({ claimToken: z.string().min(16),
    status: z.literal("waiting_confirmation") }).parse(await response.json());
  await savePairingClaimToken(code.pairingId, result.claimToken);
  return { claimToken: result.claimToken, ...device };
}

export async function waitForPairing(code: PairingCode, claimToken: string): Promise<void> {
  for (;;) {
    const response = await fetch(`${code.server.replace(/\/$/, "")}/api/v1/client/device-pairings/${code.pairingId}/status`,
      { headers: { Authorization: `Pairing ${claimToken}` } });
    if (!response.ok) throw new Error("读取授权状态失败");
    const state = z.object({ status: z.string() }).parse(await response.json());
    if (state.status === "approved") {
      await deletePairingClaimToken(code.pairingId);
      return;
    }
    if (state.status === "rejected" || state.status === "expired") {
      await deletePairingClaimToken(code.pairingId);
      throw new Error(state.status === "expired" ? "二维码已过期" : "管理员拒绝了授权");
    }
    await new Promise((resolve) => setTimeout(resolve, 1500));
  }
}

export async function fetchPairedMachine(code: PairingCode, credential: string) {
  const response = await fetch(`${code.server.replace(/\/$/, "")}/api/v1/client/machines`,
    { headers: { Authorization: `Bearer ${credential}`, Accept: "application/json" } });
  if (!response.ok) throw new Error("授权已确认，但读取机器身份失败");
  const result = z.object({ items: z.array(z.object({
    workerId: z.string().uuid(),
  engine: engineSchema, name: z.string(),
    sshHostKeyFingerprint: z.string(), status: z.string(),
  })) }).parse(await response.json());
  const machine = result.items.find((item) => item.workerId === code.workerId && item.engine === code.engine);
  if (!machine || machine.sshHostKeyFingerprint !== code.sshHostKeyFingerprint) {
    throw new Error("Control 返回的机器身份与二维码不一致");
  }
  return machine;
}
