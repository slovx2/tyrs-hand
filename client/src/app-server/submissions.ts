import { getDatabase, runDatabaseWrite } from "@/db/database";

export type PendingSubmission = {
  profileId: string;
  clientMessageId: string;
  threadId: string | null;
  projectId: string | null;
  payload: unknown;
  state: "prepared" | "unknown";
  error: string | null;
};

export interface SubmissionJournal {
  prepare(input: Omit<PendingSubmission, "state" | "error">): Promise<void>;
  setThread(profileId: string, clientMessageId: string, threadId: string): Promise<void>;
  markUnknown(profileId: string, clientMessageId: string, error: string): Promise<void>;
  complete(profileId: string, clientMessageId: string): Promise<void>;
}

export const persistentSubmissionJournal: SubmissionJournal = {
  async prepare(input) {
    const now = new Date().toISOString();
    await runDatabaseWrite((database) => database.runAsync(`INSERT INTO pending_submissions(
      profile_id,client_message_id,thread_id,project_id,payload,state,error,created_at,updated_at)
      VALUES (?,?,?,?,?,'prepared',NULL,?,?) ON CONFLICT(profile_id,client_message_id) DO NOTHING`,
    input.profileId, input.clientMessageId, input.threadId, input.projectId,
    JSON.stringify(input.payload), now, now));
  },
  async setThread(profileId, clientMessageId, threadId) {
    await runDatabaseWrite((database) => database.runAsync(`UPDATE pending_submissions
      SET thread_id=?,updated_at=? WHERE profile_id=? AND client_message_id=?`, threadId,
    new Date().toISOString(), profileId, clientMessageId));
  },
  async markUnknown(profileId, clientMessageId, error) {
    await runDatabaseWrite((database) => database.runAsync(`UPDATE pending_submissions
      SET state='unknown',error=?,updated_at=? WHERE profile_id=? AND client_message_id=?`,
    error, new Date().toISOString(), profileId, clientMessageId));
  },
  async complete(profileId, clientMessageId) {
    await runDatabaseWrite((database) => database.runAsync(`DELETE FROM pending_submissions
      WHERE profile_id=? AND client_message_id=?`, profileId, clientMessageId));
  },
};

export async function listPendingSubmissions(profileId: string): Promise<PendingSubmission[]> {
  const database = await getDatabase();
  const rows = await database.getAllAsync<{
    profile_id: string;
    client_message_id: string;
    thread_id: string | null;
    project_id: string | null;
    payload: string;
    state: "prepared" | "unknown";
    error: string | null;
  }>(`SELECT profile_id,client_message_id,thread_id,project_id,payload,state,error
      FROM pending_submissions WHERE profile_id=? ORDER BY created_at`, profileId);
  return rows.map((row) => ({ profileId: row.profile_id, clientMessageId: row.client_message_id,
    threadId: row.thread_id, projectId: row.project_id, payload: JSON.parse(row.payload) as unknown,
    state: row.state, error: row.error }));
}
