import * as Device from "expo-device";

import { claimPairing, parsePairingCode, waitForPairing } from "@/api/pairing";
import { saveConnection } from "@/db/connections";

export async function connectPairingUri(value: string): Promise<string> {
  const code = parsePairingCode(value);
  const deviceName = Device.deviceName ?? "Tyrs Hand 移动设备";
  const claim = await claimPairing(code, deviceName);
  await waitForPairing(code, claim.claimToken);
  await saveConnection({
    serverId: code.serverId,
    baseUrl: code.server,
    name: new URL(code.server).host,
    deviceId: claim.deviceId,
  }, claim.credential);
  return code.serverId;
}
