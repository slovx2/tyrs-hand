import { isPreviewMode, isPreviewServerId } from "@/preview/config";
import type { Session } from "@/types/protocol";
import { getDatabase, runDatabaseWrite } from "./database";
import type { SessionReadState, SessionReadStates } from "./sessionReadStatus";

export type { SessionReadState, SessionReadStates } from "./sessionReadStatus";

export async function loadSessionReadStates(serverId: string): Promise<SessionReadStates> {
  if (isPreviewMode && isPreviewServerId(serverId)) {
    const { previewSessionReadStates } = await import("@/preview/runtime");
    return previewSessionReadStates(serverId);
  }
  const database = await getDatabase();
  const rows = await database.getAllAsync<{
    session_id: string;
    last_read_agent_seq: number;
    last_read_interactive_id: string | null;
  }>(`SELECT session_id,last_read_agent_seq,last_read_interactive_id FROM session_reads
    WHERE server_id=?`, serverId);
  return Object.fromEntries(rows.map((row) => [row.session_id, {
    lastReadAgentSeq: row.last_read_agent_seq,
    lastReadInteractiveId: row.last_read_interactive_id,
  }]));
}

export async function markSessionRead(serverId: string, session: Session): Promise<SessionReadState> {
  const readState = {
    lastReadAgentSeq: session.lastAgentMessageSeq,
    lastReadInteractiveId: session.pendingInteractiveId,
  };
  if (isPreviewMode && isPreviewServerId(serverId)) return readState;
  await runDatabaseWrite(async (database) => {
    await database.runAsync(`INSERT INTO session_reads(server_id,session_id,last_read_agent_seq,
      last_read_interactive_id,initialized,updated_at) VALUES (?,?,?,?,1,?)
      ON CONFLICT(server_id,session_id) DO UPDATE SET
      last_read_agent_seq=max(session_reads.last_read_agent_seq,excluded.last_read_agent_seq),
      last_read_interactive_id=excluded.last_read_interactive_id,
      initialized=1,updated_at=excluded.updated_at`, serverId, session.id,
    readState.lastReadAgentSeq, readState.lastReadInteractiveId, new Date().toISOString());
  });
  return readState;
}
