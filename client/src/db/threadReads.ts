import { isPreviewMode } from "@/preview/config";
import { getDatabase, withDatabaseTransaction } from "./database";

const initializedKey = (profileId: string) => `thread_reads_initialized:${profileId}`;

export async function loadUnreadThreadIds(profileId: string): Promise<string[]> {
  if (isPreviewMode) return [];
  const database = await getDatabase();
  const rows = await database.getAllAsync<{ thread_id: string }>(
    `SELECT thread_id FROM thread_reads WHERE profile_id=? AND has_unread=1
      ORDER BY updated_at DESC`, profileId);
  return rows.map((row) => row.thread_id);
}

/**
 * 第一次拿到完整目录时只建立已读基线；后续目录中新出现的 Thread 才标记未读。
 */
export async function reconcileThreadReads(profileId: string,
  threadIds: readonly string[]): Promise<string[]> {
  if (isPreviewMode) return [];
  const now = new Date().toISOString();
  await withDatabaseTransaction(async (database) => {
    const initialized = await database.getFirstAsync<{ value: string }>(
      "SELECT value FROM app_settings WHERE key=?", initializedKey(profileId));
    const knownRows = await database.getAllAsync<{ thread_id: string }>(
      "SELECT thread_id FROM thread_reads WHERE profile_id=?", profileId);
    const insertions = catalogReadInsertions(threadIds,
      knownRows.map((row) => row.thread_id), initialized !== null && initialized !== undefined);
    for (const { threadId, hasUnread } of insertions) {
      await database.runAsync(`INSERT OR IGNORE INTO thread_reads(
        profile_id,thread_id,has_unread,updated_at) VALUES (?,?,?,?)`,
      profileId, threadId, hasUnread, now);
    }
    if (!initialized) {
      await database.runAsync(`INSERT INTO app_settings(key,value) VALUES (?,?)
        ON CONFLICT(key) DO UPDATE SET value=excluded.value`, initializedKey(profileId), now);
    }
    if (threadIds.length > 0) {
      const placeholders = threadIds.map(() => "?").join(",");
      await database.runAsync(`DELETE FROM thread_reads WHERE profile_id=?
        AND thread_id NOT IN (${placeholders})`, profileId, ...threadIds);
    } else {
      await database.runAsync("DELETE FROM thread_reads WHERE profile_id=?", profileId);
    }
  });
  return loadUnreadThreadIds(profileId);
}

export function catalogReadInsertions(threadIds: readonly string[], knownThreadIds: readonly string[],
  initialized: boolean): { threadId: string; hasUnread: 0 | 1 }[] {
  const known = new Set(knownThreadIds);
  return threadIds.filter((threadId) => !known.has(threadId)).map((threadId) => ({ threadId,
    hasUnread: initialized ? 1 : 0 }));
}

export async function markThreadUnread(profileId: string, threadId: string): Promise<void> {
  if (isPreviewMode) return;
  await withDatabaseTransaction(async (database) => {
    await database.runAsync(`INSERT INTO thread_reads(profile_id,thread_id,has_unread,updated_at)
      VALUES (?,?,1,?) ON CONFLICT(profile_id,thread_id) DO UPDATE SET
      has_unread=1,updated_at=excluded.updated_at`, profileId, threadId,
    new Date().toISOString());
  });
}

export async function markThreadRead(profileId: string, threadId: string): Promise<void> {
  if (isPreviewMode) return;
  await withDatabaseTransaction(async (database) => {
    await database.runAsync(`INSERT INTO thread_reads(profile_id,thread_id,has_unread,updated_at)
      VALUES (?,?,0,?) ON CONFLICT(profile_id,thread_id) DO UPDATE SET
      has_unread=0,updated_at=excluded.updated_at`, profileId, threadId,
    new Date().toISOString());
  });
}

export async function removeThreadRead(profileId: string, threadId: string): Promise<void> {
  if (isPreviewMode) return;
  await withDatabaseTransaction(async (database) => {
    await database.runAsync("DELETE FROM thread_reads WHERE profile_id=? AND thread_id=?",
      profileId, threadId);
  });
}
