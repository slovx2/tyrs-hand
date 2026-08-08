import type { LocalAttachment } from "@/app-server/attachments";
import type { TurnPreferences } from "@/app-server/officialClient";
import { getDatabase, runDatabaseWrite } from "./database";

export type Draft = {
  text: string;
  settings: TurnPreferences | null;
  attachments: LocalAttachment[];
};

export async function loadDraft(profileId: string, scope: string): Promise<Draft | null> {
  const database = await getDatabase();
  const row = await database.getFirstAsync<{
    text: string;
    settings: string | null;
    attachment_ids: string;
  }>("SELECT text,settings,attachment_ids FROM drafts WHERE profile_id=? AND scope=?",
  profileId, scope);
  if (!row) return null;
  try {
    return { text: row.text,
      settings: row.settings ? JSON.parse(row.settings) as TurnPreferences : null,
      attachments: JSON.parse(row.attachment_ids) as LocalAttachment[] };
  } catch {
    await clearDraft(profileId, scope);
    return null;
  }
}

export async function saveDraft(profileId: string, scope: string, draft: Draft): Promise<void> {
  if (!draft.text && draft.attachments.length === 0 && !draft.settings) {
    await clearDraft(profileId, scope);
    return;
  }
  await runDatabaseWrite((database) => database.runAsync(
    `INSERT INTO drafts(profile_id,scope,text,settings,attachment_ids,updated_at)
    VALUES (?,?,?,?,?,?) ON CONFLICT(profile_id,scope) DO UPDATE SET
    text=excluded.text,settings=excluded.settings,attachment_ids=excluded.attachment_ids,
    updated_at=excluded.updated_at`, profileId, scope, draft.text,
  draft.settings ? JSON.stringify(draft.settings) : null, JSON.stringify(draft.attachments),
  new Date().toISOString()));
}

export async function clearDraft(profileId: string, scope: string): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(
    "DELETE FROM drafts WHERE profile_id=? AND scope=?", profileId, scope));
}
