import { ClientApi } from "@/api/client";
import { getDatabase, runDatabaseWrite } from "@/db/database";
import type { Connection } from "@/db/connections";
import type { SessionSettings } from "@/types/protocol";

export type LocalAttachment = {
  uri: string;
  name: string;
  mimeType: string | null;
  size: number | null;
};

type OutboxPayload = {
  text: string;
  attachments: LocalAttachment[];
  settings?: SessionSettings;
};

export type OutboxItem = {
  serverId: string;
  localId: string;
  kind: "create_session" | "send_message";
  sessionId: string | null;
  projectId: string | null;
  status: "pending" | "uploading" | "sending" | "failed";
  payload: OutboxPayload;
  error: string | null;
};

export async function enqueueTask(input: {
  connection: Connection;
  localId: string;
  projectId: string;
  text: string;
  settings: SessionSettings;
  attachments: LocalAttachment[];
}): Promise<void> {
  await insertOutbox(input.connection.serverId, input.localId, "create_session", null,
    input.projectId, { text: input.text, settings: input.settings, attachments: input.attachments });
}

export async function enqueueMessage(input: {
  connection: Connection;
  localId: string;
  sessionId: string;
  text: string;
  attachments: LocalAttachment[];
}): Promise<void> {
  await insertOutbox(input.connection.serverId, input.localId, "send_message", input.sessionId,
    null, { text: input.text, attachments: input.attachments });
}

async function insertOutbox(serverId: string, localId: string, kind: OutboxItem["kind"],
  sessionId: string | null, projectId: string | null, payload: OutboxPayload): Promise<void> {
  const now = new Date().toISOString();
  await runDatabaseWrite((database) => database.runAsync(
    `INSERT INTO outbox(server_id,local_id,kind,session_id,project_id,
    status,payload,created_at,updated_at) VALUES (?,?,?,?,?,'pending',?,?,?)
    ON CONFLICT(server_id,local_id) DO NOTHING`, serverId, localId, kind, sessionId,
    projectId, JSON.stringify(payload), now, now));
}

export async function listOutbox(serverId: string, sessionId?: string): Promise<OutboxItem[]> {
  const database = await getDatabase();
  const rows = sessionId
    ? await database.getAllAsync<Record<string, string | null>>(`SELECT * FROM outbox
      WHERE server_id=? AND session_id=? ORDER BY created_at`, serverId, sessionId)
    : await database.getAllAsync<Record<string, string | null>>(`SELECT * FROM outbox
      WHERE server_id=? ORDER BY created_at`, serverId);
  return rows.map((row) => ({
    serverId: String(row.server_id), localId: String(row.local_id),
    kind: row.kind as OutboxItem["kind"], sessionId: row.session_id ?? null,
    projectId: row.project_id ?? null, status: row.status as OutboxItem["status"],
    payload: JSON.parse(String(row.payload)) as OutboxPayload, error: row.error ?? null,
  }));
}

export async function retryOutbox(serverId: string, localId: string): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(
    `UPDATE outbox SET status='pending',error=NULL,updated_at=?
    WHERE server_id=? AND local_id=?`, new Date().toISOString(), serverId, localId));
}

export async function recoverFailedOutbox(serverId: string): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(
    `UPDATE outbox SET status='pending',error=NULL,updated_at=?
    WHERE server_id=? AND status='failed'`, new Date().toISOString(), serverId));
}

export async function discardOutbox(serverId: string, localId: string): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(
    "DELETE FROM outbox WHERE server_id=? AND local_id=?", serverId, localId));
}

let processing = false;

export async function processOutbox(connection: Connection): Promise<void> {
  if (processing) return;
  processing = true;
  try {
    const api = new ClientApi(connection);
    for (const item of await listOutbox(connection.serverId)) {
      if (item.status !== "pending" && item.status !== "uploading" && item.status !== "sending") continue;
      try {
        await runDatabaseWrite((database) => database.runAsync(
          `UPDATE outbox SET status='uploading',error=NULL,updated_at=?
          WHERE server_id=? AND local_id=?`, new Date().toISOString(), item.serverId, item.localId));
        const attachmentIds: string[] = [];
        for (const [index, attachment] of item.payload.attachments.entries()) {
          if (attachment.size !== null && attachment.size > 25 * 1024 * 1024) {
            throw new Error(`${attachment.name} 超过 25 MiB`);
          }
          const uploaded = await api.upload(`${item.localId}:${index}`, attachment);
          attachmentIds.push(uploaded.attachment.id);
        }
        await runDatabaseWrite((database) => database.runAsync(
          `UPDATE outbox SET status='sending',updated_at=?
          WHERE server_id=? AND local_id=?`, new Date().toISOString(), item.serverId, item.localId));
        if (item.kind === "create_session") {
          if (!item.projectId || !item.payload.settings) throw new Error("新会话参数不完整");
          await api.createSession({ projectId: item.projectId, settings: item.payload.settings,
            initialMessage: { localId: item.localId, text: item.payload.text, attachmentIds } });
        } else {
          if (!item.sessionId) throw new Error("会话消息缺少 Session ID");
          await api.sendMessage(item.sessionId,
            { localId: item.localId, text: item.payload.text, attachmentIds });
        }
        await runDatabaseWrite((database) => database.runAsync(
          "DELETE FROM outbox WHERE server_id=? AND local_id=?", item.serverId, item.localId));
      } catch (error) {
        await runDatabaseWrite((database) => database.runAsync(
          `UPDATE outbox SET status='failed',attempt_count=attempt_count+1,
          error=?,updated_at=? WHERE server_id=? AND local_id=?`,
          error instanceof Error ? error.message : "发送失败", new Date().toISOString(),
          item.serverId, item.localId));
      }
    }
  } finally {
    processing = false;
  }
}
