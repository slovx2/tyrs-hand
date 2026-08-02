import { useCallback, useEffect, useState } from "react";

import { discardOutbox, listOutbox, retryOutbox, type OutboxItem } from "@/sync/outbox";

export function useOutbox(serverId: string | undefined, sessionId?: string) {
  const [items, setItems] = useState<OutboxItem[]>([]);
  const refresh = useCallback(async () => {
    if (!serverId) return setItems([]);
    setItems(await listOutbox(serverId, sessionId));
  }, [serverId, sessionId]);
  useEffect(() => { void refresh(); }, [refresh]);
  return {
    items,
    refresh,
    retry: async (localId: string) => { if (serverId) await retryOutbox(serverId, localId); await refresh(); },
    discard: async (localId: string) => { if (serverId) await discardOutbox(serverId, localId); await refresh(); },
  };
}
