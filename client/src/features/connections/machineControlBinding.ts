import type { Connection, ControlMachineLink } from "@/db/connections";

export type MachineControlBinding =
  | { status: "unbound"; link: null; workerId: null; message: string }
  | { status: "ambiguous"; link: null; workerId: null; message: string }
  | { status: "bound"; link: ControlMachineLink; workerId: string; message: null };

/**
 * Control 的 Worker 身份由配对时的 SSH 主机指纹确定。
 * 同一台机器在多个 Control 服务上可能有多条授权记录，但不能出现多个 Worker 身份。
 */
export function resolveMachineControlBinding(
  connection: Connection | null,
): MachineControlBinding {
  if (!connection || connection.kind !== "ssh" || connection.controls.length === 0) {
    return { status: "unbound", link: null, workerId: null,
      message: "当前机器尚未关联 Control Worker，请到设置页扫码关联" };
  }
  const workerIds = [...new Set(connection.controls.map((item) => item.workerId))];
  if (workerIds.length !== 1) {
    return { status: "ambiguous", link: null, workerId: null,
      message: "当前机器关联了多个 Worker，请到设置页修复绑定" };
  }
  const link = connection.controls[0]!;
  return { status: "bound", link, workerId: workerIds[0]!, message: null };
}
