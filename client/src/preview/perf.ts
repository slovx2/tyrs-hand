import { previewPerfLogging } from "./config";

type PerfValue = string | number | boolean | null | undefined;

export function previewPerf(event: string, values: Record<string, PerfValue> = {}): void {
  if (!previewPerfLogging) return;
  const fields = Object.entries(values).map(([key, value]) => `${key}=${String(value)}`).join(" ");
  console.info(`[TYRS_PERF] ${event}${fields ? ` ${fields}` : ""}`);
}

// 交互诊断只记录协议身份，禁止记录问题、表单、权限内容或用户答案。
export function traceInteraction(stage: "received" | "pending" | "mounted",
  request: { id: string | number; method: string; params?: unknown }): void {
  const params = request.params as { threadId?: unknown } | undefined;
  previewPerf(`interactive.${stage}`, { method: request.method, id: request.id,
    idType: typeof request.id, threadId: typeof params?.threadId === "string" ? params.threadId : null });
}
