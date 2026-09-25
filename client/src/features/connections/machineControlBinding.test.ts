import { describe, expect, it } from "vitest";

import type { Connection } from "@/db/connections";
import { resolveMachineControlBinding } from "./machineControlBinding";

const control = (workerId: string, serverId = "server") => ({
  engine: "codex" as const, serverId, baseUrl: "https://control.example", workerId,
  workerName: `Worker ${workerId}`, deviceId: "device",
});

describe("机器与 Control Worker 绑定", () => {
  it("从当前 SSH 机器唯一推导 Worker，不产生独立选择", () => {
    const connection = sshConnection([control("worker-1")]);
    expect(resolveMachineControlBinding(connection)).toMatchObject({
      status: "bound", workerId: "worker-1", link: control("worker-1"),
    });
  });

  it("同一 Worker 的多个 Control 授权仍视为一个绑定", () => {
    const result = resolveMachineControlBinding(sshConnection([
      control("worker-1", "server-a"), control("worker-1", "server-b"),
    ]));
    expect(result.status).toBe("bound");
    expect(result.workerId).toBe("worker-1");
  });

  it("没有绑定或绑定了多个 Worker 时不允许猜测", () => {
    expect(resolveMachineControlBinding(sshConnection([])).status).toBe("unbound");
    expect(resolveMachineControlBinding(sshConnection([
      control("worker-1"), control("worker-2"),
    ])).status).toBe("ambiguous");
  });
});

function sshConnection(controls: Connection["controls"]): Connection {
  return { kind: "ssh", engine: "codex", workerId: null, profileId: "machine", name: "machine", active: true,
    machineFingerprint: "fingerprint", controls, host: "localhost", port: 22,
    user: "tester", keyRef: "key", hostFingerprint: null };
}
