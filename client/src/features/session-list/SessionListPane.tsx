import { useMemo, useRef, useState } from "react";
import { FlatList, Pressable, StyleSheet, Text, View } from "react-native";

import { SegmentedControl } from "@/components/SegmentedControl";
import { EmptyState, StatusDot } from "@/components/ui";
import { useTheme } from "@/theme/ThemeProvider";
import type { Session } from "@/types/protocol";
import { loadListOffset, saveListOffset } from "./listPosition";

type Filter = "active" | "archived";

function sessionStatus(session: Session): { label: string; dot: "success" | "warning" | "muted" } {
  switch (session.lifecycleState) {
    case "archive_pending": return { label: "归档中", dot: "warning" };
    case "archived": return { label: "已归档", dot: "muted" };
    case "unarchive_pending": return { label: "恢复中", dot: "warning" };
    default: return { label: "进行中", dot: "success" };
  }
}

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
        const status = sessionStatus(item);
        return <Pressable testID={`${testIDPrefix}:row:${index}`} onPress={() => onSelect(item.id)}
          style={({ pressed }) => [styles.item, { borderBottomColor: theme.colors.border,
            backgroundColor: selectedId === item.id || pressed ? theme.colors.surfaceAlt : "transparent" }]}>
          <View testID={`${testIDPrefix}:${encodeURIComponent(item.id)}`} style={styles.copy}>
            <Text numberOfLines={1} style={[styles.title, { color: theme.colors.text }]}>
              {item.title || "新的开发任务"}
            </Text>
            <View style={styles.status}><StatusDot status={status.dot} />
              <Text style={[styles.statusText, { color: theme.colors.textMuted }]}>{status.label}</Text></View>
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
  item: { minHeight: 58, paddingHorizontal: 8, paddingVertical: 8,
    borderBottomWidth: StyleSheet.hairlineWidth, justifyContent: "center" },
  copy: { flex: 1, minWidth: 0 },
  title: { fontFamily: "Inter_500Medium", fontSize: 15, lineHeight: 20 },
  status: { marginTop: 3, flexDirection: "row", alignItems: "center", gap: 5 },
  statusText: { fontFamily: "Inter_400Regular", fontSize: 11, lineHeight: 15 },
});
