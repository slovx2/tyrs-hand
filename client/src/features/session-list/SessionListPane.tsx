import { useMemo, useRef, useState } from "react";
import { ActivityIndicator, FlatList, Pressable, StyleSheet, Text, View } from "react-native";

import { threadTitle, type ThreadRecord } from "@/app-server/types";
import { SegmentedControl } from "@/components/SegmentedControl";
import { EmptyState } from "@/components/ui";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import { loadListOffset, saveListOffset } from "./listPosition";

type Filter = "active" | "archived";

export function SessionListPane({ sessions, selectedId, onSelect, emptyDetail,
  testIDPrefix = "session", positionKey = testIDPrefix }: {
  sessions: ThreadRecord[];
  selectedId?: string | null;
  onSelect: (id: string) => void;
  emptyDetail?: string;
  testIDPrefix?: string;
  positionKey?: string;
}) {
  const theme = useTheme();
  const unreadThreadIds = useAppStore((state) => state.unreadThreadIds);
  const [filter, setFilter] = useState<Filter>("active");
  const list = useRef<FlatList<ThreadRecord>>(null);
  const filtered = useMemo(() => sessions.filter((record) =>
    filter === "archived" ? record.archived : !record.archived), [filter, sessions]);

  return <View style={styles.container}>
    <View style={styles.filter}><SegmentedControl testIDPrefix={`${testIDPrefix}s:filter`}
      value={filter} options={[{ value: "active", label: "进行中" },
        { value: "archived", label: "已归档" }] as const} onChange={setFilter} /></View>
    <FlatList ref={list} testID={`${testIDPrefix}s:list`} data={filtered}
      keyExtractor={(item) => item.thread.id}
      onLayout={() => list.current?.scrollToOffset({
        offset: loadListOffset(`${positionKey}:${filter}`), animated: false })}
      onScroll={(event) => saveListOffset(`${positionKey}:${filter}`,
        event.nativeEvent.contentOffset.y)} scrollEventThrottle={100}
      contentContainerStyle={[styles.list, filtered.length === 0 && styles.emptyList]}
      ListEmptyComponent={<EmptyState title="没有会话"
        detail={emptyDetail ?? "从项目页进入对应项目后创建第一个任务。"} />}
      renderItem={({ item, index }) => {
        const running = item.thread.status.type === "active";
        const unread = !running && unreadThreadIds[item.thread.id] === true;
        return <Pressable testID={`${testIDPrefix}:row:${index}`}
          onPress={() => onSelect(item.thread.id)} style={({ pressed }) => [styles.item, {
            backgroundColor: selectedId === item.thread.id || pressed
              ? theme.colors.surfaceAlt : "transparent",
          }]}>
          <View testID={`${testIDPrefix}:${encodeURIComponent(item.thread.id)}`} style={styles.copy}>
            <Text numberOfLines={1} style={[styles.title, { color: theme.colors.text }]}>
              {threadTitle(item.thread)}
            </Text>
            <View style={styles.statusSlot}
              accessibilityLabel={running ? "正在运行" : unread ? "有未读更新" : undefined}>
              {running && <ActivityIndicator testID={`${testIDPrefix}:running:${item.thread.id}`}
                size="small" color={theme.colors.textMuted} style={styles.spinner} />}
              {unread && <View testID={`${testIDPrefix}:unread:${item.thread.id}`}
                style={[styles.unread, { backgroundColor: theme.colors.danger }]} />}
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
  unread: { width: 8, height: 8, borderRadius: 4 },
});
