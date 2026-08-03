import { router } from "expo-router";
import { useState } from "react";
import { StyleSheet, View } from "react-native";

import { EmptyState, Screen } from "@/components/ui";
import { ConversationPane } from "@/features/chat/ConversationPane";
import { SessionListPane } from "@/features/session-list/SessionListPane";
import { useTablet } from "@/hooks/useTablet";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

export default function SessionsScreen() {
  const theme = useTheme();
  const tablet = useTablet();
  const sessions = useAppStore((state) => state.sessions);
  const connection = useAppStore((state) => state.activeConnection);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const select = (id: string) => {
    if (tablet) setSelectedId(id);
    else router.push({ pathname: "/session/[id]", params: { id } });
  };
  const list = <View style={[styles.master, tablet && { borderRightColor: theme.colors.border }]}>
    <SessionListPane sessions={sessions} selectedId={selectedId} onSelect={select}
      positionKey={`${connection?.serverId ?? "none"}:sessions`} />
  </View>;
  if (!tablet) return <Screen>{list}</Screen>;
  return <Screen style={styles.horizontal}>{list}<View style={styles.detail}>
    {selectedId ? <ConversationPane sessionId={selectedId} /> :
      <EmptyState title="选择一个会话" detail="消息、运行进度、Plan 和交互问答会显示在这里。" />}
  </View></Screen>;
}

const styles = StyleSheet.create({
  horizontal: { flexDirection: "row" },
  master: { flex: 1 },
  detail: { flex: 1.65 },
});
