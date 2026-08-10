import type { ThreadRecord } from "@/app-server/types";

export function mergeThreadCatalog(summaries: ThreadRecord[], existing: ThreadRecord[],
  pendingIds: ReadonlySet<string>): ThreadRecord[] {
  const listedIds = new Set(summaries.map((record) => record.thread.id));
  const optimistic = existing.filter((record) => pendingIds.has(record.thread.id) &&
    !listedIds.has(record.thread.id));
  const byId = new Map(existing.map((record) => [record.thread.id, record]));
  return [...summaries, ...optimistic].map((summary) => {
    const loaded = byId.get(summary.thread.id);
    if (!loaded || loaded.history.kind !== "loaded") return summary;
    return { ...summary, history: loaded.history,
      thread: { ...summary.thread, turns: loaded.thread.turns } };
  }).sort((left, right) => (right.thread.recencyAt ?? right.thread.updatedAt) -
    (left.thread.recencyAt ?? left.thread.updatedAt));
}
