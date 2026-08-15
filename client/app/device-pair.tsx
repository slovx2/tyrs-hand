import * as Linking from "expo-linking";
import { router, useLocalSearchParams } from "expo-router";
import { useEffect, useRef, useState } from "react";
import { View } from "react-native";

import { EmptyState, Screen } from "@/components/ui";
import { resolvePairingUri } from "@/api/pairing";
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
        const value = resolvePairingUri(params, initial);
        const profileId = await connectPairingUri(value);
        await useAppStore.getState().reloadConnections();
        await useAppStore.getState().switchConnection(profileId);
        router.replace("/(tabs)/automations");
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : "无法完成定时任务授权");
      }
    })();
  }, [params, ready]);

  return <Screen><View testID={error ? "pairing:error" : "pairing:waiting"} style={{ flex: 1 }}>
    <EmptyState title={error ? "授权失败" : "等待管理员确认"}
      detail={error ?? "扫码只会授权查看这台机器的定时任务；请回到管理后台确认设备。"} />
  </View></Screen>;
}
