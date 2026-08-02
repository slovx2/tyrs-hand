import { getDatabase, runDatabaseWrite } from "./database";
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
