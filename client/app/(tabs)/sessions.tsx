import { router } from "expo-router";
import { useMemo, useState } from "react";
import { FlatList, Pressable, StyleSheet, View } from "react-native";

import { SegmentedControl } from "@/components/SegmentedControl";
import { Card, EmptyState, Muted, Screen, StatusDot, Title } from "@/components/ui";
import { ConversationPane } from "@/features/chat/ConversationPane";
import { useTablet } from "@/hooks/useTablet";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

type Filter = "active" | "archived";

export default function SessionsScreen() {
  const theme = useTheme();
  const tablet = useTablet();
  const sessions = useAppStore((state) => state.sessions);
  const [filter, setFilter] = useState<Filter>("active");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const filtered = useMemo(() => sessions.filter((session) => filter === "archived" ?
    session.lifecycleState === "archived" : session.lifecycleState !== "archived"), [filter, sessions]);
  const select = (id: string) => {
    if (tablet) setSelectedId(id);
    else router.push({ pathname: "/session/[id]", params: { id } });
  };
  const list = <View style={[styles.master, tablet && { borderRightColor: theme.colors.border }]}>
    <View style={styles.filter}><SegmentedControl testIDPrefix="sessions:filter" value={filter} options={[
      { value: "active", label: "进行中" }, { value: "archived", label: "已归档" },
    ] as const} onChange={setFilter} /></View>
    <FlatList testID="sessions:list" data={filtered} keyExtractor={(item) => item.id} contentContainerStyle={styles.list}
      ListEmptyComponent={<EmptyState title="没有会话" detail="从项目页底部的聊天输入框创建第一个任务。" />}
      renderItem={({ item, index }) => <Pressable testID={`session:row:${index}`}
        onPress={() => select(item.id)}>
        <Card testID={`session:${encodeURIComponent(item.id)}`}
          style={[styles.item, selectedId === item.id && { borderColor: theme.colors.accent }]}> 
          <View style={styles.row}><StatusDot status={item.lifecycleState === "active" ? "success" : "warning"} />
            <Title>{item.title || "新的开发任务"}</Title></View>
          <Muted>{item.collaborationMode} · {item.serviceTier} · {new Date(item.lastActivityAt).toLocaleString("zh-CN")}</Muted>
        </Card>
      </Pressable>} />
  </View>;
  if (!tablet) return <Screen>{list}</Screen>;
  return <Screen style={styles.horizontal}>{list}<View style={styles.detail}>
    {selectedId ? <ConversationPane sessionId={selectedId} /> :
      <EmptyState title="选择一个会话" detail="消息、运行进度、Plan 和交互问答会显示在这里。" />}
  </View></Screen>;
}

const styles = StyleSheet.create({
  horizontal: { flexDirection: "row" }, master: { flex: 1 }, detail: { flex: 1.65 },
  filter: { padding: 12 }, list: { padding: 12, paddingTop: 0, gap: 8 },
  item: { gap: 6 }, row: { flexDirection: "row", alignItems: "center", gap: 8 },
});
