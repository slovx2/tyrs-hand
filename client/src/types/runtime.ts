import { z } from "zod";

export const engineSchema = z.enum(["codex", "claude-code"]);
export type Engine = z.infer<typeof engineSchema>;

export const runtimeInfoSchema = z.object({
  workerId: z.string().min(1),
  engine: engineSchema,
  protocolVersion: z.literal("0.147.0"),
  status: z.enum(["running", "unavailable", "stopped"]),
  capabilities: z.array(z.string()).nullable(),
  releaseReady: z.boolean(),
});
export type RuntimeInfo = z.infer<typeof runtimeInfoSchema>;

export function engineName(engine: Engine): string {
  return engine === "codex" ? "Codex" : "Claude";
}
