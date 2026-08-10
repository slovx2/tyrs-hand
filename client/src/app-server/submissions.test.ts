import { describe, expect, it, vi } from "vitest";

import { listPendingSubmissions, persistentSubmissionJournal } from "./submissions";

const database = vi.hoisted(() => ({
  getDatabase: vi.fn(),
  runDatabaseWrite: vi.fn(),
}));

vi.mock("@/preview/config", () => ({ isPreviewMode: true }));
vi.mock("@/db/database", () => database);

describe("Preview submission journal", () => {
  it("does not access SQLite for journal operations", async () => {
    const input = { profileId: "preview", clientMessageId: "message-1",
      threadId: null, projectId: "project-1", payload: { text: "hello" } };

    await persistentSubmissionJournal.prepare(input);
    await persistentSubmissionJournal.setThread("preview", "message-1", "thread-1");
    await persistentSubmissionJournal.markUnknown("preview", "message-1", "disconnected");
    await persistentSubmissionJournal.complete("preview", "message-1");

    expect(await listPendingSubmissions("preview")).toEqual([]);
    expect(database.runDatabaseWrite).not.toHaveBeenCalled();
    expect(database.getDatabase).not.toHaveBeenCalled();
  });
});
