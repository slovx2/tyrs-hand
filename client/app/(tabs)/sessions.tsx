import { router, Tabs } from "expo-router";
import { useState } from "react";
import { StyleSheet, View } from "react-native";

import { EmptyState, Screen } from "@/components/ui";
import { ConnectionErrorBanner } from "@/components/ConnectionErrorBanner";
import { ConversationPane } from "@/features/chat/ConversationPane";
import { SessionActionsMenu } from "@/features/chat/SessionActionsMenu";
import { SessionListPane } from "@/features/session-list/SessionListPane";
import { useTablet } from "@/hooks/useTablet";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import { threadTitle } from "@/app-server/types";

export default function SessionsScreen() {
  const theme = useTheme();
  const tablet = useTablet();
  const sessions = useAppStore((state) => state.threads);
  const connection = useAppStore((state) => state.activeConnection);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const select = (id: string) => {
    if (tablet) setSelectedId(id);
    else router.push({ pathname: "/session/[id]", params: { id } });
  };
  const navigation = <Tabs.Screen options={{
    title: selectedId ? (() => {
      const record = sessions.find((item) => item.thread.id === selectedId);
      return record ? threadTitle(record.thread) : "会话";
    })() : "会话",
    ...(tablet && selectedId ? {
      headerRight: () => <SessionActionsMenu sessionId={selectedId} />,
    } : {}),
  }} />;
  if (connection?.kind === "control") {
    return <Screen>{navigation}<EmptyState title="尚未配置 SSH"
      detail="扫码授权只用于查看定时任务；项目和会话需要先在连接页添加 SSH。" /></Screen>;
  }
  const list = <View style={[styles.master, tablet && { borderRightColor: theme.colors.border }]}>
    <ConnectionErrorBanner />
    <SessionListPane sessions={sessions} selectedId={selectedId} onSelect={select}
      positionKey={`${connection?.profileId ?? "none"}:sessions`} />
  </View>;
  if (!tablet) return <Screen>{navigation}{list}</Screen>;
  return <Screen style={styles.horizontal}>{navigation}{list}<View style={styles.detail}>
    {selectedId ? <ConversationPane sessionId={selectedId} /> :
      <EmptyState title="选择一个会话" detail="消息、处理进度、计划和交互问答会显示在这里。" />}
  </View></Screen>;
}

const styles = StyleSheet.create({
  horizontal: { flexDirection: "row" },
  master: { flex: 1 },
  detail: { flex: 1.65 },
});
