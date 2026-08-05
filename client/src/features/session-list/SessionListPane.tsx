import { useMemo, useRef, useState } from "react";
import { ActivityIndicator, FlatList, Pressable, StyleSheet, Text, View } from "react-native";

import { SegmentedControl } from "@/components/SegmentedControl";
import { EmptyState } from "@/components/ui";
import { sessionListIndicator } from "@/db/sessionReadStatus";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import type { Session } from "@/types/protocol";
import { loadListOffset, saveListOffset } from "./listPosition";

type Filter = "active" | "archived";

export function SessionListPane({ sessions, selectedId, onSelect, emptyDetail, testIDPrefix = "session",
  positionKey = testIDPrefix }: {
  sessions: Session[];
  selectedId?: string | null;
  onSelect: (id: string) => void;
  emptyDetail?: string;
  testIDPrefix?: string;
  positionKey?: string;
}) {
  const theme = useTheme();
  const sessionReads = useAppStore((state) => state.sessionReads);
  const [filter, setFilter] = useState<Filter>("active");
  const list = useRef<FlatList<Session>>(null);
  const filtered = useMemo(() => sessions.filter((session) => filter === "archived" ?
    session.lifecycleState === "archived" : session.lifecycleState !== "archived"), [filter, sessions]);

  return <View style={styles.container}>
    <View style={styles.filter}><SegmentedControl testIDPrefix={`${testIDPrefix}s:filter`} value={filter} options={[
      { value: "active", label: "进行中" }, { value: "archived", label: "已归档" },
    ] as const} onChange={setFilter} /></View>
    <FlatList ref={list} testID={`${testIDPrefix}s:list`} data={filtered} keyExtractor={(item) => item.id}
      onLayout={() => list.current?.scrollToOffset({ offset: loadListOffset(`${positionKey}:${filter}`), animated: false })}
      onScroll={(event) => saveListOffset(`${positionKey}:${filter}`, event.nativeEvent.contentOffset.y)}
      scrollEventThrottle={100}
      contentContainerStyle={[styles.list, filtered.length === 0 && styles.emptyList]}
      ListEmptyComponent={<EmptyState title="没有会话"
        detail={emptyDetail ?? "从项目页进入对应项目后创建第一个任务。"} />}
      renderItem={({ item, index }) => {
        const indicator = sessionListIndicator(item, sessionReads[item.id]);
        return <Pressable testID={`${testIDPrefix}:row:${index}`} onPress={() => onSelect(item.id)}
          style={({ pressed }) => [styles.item, {
            backgroundColor: selectedId === item.id || pressed ? theme.colors.surfaceAlt : "transparent",
          }]}>
          <View testID={`${testIDPrefix}:${encodeURIComponent(item.id)}`} style={styles.copy}>
            <Text numberOfLines={1} style={[styles.title, { color: theme.colors.text }]}>
              {item.title || "新的开发任务"}
            </Text>
            <View style={styles.statusSlot} accessibilityLabel={indicator === "running" ? "正在运行" :
              indicator === "issue" ? "任务已停止或发生错误" :
                indicator === "unread" ? "有未读消息" : undefined}>
              {indicator === "running" ? <ActivityIndicator testID={`${testIDPrefix}:running:${item.id}`}
                size="small" color={theme.colors.textMuted} style={styles.spinner} /> :
                indicator === "issue" ? <View testID={`${testIDPrefix}:issue:${item.id}`}
                  style={[styles.issueIcon, { borderColor: theme.colors.danger }]}>
                  <Text style={[styles.issueText, { color: theme.colors.danger }]}>!</Text>
                </View> : indicator === "unread" ?
                  <View testID={`${testIDPrefix}:unread:${item.id}`}
                    style={[styles.unreadDot, { backgroundColor: theme.colors.danger }]} /> : null}
            </View>
          </View>
        </Pressable>;
      }} />
  </View>;
}

const styles = StyleSheet.create({
  container: { flex: 1 },
  filter: { padding: 12 },
  list: { paddingHorizontal: 12 },
  emptyList: { flexGrow: 1 },
  item: { height: 46, paddingHorizontal: 8, justifyContent: "center" },
  copy: { flex: 1, minWidth: 0, flexDirection: "row", alignItems: "center", gap: 8 },
  title: { flex: 1, minWidth: 0, fontFamily: "Inter_500Medium", fontSize: 15, lineHeight: 20 },
  statusSlot: { width: 18, height: 20, alignItems: "center", justifyContent: "center" },
  spinner: { transform: [{ scale: 0.72 }] },
  issueIcon: { width: 14, height: 14, borderRadius: 7, borderWidth: 1.5,
    alignItems: "center", justifyContent: "center" },
  issueText: { fontFamily: "Inter_600SemiBold", fontSize: 10, lineHeight: 11 },
  unreadDot: { width: 7, height: 7, borderRadius: 4 },
});
