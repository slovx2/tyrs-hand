import { CodexJsonRpcClient } from "./jsonRpc";
import { OfficialAppServerClient } from "./officialClient";
import { persistentSubmissionJournal } from "./submissions";
import { createSocketFactory } from "./transports";
import { targetKey } from "./types";
import type { Connection } from "@/db/connections";

type RegistryEntry = {
  signature: string;
  client: OfficialAppServerClient;
  rpc: CodexJsonRpcClient;
};

const entries = new Map<string, RegistryEntry>();

export function officialClientFor(connection: Connection,
  workspaceId: string | null): OfficialAppServerClient {
  const key = targetKey(connection.profileId, workspaceId);
  const signature = JSON.stringify(connection);
  const current = entries.get(key);
  if (current?.signature === signature) return current.client;
  current?.rpc.close();
  const rpc = new CodexJsonRpcClient(createSocketFactory(workspaceId === null
    ? { connection } : { connection, workspaceId }));
  const client = new OfficialAppServerClient(connection.profileId, rpc,
    persistentSubmissionJournal);
  entries.set(key, { signature, client, rpc });
  return client;
}

export function closeOfficialProfile(profileId: string): void {
  for (const [key, entry] of entries) {
    if (!key.startsWith(`${profileId}:`)) continue;
    entry.rpc.close();
    entries.delete(key);
  }
}
