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

function preferencesKey(profileId: string): string {
  return `lastTurnPreferences:${profileId}`;
}
