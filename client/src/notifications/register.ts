import Constants from "expo-constants";
import * as Device from "expo-device";
import * as Notifications from "expo-notifications";
import { Platform } from "react-native";

import { ControlApi } from "@/api/control";
import type { ControlConnection } from "@/db/connections";

Notifications.setNotificationHandler({
  handleNotification: async () => ({ shouldShowBanner: true, shouldShowList: true,
    shouldPlaySound: false, shouldSetBadge: false }),
});

export async function registerPush(connection: ControlConnection): Promise<void> {
  if (!Device.isDevice || (Platform.OS !== "ios" && Platform.OS !== "android")) return;
  const current = await Notifications.getPermissionsAsync();
  const permission = current.status === "granted" ? current : await Notifications.requestPermissionsAsync();
  if (permission.status !== "granted") return;
  if (Platform.OS === "android") {
    await Notifications.setNotificationChannelAsync("任务状态", {
      name: "任务状态", importance: Notifications.AndroidImportance.DEFAULT,
    });
  }
  const projectId = Constants.expoConfig?.extra?.eas?.projectId as string | undefined;
  if (!projectId) return;
  const token = (await Notifications.getExpoPushTokenAsync({ projectId })).data;
  const environment = String(Constants.expoConfig?.extra?.appEnvironment ?? "development");
  await new ControlApi(connection).putPushToken(token, Platform.OS, environment);
}
