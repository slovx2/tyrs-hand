import type { Message } from "@/types/protocol";

export function mergeMessages(current: Message[], incoming: Message[]): Message[] {
  const messages = new Map(current.map((message) => [message.id, message]));
  for (const message of incoming) messages.set(message.id, message);
  return [...messages.values()].sort((left, right) => left.seq - right.seq);
}

function agentFingerprint(message: Message): string | null {
  if (message.role !== "agent") return null;
  return JSON.stringify([message.content, message.attachments.map((attachment) => attachment.id)]);
}

export function deduplicateConsecutiveAgentMessages(messages: Message[]): Message[] {
  const result: Message[] = [];
  for (const message of messages) {
    const previous = result.at(-1);
    const fingerprint = agentFingerprint(message);
    if (previous && fingerprint !== null && fingerprint === agentFingerprint(previous)) {
      result[result.length - 1] = message;
    } else {
      result.push(message);
    }
  }
  return result;
}
