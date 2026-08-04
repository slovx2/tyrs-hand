import { previewPerfLogging } from "./config";

type PerfValue = string | number | boolean | null | undefined;

export function previewPerf(event: string, values: Record<string, PerfValue> = {}): void {
  if (!previewPerfLogging) return;
  const fields = Object.entries(values).map(([key, value]) => `${key}=${String(value)}`).join(" ");
  console.info(`[TYRS_PERF] ${event}${fields ? ` ${fields}` : ""}`);
}
