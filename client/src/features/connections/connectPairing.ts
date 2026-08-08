import * as Device from "expo-device";

import { claimPairing, parsePairingCode, waitForPairing } from "@/api/pairing";
import { saveControlConnection } from "@/db/connections";

export async function connectPairingUri(value: string): Promise<string> {
  const code = parsePairingCode(value);
  const deviceName = Device.deviceName ?? "Tyrs Hand 移动设备";
  const claim = await claimPairing(code, deviceName);
  await waitForPairing(code, claim.claimToken);
  return saveControlConnection({
    serverId: code.serverId,
    baseUrl: code.server,
    name: new URL(code.server).host,
    deviceId: claim.deviceId,
  }, claim.credential);
}
