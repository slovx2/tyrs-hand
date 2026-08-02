import * as Linking from "expo-linking";
import { router, useLocalSearchParams } from "expo-router";
import { useEffect, useRef, useState } from "react";
import { View } from "react-native";

import { EmptyState, Screen } from "@/components/ui";
import { connectPairingUri } from "@/features/connections/connectPairing";
import { useAppStore } from "@/store/appStore";

export default function DevicePairScreen() {
  const params = useLocalSearchParams<Record<string, string | string[]>>();
  const ready = useAppStore((state) => state.ready);
  const started = useRef(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!ready || started.current) return;
    started.current = true;
    void (async () => {
      try {
        const initial = await Linking.getInitialURL();
        const query = new URLSearchParams();
        for (const [key, raw] of Object.entries(params)) {
          const value = Array.isArray(raw) ? raw[0] : raw;
          if (value) query.set(key, value);
        }
        const value = initial?.startsWith("tyrshand://device-pair")
          ? initial
          : `tyrshand://device-pair?${query}`;
        const serverId = await connectPairingUri(value);
        await useAppStore.getState().reloadConnections();
        await useAppStore.getState().switchConnection(serverId);
        router.replace("/(tabs)/connections");
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : "无法完成设备绑定");
      }
    })();
  }, [params, ready]);

  return <Screen><View testID={error ? "pairing:error" : "pairing:waiting"} style={{ flex: 1 }}>
    <EmptyState title={error ? "连接失败" : "等待管理员确认"}
      detail={error ?? "请回到管理后台确认这台设备；确认后会自动进入连接页。"} />
  </View></Screen>;
}
