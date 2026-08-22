import { router } from "expo-router";
import { useCallback, useDeferredValue, useEffect, useState } from "react";
import { FlatList, Pressable, RefreshControl, StyleSheet, Text, View } from "react-native";

import { listScheduledTasks, type ScheduledTask } from "@/api/automations";
import { Card, EmptyState, Muted, Screen, StatusDot, Title } from "@/components/ui";
import { SegmentedControl } from "@/components/SegmentedControl";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

type StatusFilter = "all" | ScheduledTask["status"];

export default function AutomationsScreen() {
  const theme = useTheme();
  const connection = useAppStore((state) => state.activeConnection);
  const controls = connection?.controls ?? [];
  const [serverId, setServerId] = useState<string>();
  const link = controls.find((item) => item.serverId === serverId) ?? controls[0];
  const [status, setStatus] = useState<StatusFilter>("active");
  const [items, setItems] = useState<ScheduledTask[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const deferredItems = useDeferredValue(items);
  useEffect(() => {
    if (link && serverId !== link.serverId) setServerId(link.serverId);
  }, [link, serverId]);
  const refresh = useCallback(async () => {
    if (!link) return;
    setLoading(true);
    setError(null);
    try {
      const result = await listScheduledTasks(link, status === "all" ? undefined : status);
      setItems(result.items);
    } catch (cause) {
      setItems([]);
      setError(cause instanceof Error ? cause.message : "读取定时任务失败");
    } finally {
      setLoading(false);
    }
  }, [link, status]);
  useEffect(() => { void refresh(); }, [refresh]);

  if (!connection) {
    return <Screen><EmptyState title="还没有机器" detail="请先添加 SSH 或扫码关联一台机器。" /></Screen>;
  }
  if (!link) {
    return <Screen><EmptyState title="尚未授权定时任务"
      detail="请在 Control 后台为当前机器生成二维码，然后在连接页扫码。" /></Screen>;
  }

  return <Screen><View style={styles.header}><View style={styles.headerCopy}>
    <Title>定时任务</Title><Muted>{connection.name} · {link.workerName} · {controlHost(link.baseUrl)}</Muted>
  </View></View>
  {controls.length > 1 ? <View style={styles.controls}>
    <Muted>Control</Muted>
    <SegmentedControl testIDPrefix="automation:control" value={link.serverId}
      options={controls.map((item) => ({ value: item.serverId,
        label: `${item.workerName} · ${controlHost(item.baseUrl)}` }))}
      onChange={setServerId} />
  </View> : null}
  <View style={styles.filters}><SegmentedControl testIDPrefix="automation:status" value={status}
    options={[{ value: "active", label: "进行中" }, { value: "paused", label: "已暂停" },
      { value: "completed", label: "已完成" }, { value: "all", label: "全部" }] as const}
    onChange={(value: StatusFilter) => setStatus(value)} /></View>
  {error ? <View style={[styles.error, { backgroundColor: theme.colors.surface }]}>
    <Text style={{ color: theme.colors.danger }}>{error}</Text>
    <Pressable accessibilityRole="button" onPress={() => void refresh()}>
      <Text style={{ color: theme.colors.accent }}>重试</Text>
    </Pressable></View> : null}
  <FlatList testID="automations:list" data={deferredItems} keyExtractor={(item) => item.id}
    refreshControl={<RefreshControl refreshing={loading} onRefresh={() => void refresh()} />}
    contentContainerStyle={styles.list} ListEmptyComponent={!loading && !error
      ? <EmptyState title="没有定时任务" detail="当前筛选条件下没有可查看的任务。" /> : null}
    renderItem={({ item }) => <Pressable testID={`automation:${item.id}`} onPress={() => router.push({
      pathname: "/automation/[id]", params: { id: item.id, serverId: link.serverId },
    })}><Card style={styles.card}><View style={styles.titleRow}>
      <StatusDot status={taskStatusColor(item.status)} /><View style={styles.copy}>
        <Title>{item.name}</Title><Muted>{kindLabel(item.kind)} · {item.project.name}</Muted>
      </View><Text style={{ color: theme.colors.textMuted, fontSize: 24 }}>›</Text>
    </View><Muted numberOfLines={2}>{item.prompt}</Muted>
    <View style={styles.metadata}><Muted>下次：{formatTime(item.nextRunAt)}</Muted>
      <Muted>上次：{formatTime(item.lastRunAt)}</Muted></View>
    {item.lastErrorMessage ? <Text numberOfLines={2} style={{ color: theme.colors.danger }}>
      {item.lastErrorMessage}</Text> : null}
  </Card></Pressable>} />
  </Screen>;
}

function kindLabel(kind: ScheduledTask["kind"]): string {
  return kind === "heartbeat" ? "当前会话 Heartbeat" : "独立会话";
}

function taskStatusColor(status: ScheduledTask["status"]): "success" | "warning" | "muted" {
  if (status === "active") return "success";
  if (status === "paused") return "warning";
  return "muted";
}

function formatTime(value?: string): string {
  return value ? new Date(value).toLocaleString() : "—";
}

function controlHost(value: string): string {
  try { return new URL(value).host; } catch { return value; }
}

const styles = StyleSheet.create({
  header: { padding: 16, flexDirection: "row", alignItems: "center" },
  headerCopy: { flex: 1, gap: 2 },
  controls: { paddingHorizontal: 12, paddingBottom: 8, gap: 5 },
  filters: { paddingHorizontal: 12, paddingBottom: 6 },
  error: { margin: 12, padding: 12, borderRadius: 10, flexDirection: "row",
    justifyContent: "space-between", gap: 12 },
  list: { padding: 12, gap: 9, flexGrow: 1 },
  card: { gap: 10 },
  titleRow: { flexDirection: "row", alignItems: "center", gap: 9 },
  copy: { flex: 1, minWidth: 0 },
  metadata: { flexDirection: "row", flexWrap: "wrap", justifyContent: "space-between", gap: 8 },
});
