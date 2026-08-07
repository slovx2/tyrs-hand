import type { ConversationSnapshotResponse, ConversationTurn, RunActivity } from "@/types/protocol";
import type { SQLiteDatabase } from "expo-sqlite";
import { isPreviewMode, isPreviewServerId } from "@/preview/config";
import { conversationTurnSchema, runActivitySchema, sessionSchema, sessionSettingsSchema,
  runSnapshotSchema } from "@/types/protocol";
import { getDatabase, withDatabaseTransaction } from "./database";

export const conversationCacheBudgetBytes = 1000 * 1024 * 1024;

export type CachedConversationWindow = ConversationSnapshotResponse & {
  turnsComplete: boolean;
  hydrationState: "pending" | "running" | "complete" | "paused";
};

type SnapshotRow = {
  session_payload: string;
  settings_payload: string;
  current_run_payload: string | null;
  snapshot_cursor: number;
  next_cursor: string;
  has_more_before: number;
  turns_complete: number;
  hydration_state: CachedConversationWindow["hydrationState"];
};

function payloadBytes(...payloads: (string | null)[]): number {
  return payloads.reduce((total, payload) => total + (payload ? new TextEncoder().encode(payload).byteLength : 0), 0);
}

export async function loadConversationWindow(serverId: string, sessionId: string,
  limit = 20): Promise<CachedConversationWindow | null> {
  if (isPreviewMode && isPreviewServerId(serverId)) return null;
  const database = await getDatabase();
  const row = await database.getFirstAsync<SnapshotRow>(`SELECT session_payload,settings_payload,
    current_run_payload,snapshot_cursor,next_cursor,has_more_before,turns_complete,hydration_state
    FROM conversation_snapshots WHERE server_id=? AND session_id=?`, serverId, sessionId);
  if (!row) return null;
  const turnRows = await database.getAllAsync<{ payload: string }>(`SELECT payload FROM (
    SELECT payload,anchor_seq FROM conversation_turns WHERE server_id=? AND session_id=?
    ORDER BY anchor_seq DESC LIMIT ?) ORDER BY anchor_seq`, serverId, sessionId, limit);
  const session = sessionSchema.safeParse(JSON.parse(row.session_payload));
  const settings = sessionSettingsSchema.safeParse(JSON.parse(row.settings_payload));
  const currentRun = row.current_run_payload ? runSnapshotSchema.safeParse(JSON.parse(row.current_run_payload)) : null;
  const turns = turnRows.flatMap(({ payload }) => {
    const parsed = conversationTurnSchema.safeParse(JSON.parse(payload));
    return parsed.success ? [parsed.data] : [];
  });
  if (!session.success || !settings.success || currentRun && !currentRun.success) return null;
  await database.runAsync(`UPDATE conversation_snapshots SET last_accessed_at=?
    WHERE server_id=? AND session_id=?`, new Date().toISOString(), serverId, sessionId);
  const locallyOlder = turns[0] ? await hasCachedTurnBefore(serverId, sessionId, turns[0].anchorSeq) : false;
  return {
    session: session.data, settings: settings.data, currentRun: currentRun?.data ?? null,
    turns: { items: turns, hasMoreBefore: locallyOlder || row.has_more_before === 1,
      nextCursor: row.next_cursor },
    snapshotCursor: row.snapshot_cursor, turnsComplete: row.turns_complete === 1,
    hydrationState: row.hydration_state,
  };
}

export async function saveConversationSnapshot(serverId: string,
  snapshot: ConversationSnapshotResponse): Promise<void> {
  if (isPreviewMode && isPreviewServerId(serverId)) return;
  const now = new Date().toISOString();
  const sessionPayload = JSON.stringify(snapshot.session);
  const settingsPayload = JSON.stringify(snapshot.settings);
  const currentRunPayload = snapshot.currentRun ? JSON.stringify(snapshot.currentRun) : null;
  await withDatabaseTransaction(async (database) => {
    await database.runAsync(`INSERT INTO sessions(server_id,id,project_id,title,lifecycle_state,
      last_message_seq,last_activity_at,payload) VALUES (?,?,?,?,?,?,?,?)
      ON CONFLICT(server_id,id) DO UPDATE SET project_id=excluded.project_id,title=excluded.title,
      lifecycle_state=excluded.lifecycle_state,last_message_seq=excluded.last_message_seq,
      last_activity_at=excluded.last_activity_at,payload=excluded.payload`, serverId, snapshot.session.id,
    snapshot.session.projectId, snapshot.session.title, snapshot.session.lifecycleState,
    snapshot.session.lastMessageSeq, snapshot.session.lastActivityAt, sessionPayload);
    await database.runAsync(`INSERT INTO conversation_snapshots(server_id,session_id,session_payload,
      settings_payload,current_run_payload,snapshot_cursor,next_cursor,has_more_before,turns_complete,
      hydration_state,byte_size,last_accessed_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,'pending',?,?,?)
      ON CONFLICT(server_id,session_id) DO UPDATE SET session_payload=excluded.session_payload,
      settings_payload=excluded.settings_payload,current_run_payload=excluded.current_run_payload,
      snapshot_cursor=excluded.snapshot_cursor,next_cursor=excluded.next_cursor,
      has_more_before=excluded.has_more_before,
      turns_complete=max(conversation_snapshots.turns_complete,excluded.turns_complete),
      byte_size=excluded.byte_size,
      last_accessed_at=excluded.last_accessed_at,updated_at=excluded.updated_at`, serverId,
    snapshot.session.id, sessionPayload, settingsPayload, currentRunPayload, snapshot.snapshotCursor,
    snapshot.turns.nextCursor, snapshot.turns.hasMoreBefore ? 1 : 0,
    snapshot.turns.hasMoreBefore ? 0 : 1, payloadBytes(sessionPayload, settingsPayload, currentRunPayload), now, now);
    const first = snapshot.turns.items[0]?.anchorSeq;
    if (first !== undefined) {
      await database.runAsync(`DELETE FROM conversation_turns WHERE server_id=? AND session_id=?
        AND anchor_seq>=?`, serverId, snapshot.session.id, first);
    }
    await upsertTurns(database, serverId, snapshot.session.id, snapshot.turns.items, now);
  });
  await enforceConversationCacheBudget(serverId, snapshot.session.id);
}

async function upsertTurns(database: SQLiteDatabase, serverId: string, sessionId: string,
  turns: ConversationTurn[], now: string): Promise<void> {
  for (const turn of turns) {
    const payload = JSON.stringify(turn);
    await database.runAsync(`INSERT INTO conversation_turns(server_id,session_id,id,kind,anchor_seq,
      payload,byte_size,updated_at) VALUES (?,?,?,?,?,?,?,?)
      ON CONFLICT(server_id,session_id,id) DO UPDATE SET kind=excluded.kind,
      anchor_seq=excluded.anchor_seq,payload=excluded.payload,byte_size=excluded.byte_size,
      updated_at=excluded.updated_at`, serverId, sessionId, turn.id, turn.kind, turn.anchorSeq,
    payload, payloadBytes(payload), now);
  }
}

export async function saveConversationTurnPage(serverId: string, sessionId: string,
  items: ConversationTurn[], nextCursor: string, hasMoreBefore: boolean): Promise<void> {
  if (isPreviewMode && isPreviewServerId(serverId)) return;
  const now = new Date().toISOString();
  await withDatabaseTransaction(async (database) => {
    await upsertTurns(database, serverId, sessionId, items, now);
    await database.runAsync(`UPDATE conversation_snapshots SET next_cursor=?,has_more_before=?,
      turns_complete=?,hydration_state=?,updated_at=? WHERE server_id=? AND session_id=?`,
    nextCursor, hasMoreBefore ? 1 : 0, hasMoreBefore ? 0 : 1,
    hasMoreBefore ? "running" : "complete", now, serverId, sessionId);
  });
  await enforceConversationCacheBudget(serverId, sessionId);
}

export async function saveConversationTurn(serverId: string, sessionId: string,
  turn: ConversationTurn, replacedTurnId?: string): Promise<void> {
  if (isPreviewMode && isPreviewServerId(serverId)) return;
  await withDatabaseTransaction(async (database) => {
    if (replacedTurnId && replacedTurnId !== turn.id) {
      await database.runAsync(`DELETE FROM conversation_turns
        WHERE server_id=? AND session_id=? AND id=?`, serverId, sessionId, replacedTurnId);
    }
    await upsertTurns(database, serverId, sessionId, [turn], new Date().toISOString());
  });
}

export async function loadCachedTurnsBefore(serverId: string, sessionId: string,
  beforeAnchorSeq: number, limit = 20): Promise<ConversationTurn[]> {
  if (isPreviewMode && isPreviewServerId(serverId)) return [];
  const database = await getDatabase();
  const rows = await database.getAllAsync<{ payload: string }>(`SELECT payload FROM (
    SELECT payload,anchor_seq FROM conversation_turns WHERE server_id=? AND session_id=?
    AND anchor_seq<? ORDER BY anchor_seq DESC LIMIT ?) ORDER BY anchor_seq`, serverId, sessionId,
  beforeAnchorSeq, limit);
  return rows.flatMap(({ payload }) => {
    const parsed = conversationTurnSchema.safeParse(JSON.parse(payload));
    return parsed.success ? [parsed.data] : [];
  });
}

async function hasCachedTurnBefore(serverId: string, sessionId: string,
  beforeAnchorSeq: number): Promise<boolean> {
  const database = await getDatabase();
  const row = await database.getFirstAsync<{ present: number }>(`SELECT EXISTS(
    SELECT 1 FROM conversation_turns WHERE server_id=? AND session_id=? AND anchor_seq<?) AS present`,
  serverId, sessionId, beforeAnchorSeq);
  return row?.present === 1;
}

export async function updateHydrationState(serverId: string, sessionId: string,
  state: CachedConversationWindow["hydrationState"]): Promise<void> {
  if (isPreviewMode && isPreviewServerId(serverId)) return;
  const database = await getDatabase();
  await database.runAsync(`UPDATE conversation_snapshots SET hydration_state=?,updated_at=?
    WHERE server_id=? AND session_id=?`, state, new Date().toISOString(), serverId, sessionId);
}

export type SegmentActivityPage = {
  activities: RunActivity[];
  hasMoreBefore: boolean;
  persistedThroughEventSeq: number;
  finalAnswerDraft: { payload: { text: string } } | null;
};

export async function loadCachedSegmentActivities(serverId: string,
  segmentId: string): Promise<RunActivity[]> {
  if (isPreviewMode && isPreviewServerId(serverId)) return [];
  const database = await getDatabase();
  const rows = await database.getAllAsync<{ payload: string }>(`SELECT payload FROM run_activities
    WHERE server_id=? AND segment_id=? ORDER BY first_event_sequence`, serverId, segmentId);
  return rows.flatMap(({ payload }) => {
    const parsed = runActivitySchema.safeParse(JSON.parse(payload));
    return parsed.success ? [parsed.data] : [];
  });
}

export async function isSegmentCacheComplete(serverId: string, segmentId: string): Promise<boolean> {
  if (isPreviewMode && isPreviewServerId(serverId)) return false;
  const database = await getDatabase();
  const row = await database.getFirstAsync<{ complete: number }>(`SELECT complete FROM segment_cache_state
    WHERE server_id=? AND segment_id=?`, serverId, segmentId);
  return row?.complete === 1;
}

export async function saveSegmentActivityPage(serverId: string, sessionId: string, runId: string,
  segmentId: string, page: SegmentActivityPage, complete = false): Promise<void> {
  if (isPreviewMode && isPreviewServerId(serverId)) return;
  const now = new Date().toISOString();
  await withDatabaseTransaction(async (database) => {
    const finalDraft = page.finalAnswerDraft?.payload.text ?? "";
    await database.runAsync(`INSERT INTO segment_cache_state(server_id,session_id,run_id,segment_id,
      persisted_through_event_seq,has_more_before,complete,final_draft,byte_size,updated_at)
      VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(server_id,segment_id) DO UPDATE SET
      persisted_through_event_seq=max(segment_cache_state.persisted_through_event_seq,
        excluded.persisted_through_event_seq),has_more_before=excluded.has_more_before,
      complete=excluded.complete,final_draft=excluded.final_draft,byte_size=excluded.byte_size,
      updated_at=excluded.updated_at`, serverId, sessionId, runId, segmentId,
    page.persistedThroughEventSeq, page.hasMoreBefore ? 1 : 0, complete ? 1 : 0, finalDraft,
    payloadBytes(finalDraft), now);
    for (const activity of page.activities) {
      const payload = JSON.stringify(activity);
      await database.runAsync(`INSERT INTO run_activities(server_id,session_id,id,segment_id,
        first_event_sequence,last_event_sequence,payload,byte_size,updated_at) VALUES (?,?,?,?,?,?,?,?,?)
        ON CONFLICT(server_id,id) DO UPDATE SET segment_id=excluded.segment_id,
        first_event_sequence=excluded.first_event_sequence,last_event_sequence=excluded.last_event_sequence,
        payload=excluded.payload,byte_size=excluded.byte_size,updated_at=excluded.updated_at`, serverId,
      sessionId, activity.id, segmentId, activity.firstEventSequence, activity.lastEventSequence,
      payload, payloadBytes(payload), now);
    }
  });
  await enforceConversationCacheBudget(serverId, sessionId);
}

export async function enforceConversationCacheBudget(serverId: string,
  protectedSessionId: string | null, budgetBytes = conversationCacheBudgetBytes): Promise<string[]> {
  const database = await getDatabase();
  const total = await database.getFirstAsync<{ bytes: number }>(`SELECT
    COALESCE((SELECT sum(byte_size) FROM conversation_snapshots WHERE server_id=?),0)+
    COALESCE((SELECT sum(byte_size) FROM conversation_turns WHERE server_id=?),0)+
    COALESCE((SELECT sum(byte_size) FROM segment_cache_state WHERE server_id=?),0)+
    COALESCE((SELECT sum(byte_size) FROM run_activities WHERE server_id=?),0) AS bytes`,
  serverId, serverId, serverId, serverId);
  let used = total?.bytes ?? 0;
  if (used <= budgetBytes) return [];
  const candidates = await database.getAllAsync<{ session_id: string; bytes: number }>(`SELECT
    snapshot.session_id,snapshot.byte_size+
      COALESCE((SELECT sum(byte_size) FROM conversation_turns turn
        WHERE turn.server_id=snapshot.server_id AND turn.session_id=snapshot.session_id),0)+
      COALESCE((SELECT sum(byte_size) FROM segment_cache_state segment
        WHERE segment.server_id=snapshot.server_id AND segment.session_id=snapshot.session_id),0)+
      COALESCE((SELECT sum(byte_size) FROM run_activities activity
        WHERE activity.server_id=snapshot.server_id AND activity.session_id=snapshot.session_id),0) AS bytes
    FROM conversation_snapshots snapshot WHERE snapshot.server_id=? AND snapshot.session_id<>?
    ORDER BY snapshot.last_accessed_at`, serverId, protectedSessionId ?? "");
  const evicted: string[] = [];
  await withDatabaseTransaction(async (transaction) => {
    for (const candidate of candidates) {
      if (used <= budgetBytes) break;
      await transaction.runAsync(`DELETE FROM conversation_snapshots WHERE server_id=? AND session_id=?`,
        serverId, candidate.session_id);
      used -= candidate.bytes;
      evicted.push(candidate.session_id);
    }
  });
  if (evicted.length > 0) {
    await database.execAsync("PRAGMA wal_checkpoint(TRUNCATE); PRAGMA incremental_vacuum;");
  }
  return evicted;
}
