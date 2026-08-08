import type { Thread } from "@codex-app-server/v2/Thread";

import type { MobileProject, ThreadRecord } from "@/app-server/types";
import type { ControlBootstrap } from "@/types/control";
import { getDatabase, withDatabaseTransaction } from "./database";

export async function loadCachedProjects(profileId: string): Promise<MobileProject[]> {
  const database = await getDatabase();
  const rows = await database.getAllAsync<{ payload: string }>(
    "SELECT payload FROM projects WHERE profile_id=? ORDER BY lower(name),id", profileId);
  return rows.flatMap((row) => parseProject(row.payload));
}

export async function saveProjects(profileId: string, projects: MobileProject[],
  bootstrap: ControlBootstrap | null = null): Promise<void> {
  const now = new Date().toISOString();
  await withDatabaseTransaction(async (database) => {
    if (bootstrap) {
      await database.runAsync(`UPDATE connection_profiles SET bootstrap_payload=?,updated_at=?
        WHERE profile_id=?`, JSON.stringify(bootstrap), now, profileId);
    }
    await database.runAsync("DELETE FROM projects WHERE profile_id=?", profileId);
    for (const project of projects) {
      await database.runAsync(`INSERT INTO projects(profile_id,id,workspace_id,name,relative_path,
        payload,updated_at) VALUES (?,?,?,?,?,?,?)`, profileId, project.id,
      project.workspaceId ?? "ssh", project.name, project.relativePath, JSON.stringify(project), now);
    }
  });
}

export async function loadCachedThreads(profileId: string): Promise<ThreadRecord[]> {
  const database = await getDatabase();
  const rows = await database.getAllAsync<{ payload: string; archived: number }>(
    `SELECT payload,archived FROM threads WHERE profile_id=?
      ORDER BY updated_at DESC,id`, profileId);
  return rows.flatMap((row) => parseThreadRecord(row.payload, row.archived === 1));
}

export async function replaceCachedThreads(profileId: string,
  records: ThreadRecord[]): Promise<void> {
  await withDatabaseTransaction(async (database) => {
    const existingRows = await database.getAllAsync<{ id: string; payload: string }>(
      "SELECT id,payload FROM threads WHERE profile_id=?", profileId);
    const existing = new Map(existingRows.flatMap((row) => {
      const parsed = parseThreadRecord(row.payload, false)[0];
      return parsed ? [[row.id, parsed] as const] : [];
    }));
    await database.runAsync("DELETE FROM threads WHERE profile_id=?", profileId);
    for (const record of records) {
      const detailed = existing.get(record.thread.id);
      const merged = detailed && detailed.thread.turns.length > 0 && record.thread.turns.length === 0
        ? { ...record, thread: { ...record.thread, turns: detailed.thread.turns } }
        : record;
      await insertThread(database, profileId, merged);
    }
  });
}

export async function saveThreadRecord(profileId: string, record: ThreadRecord): Promise<void> {
  await withDatabaseTransaction(async (database) => insertThread(database, profileId, record));
}

async function insertThread(database: Awaited<ReturnType<typeof getDatabase>>, profileId: string,
  record: ThreadRecord): Promise<void> {
  await database.runAsync(`INSERT INTO threads(profile_id,id,archived,updated_at,payload)
    VALUES (?,?,?,?,?) ON CONFLICT(profile_id,id) DO UPDATE SET archived=excluded.archived,
    updated_at=excluded.updated_at,payload=excluded.payload`, profileId, record.thread.id,
  record.archived ? 1 : 0, record.thread.updatedAt, JSON.stringify(record));
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
    return thread && typeof thread.id === "string" && Array.isArray(thread.turns)
      ? [{ thread, archived, workspaceId: value.workspaceId ?? null,
        projectId: value.projectId ?? null }]
      : [];
  } catch {
    return [];
  }
}
