import { bootstrapSchema, sessionSchema, type Bootstrap, type Session } from "@/types/protocol";
import { isPreviewMode, isPreviewServerId } from "@/preview/config";
import { getDatabase, runDatabaseWrite, withDatabaseTransaction } from "./database";

export async function loadCachedBootstrap(serverId: string): Promise<Bootstrap | null> {
  if (isPreviewMode && isPreviewServerId(serverId)) {
    const { previewBootstrap } = await import("@/preview/runtime");
    return previewBootstrap(serverId);
  }
  const database = await getDatabase();
  const row = await database.getFirstAsync<{ bootstrap_payload: string | null }>(
    "SELECT bootstrap_payload FROM connections WHERE server_id=?", serverId);
  if (!row?.bootstrap_payload) return null;
  const parsed = bootstrapSchema.safeParse(JSON.parse(row.bootstrap_payload));
  return parsed.success ? parsed.data : null;
}

export async function saveBootstrap(serverId: string, bootstrap: Bootstrap): Promise<void> {
  if (isPreviewMode && isPreviewServerId(serverId)) return;
  const now = new Date().toISOString();
  await withDatabaseTransaction(async (database) => {
    await database.runAsync("UPDATE connections SET bootstrap_payload=?,updated_at=? WHERE server_id=?",
      JSON.stringify(bootstrap), now, serverId);
    for (const project of bootstrap.projects) {
      await database.runAsync(`INSERT INTO projects(server_id,id,workspace_id,name,relative_path,payload,updated_at)
        VALUES (?,?,?,?,?,?,?) ON CONFLICT(server_id,id) DO UPDATE SET workspace_id=excluded.workspace_id,
        name=excluded.name,relative_path=excluded.relative_path,payload=excluded.payload,updated_at=excluded.updated_at`,
      serverId, project.id, project.workspaceId, project.name, project.relativePath,
      JSON.stringify(project), now);
    }
  });
}

export async function loadCachedSessions(serverId: string): Promise<Session[]> {
  if (isPreviewMode && isPreviewServerId(serverId)) {
    const { previewSessions } = await import("@/preview/runtime");
    return previewSessions(serverId);
  }
  const database = await getDatabase();
  const rows = await database.getAllAsync<{ payload: string }>(
    "SELECT payload FROM sessions WHERE server_id=? ORDER BY last_activity_at DESC", serverId);
  return rows.flatMap((row) => {
    const parsed = sessionSchema.safeParse(JSON.parse(row.payload));
    return parsed.success ? [parsed.data] : [];
  });
}

export async function saveSessions(serverId: string, sessions: Session[]): Promise<void> {
  if (isPreviewMode && isPreviewServerId(serverId)) return;
  await withDatabaseTransaction(async (database) => {
    const connection = await database.getFirstAsync<{ session_reads_initialized: number }>(
      "SELECT session_reads_initialized FROM connections WHERE server_id=?", serverId);
    const initializeBaseline = connection?.session_reads_initialized !== 1;
    const now = new Date().toISOString();
    for (const session of sessions) {
      await database.runAsync(`INSERT INTO sessions(server_id,id,project_id,title,lifecycle_state,
        last_message_seq,last_activity_at,payload) VALUES (?,?,?,?,?,?,?,?)
        ON CONFLICT(server_id,id) DO UPDATE SET project_id=excluded.project_id,title=excluded.title,
        lifecycle_state=excluded.lifecycle_state,last_message_seq=excluded.last_message_seq,
        last_activity_at=excluded.last_activity_at,payload=excluded.payload`, serverId,
      session.id, session.projectId, session.title, session.lifecycleState,
      session.lastMessageSeq, session.lastActivityAt, JSON.stringify(session));
      if (initializeBaseline) {
        await database.runAsync(`INSERT INTO session_reads(server_id,session_id,last_read_agent_seq,
          last_read_interactive_id,initialized,updated_at) VALUES (?,?,?,?,1,?)
          ON CONFLICT(server_id,session_id) DO UPDATE SET
          last_read_agent_seq=excluded.last_read_agent_seq,
          last_read_interactive_id=excluded.last_read_interactive_id,
          initialized=1,updated_at=excluded.updated_at`, serverId, session.id,
        session.lastAgentMessageSeq, session.pendingInteractiveId, now);
      } else {
        await database.runAsync(`UPDATE session_reads SET last_read_agent_seq=?,
          last_read_interactive_id=?,initialized=1,updated_at=?
          WHERE server_id=? AND session_id=? AND initialized=0`, session.lastAgentMessageSeq,
        session.pendingInteractiveId, now, serverId, session.id);
      }
    }
    if (initializeBaseline) {
      await database.runAsync("UPDATE connections SET session_reads_initialized=1 WHERE server_id=?", serverId);
    }
  });
}

export async function getSyncCursor(serverId: string): Promise<number> {
  if (isPreviewMode && isPreviewServerId(serverId)) return 0;
  const database = await getDatabase();
  const row = await database.getFirstAsync<{ cursor: number }>(
    "SELECT cursor FROM sync_state WHERE server_id=?", serverId);
  return row?.cursor ?? 0;
}

export async function setSyncCursor(serverId: string, cursor: number): Promise<void> {
  if (isPreviewMode && isPreviewServerId(serverId)) return;
  await runDatabaseWrite((database) => database.runAsync(
    `INSERT INTO sync_state(server_id,cursor,last_synced_at) VALUES (?,?,?)
    ON CONFLICT(server_id) DO UPDATE SET cursor=excluded.cursor,last_synced_at=excluded.last_synced_at`,
    serverId, cursor, new Date().toISOString()));
}
