import type { Thread } from "@codex-app-server/v2/Thread";

import { THREAD_PAGE_SIZE } from "@/app-server/officialClient";
import type { MobileProject, ThreadRecord } from "@/app-server/types";
import { isPreviewMode } from "@/preview/config";
import { getDatabase, withDatabaseTransaction } from "./database";

export async function loadCachedProjects(profileId: string): Promise<MobileProject[]> {
  if (isPreviewMode) return [];
  const database = await getDatabase();
  const rows = await database.getAllAsync<{ payload: string }>(
    "SELECT payload FROM projects WHERE profile_id=? ORDER BY lower(name),id", profileId);
  return rows.flatMap((row) => parseProject(row.payload));
}

export async function saveProjects(profileId: string, projects: MobileProject[]): Promise<void> {
  if (isPreviewMode) return;
  const now = new Date().toISOString();
  await withDatabaseTransaction(async (database) => {
    await database.runAsync("DELETE FROM projects WHERE profile_id=?", profileId);
    for (const project of projects) {
      await database.runAsync(`INSERT INTO projects(profile_id,id,workspace_id,name,relative_path,
        payload,updated_at) VALUES (?,?,?,?,?,?,?)`, profileId, project.id,
      project.workspaceId ?? "ssh", project.name, project.relativePath, JSON.stringify(project), now);
    }
  });
}

export async function loadCachedThreads(profileId: string): Promise<ThreadRecord[]> {
  if (isPreviewMode) return [];
  const database = await getDatabase();
  const rows = await database.getAllAsync<{ payload: string; archived: number }>(
    `SELECT payload,archived FROM threads WHERE profile_id=?
      ORDER BY updated_at DESC,id`, profileId);
  return rows.flatMap((row) => parseThreadRecord(row.payload, row.archived === 1));
}

export async function replaceCachedThreads(profileId: string,
  records: ThreadRecord[]): Promise<void> {
  if (isPreviewMode) return;
  await withDatabaseTransaction(async (database) => {
    await database.runAsync("DELETE FROM threads WHERE profile_id=?", profileId);
    for (const record of records) {
      await insertThread(database, profileId, record);
    }
  });
}

export async function saveThreadRecord(profileId: string, record: ThreadRecord): Promise<void> {
  if (isPreviewMode) return;
  await withDatabaseTransaction(async (database) => insertThread(database, profileId, record));
}

async function insertThread(database: Awaited<ReturnType<typeof getDatabase>>, profileId: string,
  record: ThreadRecord): Promise<void> {
  const cached = cacheableThreadRecord(record);
  await database.runAsync(`INSERT INTO threads(profile_id,id,archived,updated_at,payload)
    VALUES (?,?,?,?,?) ON CONFLICT(profile_id,id) DO UPDATE SET archived=excluded.archived,
    updated_at=excluded.updated_at,payload=excluded.payload`, profileId, cached.thread.id,
  cached.archived ? 1 : 0, cached.thread.updatedAt, JSON.stringify(cached));
}

export function cacheableThreadRecord(record: ThreadRecord): ThreadRecord {
  if (record.history.kind !== "loaded") return record;
  return {
    ...record,
    thread: { ...record.thread, turns: record.thread.turns.slice(-THREAD_PAGE_SIZE) },
    history: {
      ...record.history,
      olderCursor: record.history.tailOlderCursor,
      hasLoadedOldest: record.history.tailOlderCursor === null,
    },
  };
}

function parseProject(payload: string): MobileProject[] {
  try {
    const value = JSON.parse(payload) as Partial<MobileProject>;
    return typeof value.id === "string" && typeof value.name === "string" &&
      typeof value.cwd === "string" ? [value as MobileProject] : [];
  } catch {
    return [];
  }
}

function parseThreadRecord(payload: string, archived: boolean): ThreadRecord[] {
  try {
    const value = JSON.parse(payload) as Partial<ThreadRecord>;
    const thread = value.thread as Thread | undefined;
    const history = value.history;
    const validHistory = history?.kind === "summary" || history?.kind === "loaded" &&
      (typeof history.olderCursor === "string" || history.olderCursor === null) &&
      (typeof history.tailOlderCursor === "string" || history.tailOlderCursor === null) &&
      typeof history.hasLoadedOldest === "boolean";
    return thread && typeof thread.id === "string" && Array.isArray(thread.turns) && validHistory
      ? [{ thread, archived, workspaceId: value.workspaceId ?? null,
        projectId: value.projectId ?? null, history }]
      : [];
  } catch {
    return [];
  }
}
