import { useLocalSearchParams } from "expo-router";
import { useEffect, useState } from "react";

import { EmptyState, Screen } from "@/components/ui";
import { openNotificationTarget } from "@/notifications/navigation";
import { useAppStore } from "@/store/appStore";

export default function NotificationRoute() {
  const { serverId, sessionId } = useLocalSearchParams<{ serverId?: string; sessionId?: string }>();
  const ready = useAppStore((state) => state.ready);
  const [invalid, setInvalid] = useState(false);
  useEffect(() => {
    if (!ready) return;
    void openNotificationTarget({ serverId, sessionId }).then((opened) => setInvalid(!opened));
  }, [ready, serverId, sessionId]);
  return <Screen><EmptyState title={invalid ? "无法打开通知" : "正在打开会话"}
    detail={invalid ? "通知对应的连接或会话已经不可用。" : "请稍候…"} /></Screen>;
}
