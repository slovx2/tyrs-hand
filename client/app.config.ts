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
  icon: "./assets/icon.png",
  orientation: "default",
  userInterfaceStyle: "automatic",
  newArchEnabled: true,
  platforms: ["ios", "android"],
  ios: {
    supportsTablet: true,
    bundleIdentifier: `com.tyrshand.app${suffix}`,
    infoPlist: {
      NSAppTransportSecurity: { NSAllowsLocalNetworking: true },
      NSLocalNetworkUsageDescription: "用于连接本机一次性 SSH App Server 适配器",
    },
  },
  android: {
    package: `com.tyrshand.app${suffix}`,
    softwareKeyboardLayoutMode: "resize",
    adaptiveIcon: {
      foregroundImage: "./assets/adaptive-icon.png",
      backgroundColor: "#09090b",
    },
  },
  plugins: [
    "expo-router",
    ["expo-build-properties", { android: { usesCleartextTraffic: false } }],
    "expo-secure-store",
    "expo-sqlite",
    ["expo-camera", { cameraPermission: "允许 Tyrs Hand 拍摄会话附件" }],
    ["expo-image-picker", { photosPermission: "允许 Tyrs Hand 选择会话附件" }],
  ],
  experiments: { typedRoutes: true },
  extra: {
    eas: { projectId: "b9f3ad15-824e-483c-b6aa-44de731c5110" },
    appEnvironment: environment,
    previewMode: environment === "preview",
  },
});
