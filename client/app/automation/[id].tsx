import { router, useLocalSearchParams } from "expo-router";
import { useEffect, useState } from "react";
import { ScrollView, StyleSheet, Text, View } from "react-native";

import { getScheduledTask, listScheduledTaskRuns, type ScheduledTask,
  type ScheduledTaskRun } from "@/api/automations";
import { Button, Card, EmptyState, Muted, Screen, StatusDot, Title } from "@/components/ui";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

export default function AutomationDetailScreen() {
  const theme = useTheme();
  const { id, serverId } = useLocalSearchParams<{ id: string; serverId?: string }>();
  const connection = useAppStore((state) => state.activeConnection);
  const link = connection?.controls.find((item) => item.serverId === serverId) ??
    connection?.controls[0];
  const [task, setTask] = useState<ScheduledTask>();
  const [runs, setRuns] = useState<ScheduledTaskRun[]>([]);
  const [nextCursor, setNextCursor] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!id || !link) {
      setLoading(false);
      return;
    }
    let active = true;
    void Promise.all([getScheduledTask(link, id), listScheduledTaskRuns(link, id)])
      .then(([nextTask, page]) => {
        if (!active) return;
        setTask(nextTask);
        setRuns(page.items);
        setNextCursor(page.nextCursor);
      })
      .catch((cause) => active && setError(cause instanceof Error ? cause.message : "读取详情失败"))
      .finally(() => active && setLoading(false));
    return () => { active = false; };
  }, [id, link]);

  if (!link) {
    return <Screen><EmptyState title="没有定时任务授权" detail="请返回连接页扫码关联机器。" /></Screen>;
  }
  if (loading) return <Screen><EmptyState title="正在读取任务" detail="正在连接 Control…" /></Screen>;
  if (error || !task) {
    return <Screen><EmptyState title="无法读取任务" detail={error ?? "任务不存在"} /></Screen>;
  }
  const loadMore = async () => {
    if (!nextCursor || loadingMore) return;
    setLoadingMore(true);
    try {
      const page = await listScheduledTaskRuns(link, task.id, nextCursor);
      setRuns((current) => [...current, ...page.items]);
      setNextCursor(page.nextCursor);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "读取更多运行记录失败");
    } finally {
      setLoadingMore(false);
    }
  };

  return <Screen><ScrollView contentContainerStyle={styles.content}>
    <Card style={styles.section}><View style={styles.heading}>
      <StatusDot status={task.status === "active" ? "success" : task.status === "paused"
        ? "warning" : "muted"} /><View style={styles.grow}><Title>{task.name}</Title>
        <Muted>{statusLabel(task.status)} · {task.kind === "heartbeat" ? "Heartbeat" : "独立会话"}</Muted>
      </View></View>
      <Detail label="项目" value={`${task.project.name} · ${task.project.relativePath}`} />
      <Detail label="时区" value={task.timezone} />
      <Detail label="下次运行" value={formatTime(task.nextRunAt)} />
      <Detail label="上次运行" value={formatTime(task.lastRunAt)} />
      {task.targetSession ? <Detail label="目标会话" value={task.targetSession.title} /> : null}
      {task.standaloneSettings ? <Detail label="运行设置" value={[
        task.standaloneSettings.model, task.standaloneSettings.reasoningEffort,
        task.standaloneSettings.serviceTier].filter(Boolean).join(" · ") || "默认"} /> : null}
      {task.lastErrorMessage ? <View style={styles.errorBox}>
        <Text style={{ color: theme.colors.danger }}>{task.lastErrorMessage}</Text></View> : null}
    </Card>

    <Card style={styles.section}><Title>任务指令</Title>
      <Text selectable style={[styles.body, { color: theme.colors.text }]}>{task.prompt}</Text>
    </Card>
    <Card style={styles.section}><Title>日程（RFC 5545）</Title>
      <Text testID="automation:schedule" selectable style={[styles.code, { color: theme.colors.text,
        backgroundColor: theme.colors.surfaceAlt }]}>{task.schedule}</Text>
    </Card>

    <View style={styles.runHeader}><Title>运行记录</Title><Muted>{runs.length} 条已加载</Muted></View>
    {runs.length === 0 ? <Card><Muted>还没有运行记录。</Muted></Card> : runs.map((run) =>
      <Card style={styles.run} key={run.id}><View style={styles.heading}>
        <StatusDot status={runColor(run.status)} /><View style={styles.grow}>
          <Title>{runStatusLabel(run.status)}</Title>
          <Muted>{run.trigger === "run_now" ? "手动触发" : "按计划触发"} · {formatTime(run.scheduledFor)}</Muted>
        </View></View>
        {run.coalescedThrough ? <Muted>已合并至 {formatTime(run.coalescedThrough)}</Muted> : null}
        {run.session ? <View style={styles.sessionRow}><View style={styles.grow}>
          <Muted>会话：{run.session.title}</Muted></View>
          {connection?.kind === "ssh" && run.session.externalThreadId
            ? <Button title="在会话中查看" variant="secondary" onPress={() => router.push({
              pathname: "/session/[id]", params: { id: run.session!.externalThreadId! },
            })} /> : null}</View> : null}
        {run.errorMessage ? <Text style={{ color: theme.colors.danger }}>{run.errorMessage}</Text> : null}
      </Card>)}
    {error ? <Text style={{ color: theme.colors.danger }}>{error}</Text> : null}
    {nextCursor ? <Button title="加载更多" variant="secondary" loading={loadingMore}
      onPress={() => void loadMore()} /> : null}
  </ScrollView></Screen>;
}

function Detail({ label, value }: { label: string; value: string }) {
  const theme = useTheme();
  return <View style={styles.detail}><Muted>{label}</Muted>
    <Text style={[styles.detailValue, { color: theme.colors.text }]}>{value}</Text></View>;
}

function formatTime(value?: string): string { return value ? new Date(value).toLocaleString() : "—"; }
function statusLabel(status: ScheduledTask["status"]): string {
  return { active: "运行中", paused: "已暂停", completed: "已完成", deleted: "已删除" }[status];
}
function runStatusLabel(status: ScheduledTaskRun["status"]): string {
  return { queued: "等待执行", running: "正在运行", waiting_for_user: "等待用户",
    succeeded: "运行成功", failed: "运行失败", canceled: "已取消" }[status];
}
function runColor(status: ScheduledTaskRun["status"]): "success" | "warning" | "danger" | "muted" {
  if (status === "succeeded") return "success";
  if (status === "failed" || status === "canceled") return "danger";
  if (status === "running" || status === "waiting_for_user") return "warning";
  return "muted";
}

const styles = StyleSheet.create({
  content: { padding: 14, gap: 12, paddingBottom: 32 },
  section: { gap: 12 },
  heading: { flexDirection: "row", alignItems: "center", gap: 9 },
  grow: { flex: 1, minWidth: 0 },
  detail: { flexDirection: "row", justifyContent: "space-between", gap: 16 },
  detailValue: { flex: 1, textAlign: "right", fontFamily: "Inter_500Medium" },
  body: { fontFamily: "Inter_400Regular", fontSize: 14, lineHeight: 22 },
  code: { fontFamily: "monospace", fontSize: 12, lineHeight: 18, padding: 10, borderRadius: 8 },
  errorBox: { padding: 10, borderRadius: 8 },
  runHeader: { flexDirection: "row", alignItems: "center", justifyContent: "space-between" },
  run: { gap: 9 },
  sessionRow: { flexDirection: "row", alignItems: "center", gap: 8 },
});
