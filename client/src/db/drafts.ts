import type { LocalAttachment } from "@/sync/outbox";
import type { SessionSettings } from "@/types/protocol";
import { getDatabase, runDatabaseWrite } from "./database";

export type Draft = {
  text: string;
  settings: SessionSettings | null;
  attachments: LocalAttachment[];
};

export async function loadDraft(serverId: string, scope: string): Promise<Draft | null> {
  const database = await getDatabase();
  const row = await database.getFirstAsync<{
    text: string;
    settings: string | null;
    attachment_ids: string;
  }>("SELECT text,settings,attachment_ids FROM drafts WHERE server_id=? AND scope=?", serverId, scope);
  if (!row) return null;
  try {
    return {
      text: row.text,
      settings: row.settings ? JSON.parse(row.settings) as SessionSettings : null,
      attachments: JSON.parse(row.attachment_ids) as LocalAttachment[],
    };
  } catch {
    await clearDraft(serverId, scope);
    return null;
  }
}

export async function saveDraft(serverId: string, scope: string, draft: Draft): Promise<void> {
  if (!draft.text && draft.attachments.length === 0 && !draft.settings) {
    await clearDraft(serverId, scope);
    return;
  }
  await runDatabaseWrite((database) => database.runAsync(
    `INSERT INTO drafts(server_id,scope,text,settings,attachment_ids,updated_at)
    VALUES (?,?,?,?,?,?) ON CONFLICT(server_id,scope) DO UPDATE SET
    text=excluded.text,settings=excluded.settings,attachment_ids=excluded.attachment_ids,
    updated_at=excluded.updated_at`, serverId, scope, draft.text,
    draft.settings ? JSON.stringify(draft.settings) : null, JSON.stringify(draft.attachments),
    new Date().toISOString()));
}

export async function clearDraft(serverId: string, scope: string): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(
    "DELETE FROM drafts WHERE server_id=? AND scope=?", serverId, scope));
}
