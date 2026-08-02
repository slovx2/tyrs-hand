import type { ConfigContext, ExpoConfig } from "expo/config";

type AppEnvironment = "development" | "preview" | "production";

const environment = (process.env.APP_ENV ?? "development") as AppEnvironment;
const suffix = environment === "production" ? "" : `.${environment === "development" ? "dev" : "preview"}`;
const appName = environment === "production" ? "Tyrs Hand" : `Tyrs Hand ${environment === "development" ? "Dev" : "Preview"}`;

export default ({ config }: ConfigContext): ExpoConfig => ({
  ...config,
  name: appName,
  slug: "tyrs-hand",
  scheme: "tyrshand",
  version: "0.1.0",
  orientation: "default",
  userInterfaceStyle: "automatic",
  newArchEnabled: true,
  platforms: ["ios", "android"],
  ios: {
    supportsTablet: true,
    bundleIdentifier: `com.tyrshand.app${suffix}`,
    infoPlist: { UIBackgroundModes: ["remote-notification"] },
  },
  android: {
    package: `com.tyrshand.app${suffix}`,
    softwareKeyboardLayoutMode: "pan",
    adaptiveIcon: { backgroundColor: "#f6f8fa" },
  },
  plugins: [
    "expo-router",
    ["expo-build-properties", { android: { usesCleartextTraffic: environment !== "production" } }],
    "expo-secure-store",
    "expo-sqlite",
    ["expo-camera", { cameraPermission: "允许 Tyrs Hand 扫描设备二维码和拍摄附件" }],
    ["expo-image-picker", { photosPermission: "允许 Tyrs Hand 选择会话附件" }],
    ["expo-notifications", { defaultChannel: "任务状态" }],
  ],
  experiments: { typedRoutes: true },
  extra: { appEnvironment: environment },
});
