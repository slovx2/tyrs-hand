import type { LocalAttachment } from "@/app-server/attachments";
import { isPreviewMode } from "@/preview/config";
import { getDatabase, runDatabaseWrite } from "./database";

export type PendingMessagePreview = {
  profileId: string;
  clientMessageId: string;
  threadId: string | null;
  projectId: string;
  text: string;
  attachments: LocalAttachment[];
  createdAt: string;
};

const previewPending = new Map<string, PendingMessagePreview>();

export async function listPendingMessagePreviews(profileId: string): Promise<PendingMessagePreview[]> {
  if (isPreviewMode) return [...previewPending.values()].filter((item) => item.profileId === profileId);
  const database = await getDatabase();
  const rows = await database.getAllAsync<{
    profile_id: string;
    client_message_id: string;
    thread_id: string | null;
    project_id: string;
    text: string;
    attachments: string;
    created_at: string;
  }>(`SELECT profile_id,client_message_id,thread_id,project_id,text,attachments,created_at
      FROM pending_message_previews WHERE profile_id=? ORDER BY created_at`, profileId);
  return rows.map((row) => ({ profileId: row.profile_id, clientMessageId: row.client_message_id,
    threadId: row.thread_id, projectId: row.project_id, text: row.text,
    attachments: JSON.parse(row.attachments) as LocalAttachment[], createdAt: row.created_at }));
}

export async function savePendingMessagePreview(input: Omit<PendingMessagePreview, "createdAt">): Promise<void> {
  const createdAt = new Date().toISOString();
  const value = { ...input, createdAt };
  if (isPreviewMode) {
    previewPending.set(pendingKey(input.profileId, input.clientMessageId), value);
    return;
  }
  await runDatabaseWrite((database) => database.runAsync(`INSERT INTO pending_message_previews(
    profile_id,client_message_id,thread_id,project_id,text,attachments,created_at)
    VALUES (?,?,?,?,?,?,?) ON CONFLICT(profile_id,client_message_id) DO UPDATE SET
    thread_id=excluded.thread_id,project_id=excluded.project_id,text=excluded.text,
    attachments=excluded.attachments`, input.profileId, input.clientMessageId, input.threadId,
  input.projectId, input.text, JSON.stringify(input.attachments), createdAt));
}

export async function removePendingMessagePreview(profileId: string, clientMessageId: string): Promise<void> {
  if (isPreviewMode) {
    previewPending.delete(pendingKey(profileId, clientMessageId));
    return;
  }
  await runDatabaseWrite((database) => database.runAsync(
    "DELETE FROM pending_message_previews WHERE profile_id=? AND client_message_id=?",
    profileId, clientMessageId));
}

function pendingKey(profileId: string, clientMessageId: string): string {
  return `${profileId}:${clientMessageId}`;
}
