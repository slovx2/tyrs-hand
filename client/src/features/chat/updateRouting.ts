export function conversationTurnIdFromPayload(payload: unknown): string | null {
  if (!payload || typeof payload !== "object") return null;
  const root = payload as Record<string, unknown>;
  const message = root.message && typeof root.message === "object" ?
    root.message as Record<string, unknown> : root;
  return typeof message.conversationTurnId === "string" && message.conversationTurnId ?
    message.conversationTurnId : null;
}
