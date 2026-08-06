import type { Session } from "@/types/protocol";

export type SessionReadState = {
  lastReadAgentSeq: number;
  lastReadInteractiveId: string | null;
};

export type SessionReadStates = Record<string, SessionReadState>;

export function visibleSessionReadSnapshot(listedSession: Session | undefined,
  detailSession: Session | undefined): Session | undefined {
  return listedSession ?? detailSession;
}

export function sessionHasUnread(session: Session, readState?: SessionReadState): boolean {
  const lastReadAgentSeq = readState?.lastReadAgentSeq ?? 0;
  const lastReadInteractiveId = readState?.lastReadInteractiveId ?? null;
  return session.lastAgentMessageSeq > lastReadAgentSeq ||
    (session.pendingInteractiveId !== null && session.pendingInteractiveId !== lastReadInteractiveId);
}

export function sessionListIndicator(session: Session,
  readState?: SessionReadState): "running" | "issue" | "unread" | null {
  if (session.isRunning) return "running";
  if (session.hasRunIssue) return "issue";
  return sessionHasUnread(session, readState) ? "unread" : null;
}
