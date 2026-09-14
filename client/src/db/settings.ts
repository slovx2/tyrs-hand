import { getDatabase, runDatabaseWrite } from "./database";
import type { TurnPreferences } from "@/app-server/officialClient";
import type { ThemeMode } from "@/theme/tokens";

export async function loadThemeMode(): Promise<ThemeMode> {
  const database = await getDatabase();
  const row = await database.getFirstAsync<{ value: string }>(
    "SELECT value FROM app_settings WHERE key='themeMode'");
  return row?.value === "light" || row?.value === "dark" ? row.value : "system";
}

export async function saveThemeMode(value: ThemeMode): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(
    `INSERT INTO app_settings(key,value) VALUES ('themeMode',?)
    ON CONFLICT(key) DO UPDATE SET value=excluded.value`, value));
}

export async function loadLiveConnectionSoundsEnabled(): Promise<boolean> {
  const database = await getDatabase();
  const row = await database.getFirstAsync<{ value: string }>(
    "SELECT value FROM app_settings WHERE key=?", "liveConnectionSounds");
  return row?.value !== "0";
}

export async function saveLiveConnectionSoundsEnabled(value: boolean): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(
    `INSERT INTO app_settings(key,value) VALUES (?,?)
    ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
  "liveConnectionSounds", value ? "1" : "0"));
}

export async function loadLastTurnPreferences(profileId: string): Promise<TurnPreferences | null> {
  const database = await getDatabase();
  const row = await database.getFirstAsync<{ value: string }>(
    "SELECT value FROM app_settings WHERE key=?", preferencesKey(profileId));
  if (!row) return null;
  try {
    const value = JSON.parse(row.value) as Partial<TurnPreferences>;
    if (typeof value.model !== "string" ||
      (value.effort !== null && typeof value.effort !== "string") ||
      (value.serviceTier !== null && typeof value.serviceTier !== "string") ||
      (value.collaborationMode !== "default" && value.collaborationMode !== "plan")) return null;
    return value as TurnPreferences;
  } catch {
    return null;
  }
}

export async function saveLastTurnPreferences(profileId: string,
  value: TurnPreferences): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(
    `INSERT INTO app_settings(key,value) VALUES (?,?)
    ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
  preferencesKey(profileId), JSON.stringify(value)));
}

export async function loadSelectedProjectId(profileId: string): Promise<string | null> {
  const database = await getDatabase();
  const row = await database.getFirstAsync<{ value: string }>(
    "SELECT value FROM app_settings WHERE key=?", selectedProjectKey(profileId));
  const value = row?.value?.trim();
  return value || null;
}

export async function saveSelectedProjectId(profileId: string,
  projectId: string | null): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(
    `INSERT INTO app_settings(key,value) VALUES (?,?)
    ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
  selectedProjectKey(profileId), projectId ?? ""));
}

export async function loadSelectedWorkerId(profileId: string): Promise<string | null> {
  const database = await getDatabase();
  const row = await database.getFirstAsync<{ value: string }>(
    "SELECT value FROM app_settings WHERE key=?", selectedWorkerKey(profileId));
  const value = row?.value?.trim();
  return value || null;
}

export async function saveSelectedWorkerId(profileId: string,
  workerId: string | null): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(
    `INSERT INTO app_settings(key,value) VALUES (?,?)
    ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
  selectedWorkerKey(profileId), workerId ?? ""));
}

function preferencesKey(profileId: string): string {
  return `lastTurnPreferences:${profileId}`;
}

export async function loadLiveConversationId(profileId: string, workerId: string): Promise<string | null> {
  const database = await getDatabase();
  const row = await database.getFirstAsync<{ value: string }>(
    "SELECT value FROM app_settings WHERE key=?", liveConversationKey(profileId, workerId));
  const value = row?.value?.trim();
  return value || null;
}

export async function loadLegacyLiveConversationId(profileId: string): Promise<string | null> {
  const database = await getDatabase();
  const row = await database.getFirstAsync<{ value: string }>(
    "SELECT value FROM app_settings WHERE key=?", legacyLiveConversationKey(profileId));
  const value = row?.value?.trim();
  return value || null;
}

export async function clearLegacyLiveConversationId(profileId: string): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(
    "DELETE FROM app_settings WHERE key=?", legacyLiveConversationKey(profileId)));
}

export async function saveLiveConversationId(profileId: string,
  workerId: string, conversationId: string | null): Promise<void> {
  await runDatabaseWrite((database) => database.runAsync(
    `INSERT INTO app_settings(key,value) VALUES (?,?)
    ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
  liveConversationKey(profileId, workerId), conversationId ?? ""));
}

function selectedProjectKey(profileId: string): string {
  return `selectedProject:${profileId}`;
}

function selectedWorkerKey(profileId: string): string {
  return `selectedWorker:${profileId}`;
}

function liveConversationKey(profileId: string, workerId: string): string {
  return `liveConversation:${profileId}:${workerId}`;
}

function legacyLiveConversationKey(profileId: string): string {
  return `liveConversation:${profileId}`;
}
