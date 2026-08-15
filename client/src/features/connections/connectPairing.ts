import * as Crypto from "expo-crypto";
import * as Device from "expo-device";
import { Platform } from "react-native";

import { claimPairing, fetchPairedMachine, parsePairingCode,
  waitForPairing } from "@/api/pairing";
import { saveControlMachineLink } from "@/db/connections";

export async function connectPairingUri(value: string): Promise<string> {
  const code = parsePairingCode(value);
  const deviceName = Device.deviceName ?? "Tyrs Hand 移动设备";
  const claim = await claimPairing(code, deviceName, Platform.OS);
  await waitForPairing(code, claim.claimToken);
  const machine = await fetchPairedMachine(code, claim.credential);
  return saveControlMachineLink({
    profileId: Crypto.randomUUID(),
    name: machine.name,
    machineFingerprint: machine.sshHostKeyFingerprint,
    serverId: code.serverId,
    baseUrl: code.server,
    workerId: machine.workerId,
    workerName: machine.name,
    deviceId: claim.deviceId,
  });
}
