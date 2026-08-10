import * as Crypto from "expo-crypto";

import { isPreviewMode } from "@/preview/config";
import { getDatabase, runDatabaseWrite } from "./database";

export type SSHProject = {
  profileId: string;
  id: string;
  remotePath: string;
};

type SSHProjectRow = {
  profile_id: string;
  id: string;
  remote_path: string;
};

export async function listSSHProjects(profileId: string): Promise<SSHProject[]> {
  if (isPreviewMode) {
    const { listPreviewSSHProjects } = await import("@/preview/runtime");
    return listPreviewSSHProjects(profileId);
  }
  const database = await getDatabase();
  const rows = await database.getAllAsync<SSHProjectRow>(`SELECT profile_id,id,remote_path
    FROM ssh_projects WHERE profile_id=? ORDER BY lower(remote_path),id`, profileId);
  return rows.map((row) => ({ profileId: row.profile_id, id: row.id,
    remotePath: row.remote_path }));
}

export async function addSSHProject(profileId: string, remotePath: string): Promise<SSHProject> {
  const normalized = normalizeRemotePath(remotePath);
  const project = { profileId, id: Crypto.randomUUID(), remotePath: normalized };
  const now = new Date().toISOString();
  const result = await runDatabaseWrite((database) => database.runAsync(`INSERT OR IGNORE INTO
    ssh_projects(profile_id,id,remote_path,created_at,updated_at) VALUES (?,?,?,?,?)`,
  project.profileId, project.id, project.remotePath, now, now));
  if (result.changes === 0) throw new Error("该目录已经添加为项目");
  return project;
}

export async function removeSSHProject(profileId: string, projectId: string): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(
    "DELETE FROM ssh_projects WHERE profile_id=? AND id=?", profileId, projectId));
}

function normalizeRemotePath(value: string): string {
  const normalized = value.trim().replace(/\/+$/, "") || "/";
  if (!normalized.startsWith("/")) throw new Error("项目目录必须是绝对路径");
  return normalized;
}
