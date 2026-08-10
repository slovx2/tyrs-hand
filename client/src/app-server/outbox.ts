import type { LocalAttachment } from "./attachments";
import type { TurnPreferences } from "./officialClient";
import { getDatabase, runDatabaseWrite } from "@/db/database";
import { isPreviewMode } from "@/preview/config";

export type NativeOutboxPayload = {
  text: string;
  attachments: LocalAttachment[];
  preferences: TurnPreferences;
};

export type NativeOutboxItem = {
  profileId: string;
  clientMessageId: string;
  kind: "create_task" | "submit_message";
  projectId: string;
  threadId: string | null;
  payload: NativeOutboxPayload;
  state: "pending" | "processing" | "failed";
  attemptCount: number;
  error: string | null;
};

const previewOutbox = new Map<string, NativeOutboxItem>();

export async function enqueueOutbox(input: Omit<NativeOutboxItem,
  "state" | "attemptCount" | "error">): Promise<void> {
  if (isPreviewMode) {
    const key = previewKey(input.profileId, input.clientMessageId);
    if (!previewOutbox.has(key)) previewOutbox.set(key,
      { ...input, state: "pending", attemptCount: 0, error: null });
    return;
  }
  const now = new Date().toISOString();
  await runDatabaseWrite((database) => database.runAsync(`INSERT INTO outbox(
    profile_id,client_message_id,kind,project_id,thread_id,payload,state,attempt_count,error,
    created_at,updated_at) VALUES (?,?,?,?,?,?,'pending',0,NULL,?,?)
    ON CONFLICT(profile_id,client_message_id) DO NOTHING`, input.profileId,
  input.clientMessageId, input.kind, input.projectId, input.threadId,
  JSON.stringify(input.payload), now, now));
}

export async function listOutbox(profileId: string): Promise<NativeOutboxItem[]> {
  if (isPreviewMode) return [...previewOutbox.values()].filter((item) =>
    item.profileId === profileId);
  const database = await getDatabase();
  const rows = await database.getAllAsync<{
    profile_id: string;
    client_message_id: string;
    kind: NativeOutboxItem["kind"];
    project_id: string;
    thread_id: string | null;
    payload: string;
    state: NativeOutboxItem["state"];
    attempt_count: number;
    error: string | null;
  }>(`SELECT profile_id,client_message_id,kind,project_id,thread_id,payload,state,
      attempt_count,error FROM outbox WHERE profile_id=? ORDER BY created_at`, profileId);
  return rows.map((row) => ({ profileId: row.profile_id,
    clientMessageId: row.client_message_id, kind: row.kind, projectId: row.project_id,
    threadId: row.thread_id, payload: JSON.parse(row.payload) as NativeOutboxPayload,
    state: row.state, attemptCount: row.attempt_count, error: row.error }));
}

export async function markOutboxProcessing(profileId: string,
  clientMessageId: string): Promise<void> {
  if (isPreviewMode) return updatePreview(profileId, clientMessageId,
    (item) => ({ ...item, state: "processing", error: null }));
  await runDatabaseWrite((database) => database.runAsync(`UPDATE outbox SET state='processing',
    error=NULL,updated_at=? WHERE profile_id=? AND client_message_id=?`, now(), profileId,
  clientMessageId));
}

export async function setOutboxThread(profileId: string, clientMessageId: string,
  threadId: string): Promise<void> {
  if (isPreviewMode) return updatePreview(profileId, clientMessageId,
    (item) => ({ ...item, threadId }));
  await runDatabaseWrite((database) => database.runAsync(`UPDATE outbox SET thread_id=?,
    updated_at=? WHERE profile_id=? AND client_message_id=?`, threadId, now(), profileId,
  clientMessageId));
}

export async function failOutbox(profileId: string, clientMessageId: string,
  error: string): Promise<void> {
  if (isPreviewMode) return updatePreview(profileId, clientMessageId,
    (item) => ({ ...item, state: "failed", attemptCount: item.attemptCount + 1, error }));
  await runDatabaseWrite((database) => database.runAsync(`UPDATE outbox SET state='failed',
    attempt_count=attempt_count+1,error=?,updated_at=?
    WHERE profile_id=? AND client_message_id=?`, error, now(), profileId, clientMessageId));
}

export async function completeOutbox(profileId: string, clientMessageId: string): Promise<void> {
  if (isPreviewMode) {
    previewOutbox.delete(previewKey(profileId, clientMessageId));
    return;
  }
  await runDatabaseWrite((database) => database.runAsync(
    "DELETE FROM outbox WHERE profile_id=? AND client_message_id=?", profileId, clientMessageId));
}

export async function discardOutboxItem(profileId: string, clientMessageId: string): Promise<void> {
  return completeOutbox(profileId, clientMessageId);
}

export async function retryOutboxItem(profileId: string, clientMessageId?: string): Promise<void> {
  if (isPreviewMode) {
    for (const item of previewOutbox.values()) {
      if (item.profileId === profileId && (!clientMessageId || item.clientMessageId === clientMessageId) &&
        (item.state === "failed" || item.state === "processing")) {
        previewOutbox.set(previewKey(profileId, item.clientMessageId),
          { ...item, state: "pending", error: null });
      }
    }
    return;
  }
  const suffix = clientMessageId ? " AND client_message_id=?" : "";
  const parameters = clientMessageId
    ? [now(), profileId, clientMessageId] : [now(), profileId];
  await runDatabaseWrite((database) => database.runAsync(`UPDATE outbox SET state='pending',
    error=NULL,updated_at=? WHERE profile_id=? AND state IN ('failed','processing')${suffix}`,
  ...parameters));
}

function now(): string { return new Date().toISOString(); }

function previewKey(profileId: string, clientMessageId: string): string {
  return `${profileId}:${clientMessageId}`;
}

function updatePreview(profileId: string, clientMessageId: string,
  update: (item: NativeOutboxItem) => NativeOutboxItem): void {
  const key = previewKey(profileId, clientMessageId);
  const current = previewOutbox.get(key);
  if (current) previewOutbox.set(key, update(current));
}
