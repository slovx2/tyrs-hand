import type { MobileThreadItem, MobileTurn } from "@/app-server/types";

type AgentMessage = Extract<MobileThreadItem, { type: "agentMessage" }>;

/** 缺少 phase 的模型仍会输出回答；只对完成态末尾的非空助手正文回退。 */
export function visibleTurnAnswers(turn: MobileTurn): AgentMessage[] {
  const explicit = turn.items.filter((item): item is AgentMessage =>
    item.type === "agentMessage" && item.phase === "final_answer");
  if (explicit.length > 0) return explicit;
  const last = turn.items.at(-1);
  return turn.status === "completed" && last?.type === "agentMessage" &&
    last.phase === null && last.text.trim().length > 0 ? [last] : [];
}
