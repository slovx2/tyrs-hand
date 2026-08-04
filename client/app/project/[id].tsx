import { router, Stack, useLocalSearchParams } from "expo-router";
import { useMemo, useState } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { EmptyState, Screen } from "@/components/ui";
import { ConversationPane } from "@/features/chat/ConversationPane";
import { SessionListPane } from "@/features/session-list/SessionListPane";
import { useTablet } from "@/hooks/useTablet";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

export default function ProjectSessionsScreen() {
  const theme = useTheme();
  const tablet = useTablet();
  const insets = useSafeAreaInsets();
  const { id } = useLocalSearchParams<{ id: string }>();
  const bootstrap = useAppStore((state) => state.bootstrap);
  const connection = useAppStore((state) => state.activeConnection);
  const allSessions = useAppStore((state) => state.sessions);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const project = bootstrap?.projects.find((item) => item.id === id);
  const sessions = useMemo(() => allSessions.filter((item) => item.projectId === id),
    [allSessions, id]);
  const select = (sessionId: string) => {
    if (tablet) setSelectedId(sessionId);
    else router.push({ pathname: "/session/[id]", params: { id: sessionId } });
  };

  if (!project) {
    return <Screen><Stack.Screen options={{ title: "项目会话" }} />
      <EmptyState title="项目不可用" detail="它可能已被移除，或属于另一个 Control。" /></Screen>;
  }

  const navigation = <Stack.Screen options={{ title: project.name, headerBackTitle: "项目",
    gestureEnabled: true, fullScreenGestureEnabled: true,
    headerLeft: () => <Pressable testID="project:back" accessibilityRole="button"
      accessibilityLabel="返回" hitSlop={8} onPress={() => router.back()}
      style={styles.backButton}><Text style={[styles.backText, { color: theme.colors.accent }]}>‹ 返回</Text></Pressable> }} />;
  const list = <SessionListPane sessions={sessions} selectedId={selectedId} onSelect={select}
    positionKey={`${connection?.serverId ?? "none"}:project:${project.id}:sessions`}
    emptyDetail="这个项目还没有会话，点击右下角加号创建第一个任务。" />;
  const newTaskButton = <Pressable testID="project:new-task:add" accessibilityRole="button"
    accessibilityLabel="新建任务" onPress={() => router.push({
      pathname: "/project/[id]/new", params: { id: project.id },
    })} style={({ pressed }) => [styles.fab, { backgroundColor: theme.colors.accent,
      bottom: Math.max(insets.bottom, 16), opacity: pressed ? 0.78 : 1 }, theme.shadow]}>
    <Text style={[styles.fabText, { color: theme.colors.accentForeground }]}>＋</Text>
  </Pressable>;

  if (!tablet) return <Screen>{navigation}<View style={styles.mobileList}>{list}</View>
    {newTaskButton}</Screen>;

  return <Screen style={styles.horizontal}>{navigation}
    <View style={styles.master}>{list}</View>
    <View style={styles.detail}>{selectedId ? <ConversationPane sessionId={selectedId} /> :
      <EmptyState title="选择一个会话" detail="查看会话内容，或点击右下角加号创建新任务。" />}</View>
    {newTaskButton}
  </Screen>;
}

const styles = StyleSheet.create({
  horizontal: { flexDirection: "row" },
  mobileList: { flex: 1, minHeight: 0 },
  master: { flex: 1, minWidth: 300 },
  detail: { flex: 1.65 },
  backButton: { minHeight: 44, paddingRight: 12, justifyContent: "center" },
  backText: { fontFamily: "Inter_500Medium", fontSize: 16 },
  fab: { position: "absolute", right: 20, width: 56, height: 56, borderRadius: 28,
    alignItems: "center", justifyContent: "center", zIndex: 10, elevation: 6 },
  fabText: { fontFamily: "Inter_400Regular", fontSize: 34, lineHeight: 38, marginTop: -2 },
});
