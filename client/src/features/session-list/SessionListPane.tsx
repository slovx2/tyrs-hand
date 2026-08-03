import { useMemo, useState } from "react";
import { FlatList, Pressable, StyleSheet, View } from "react-native";

import { SegmentedControl } from "@/components/SegmentedControl";
import { Card, EmptyState, Muted, StatusDot, Title } from "@/components/ui";
import { useTheme } from "@/theme/ThemeProvider";
import type { Session } from "@/types/protocol";

type Filter = "active" | "archived";

export function SessionListPane({ sessions, selectedId, onSelect, emptyDetail, testIDPrefix = "session" }: {
  sessions: Session[];
  selectedId?: string | null;
  onSelect: (id: string) => void;
  emptyDetail?: string;
  testIDPrefix?: string;
}) {
  const theme = useTheme();
  const [filter, setFilter] = useState<Filter>("active");
  const filtered = useMemo(() => sessions.filter((session) => filter === "archived" ?
    session.lifecycleState === "archived" : session.lifecycleState !== "archived"), [filter, sessions]);

  return <View style={styles.container}>
    <View style={styles.filter}><SegmentedControl testIDPrefix={`${testIDPrefix}s:filter`} value={filter} options={[
      { value: "active", label: "进行中" }, { value: "archived", label: "已归档" },
    ] as const} onChange={setFilter} /></View>
    <FlatList testID={`${testIDPrefix}s:list`} data={filtered} keyExtractor={(item) => item.id}
      contentContainerStyle={[styles.list, filtered.length === 0 && styles.emptyList]}
      ListEmptyComponent={<EmptyState title="没有会话"
        detail={emptyDetail ?? "从项目页进入对应项目后创建第一个任务。"} />}
      renderItem={({ item, index }) => <Pressable testID={`${testIDPrefix}:row:${index}`}
        onPress={() => onSelect(item.id)}>
        <Card testID={`${testIDPrefix}:${encodeURIComponent(item.id)}`}
          style={[styles.item, selectedId === item.id && { borderColor: theme.colors.accent }]}>
          <View style={styles.row}><StatusDot status={item.lifecycleState === "active" ? "muted" : "warning"} />
            <View style={styles.copy}><Title>{item.title || "新的开发任务"}</Title></View></View>
          <Muted>{item.collaborationMode} · {item.serviceTier} · {new Date(item.lastActivityAt).toLocaleString("zh-CN")}</Muted>
        </Card>
      </Pressable>} />
  </View>;
}

const styles = StyleSheet.create({
  container: { flex: 1 },
  filter: { padding: 12 },
  list: { padding: 12, paddingTop: 0, gap: 8 },
  emptyList: { flexGrow: 1 },
  item: { gap: 6 },
  row: { flexDirection: "row", alignItems: "center", gap: 8 },
  copy: { flex: 1, minWidth: 0 },
});
