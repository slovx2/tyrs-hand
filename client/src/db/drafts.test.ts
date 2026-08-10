import { describe, expect, it, vi } from "vitest";

import { clearDraft, loadDraft, saveDraft } from "./drafts";

const database = vi.hoisted(() => ({
  getDatabase: vi.fn(),
  runDatabaseWrite: vi.fn(),
}));

vi.mock("@/preview/config", () => ({ isPreviewMode: true }));
vi.mock("./database", () => database);

describe("Preview drafts", () => {
  it("does not access SQLite", async () => {
    expect(await loadDraft("preview", "thread-1")).toBeNull();
    await saveDraft("preview", "thread-1", { text: "draft", settings: null, attachments: [] });
    await clearDraft("preview", "thread-1");

    expect(database.getDatabase).not.toHaveBeenCalled();
    expect(database.runDatabaseWrite).not.toHaveBeenCalled();
  });
});
