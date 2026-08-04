import { useEffect, useMemo, useState } from "react";
import { Pressable, StyleSheet, Text, TextInput, View } from "react-native";

import { Button, Card, Muted, Title } from "@/components/ui";
import { useTheme } from "@/theme/ThemeProvider";
import type { RunSnapshot } from "@/types/protocol";
import { MarkdownContent } from "./MarkdownContent";

type CommentaryPart = { kind: "commentary"; id: string; text: string };
type Operation = { id: string; label: string; status: "running" | "completed" | "failed" };
type OperationsPart = { kind: "operations"; id: string; operations: Operation[] };
export type RunActivityPart = CommentaryPart | OperationsPart;

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" ? value as Record<string, unknown> : {};
}

function string(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function compact(value: string, max = 64): string {
  const normalized = value.replace(/\s+/g, " ").trim();
  return normalized.length > max ? `${normalized.slice(0, max - 1)}…` : normalized;
}

function operationLabel(type: string, item: Record<string, unknown>, eventType: string): string {
  const status = eventType.endsWith("completed") ? "已" : "正在";
  if (type === "commandExecution") return `${status}运行命令 ${compact(string(item.command) || string(item.cmd))}`.trim();
  if (type === "fileChange") {
    const changes = Array.isArray(item.changes) ? item.changes.map((value) => string(record(value).path)).filter(Boolean) : [];
    return `${status}修改文件${changes.length ? ` ${compact(changes.join("、"))}` : ""}`;
  }
  if (["mcpToolCall", "dynamicToolCall"].includes(type)) {
    const namespace = string(item.server) || string(item.namespace);
    const tool = [namespace, string(item.tool) || string(item.name)].filter(Boolean).join(".");
    return `${status}调用${tool ? ` ${tool}` : "工具"}`;
  }
  if (type === "webSearch") return `${status}搜索 ${compact(string(item.query))}`.trim();
  if (type === "collabAgentToolCall") return `${status}调度子 Agent`;
  if (type) return `${status}处理 ${type}`;
  return compact(eventType);
}

export function buildRunActivity(run: RunSnapshot): RunActivityPart[] {
  const parts: RunActivityPart[] = [];
  const commentaryIndexes = new Map<string, number>();
  const appendCommentary = (id: string, text: string, replace: boolean) => {
    if (!id || !text) return;
    const existing = commentaryIndexes.get(id);
    if (existing !== undefined) {
      const part = parts[existing];
      if (part?.kind === "commentary") part.text = replace ? text.trim() : part.text + text;
      return;
    }
    commentaryIndexes.set(id, parts.length);
    parts.push({ kind: "commentary", id, text: text.trimStart() });
  };
  const appendOperation = (operation: Operation) => {
    for (const part of parts) {
      if (part.kind !== "operations") continue;
      const existing = part.operations.findIndex((item) => item.id === operation.id);
      if (existing >= 0) {
        part.operations[existing] = operation;
        return;
      }
    }
    const previous = parts.at(-1);
    if (previous?.kind === "operations") previous.operations.push(operation);
    else parts.push({ kind: "operations", id: `operations-${operation.id}`, operations: [operation] });
  };

  for (const event of run.timeline) {
    const payload = record(event.payload);
    const item = record(payload.item);
    const itemType = string(item.type);
    const phase = string(item.phase) || string(payload.phase);
    const id = string(item.id) || string(payload.itemId) || `event-${event.sequence}`;
    if (itemType === "agentMessage") {
      if (phase === "commentary") appendCommentary(id, string(item.text), true);
      continue;
    }
    if (event.type === "item/agentMessage/delta" || event.type === "item/delta") {
      if (phase === "commentary" || commentaryIndexes.has(id)) {
        appendCommentary(id, string(payload.delta) || string(payload.text), false);
      }
      continue;
    }
    if (event.type === "item/started" || event.type === "item/completed" ||
      event.type === "discord/tool/started" || event.type === "discord/tool/completed") {
      if (!itemType) continue;
      const failed = string(item.status) === "failed" || record(item.error).message !== undefined;
      appendOperation({ id, label: operationLabel(itemType, item, event.type),
        status: failed ? "failed" : event.type.endsWith("completed") ? "completed" : "running" });
      continue;
    }
    if (event.type !== "runtime.settings_applied") appendOperation({ id,
      label: compact(event.type), status: "completed" });
  }
  return parts.filter((part) => part.kind === "operations" || part.text.trim() !== "");
}

function formatDuration(startedAt: string, finishedAt: string | null): string {
  const elapsed = Math.max(0, new Date(finishedAt ?? Date.now()).getTime() - new Date(startedAt).getTime());
  const totalSeconds = Math.floor(elapsed / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return minutes > 0 ? `${minutes}m ${seconds}s` : `${seconds}s`;
}

function OperationGroup({ part, index }: { part: OperationsPart; index: number }) {
  const theme = useTheme();
  const [expanded, setExpanded] = useState(false);
  const summary = part.operations.length === 1 ? part.operations[0]!.label :
    `${part.operations[0]!.label}等 ${part.operations.length} 项操作`;
  return <View style={styles.operationGroup}>
    <Pressable testID={`run:operations:${index}:toggle`} accessibilityRole="button"
      accessibilityState={{ expanded }} onPress={() => setExpanded((value) => !value)}
      style={styles.operationToggle}>
      <View style={[styles.operationRail, { backgroundColor: theme.colors.border }]} />
      <Text style={[styles.operationIcon, { color: theme.colors.textMuted }]}>↳</Text>
      <Text numberOfLines={1} style={[styles.operationSummary, { color: theme.colors.textMuted }]}>{summary}</Text>
      <Text style={{ color: theme.colors.textMuted }}>{expanded ? "⌃" : "⌄"}</Text>
    </Pressable>
    {expanded && <View style={[styles.operationList, { borderLeftColor: theme.colors.border }]}>
      {part.operations.map((operation) => <View key={operation.id} style={styles.operationRow}>
        <Text style={{ color: operation.status === "failed" ? theme.colors.danger : theme.colors.textMuted }}>
          {operation.status === "running" ? "○" : operation.status === "failed" ? "!" : "✓"}
        </Text>
        <Text style={[styles.operationText, { color: theme.colors.text }]}>{operation.label}</Text>
      </View>)}
    </View>}
  </View>;
}

function runPresentation(run: RunSnapshot, terminal: boolean) {
  if (run.status === "failed") return { icon: "!", label: "失败", color: "danger" as const };
  if (run.status === "canceled") return { icon: "×", label: "已停止", color: "textMuted" as const };
  if (run.status === "waiting_for_user") return { icon: "?", label: "等待回答", color: "warning" as const };
  if (terminal) return { icon: "✓", label: "已完成", color: "success" as const };
  return { icon: "•", label: "正在处理", color: "accent" as const };
}

export function RunProgressCard({ run }: { run: RunSnapshot }) {
  const theme = useTheme();
  const terminal = ["completed", "failed", "canceled"].includes(run.status);
  const [expanded, setExpanded] = useState(!terminal);
  const parts = useMemo(() => buildRunActivity(run), [run]);
  const presentation = runPresentation(run, terminal);
  const statusColor = theme.colors[presentation.color];
  const activityCount = parts.reduce((total, part) => total +
    (part.kind === "operations" ? part.operations.length : 1), 0);
  useEffect(() => setExpanded(!terminal), [run.id, terminal]);
  return <View testID={run.status === "failed" ? "run:error" : `run:${encodeURIComponent(run.id)}:progress`}
    style={[styles.activity, { backgroundColor: theme.colors.surface, borderColor: theme.colors.border }]}>
    <View style={[styles.statusRail, { backgroundColor: statusColor }]} />
    <Pressable testID="run:activity:toggle" accessibilityRole="button"
      accessibilityState={{ expanded }} onPress={() => setExpanded((value) => !value)} style={styles.activityHeader}>
      <View style={styles.activityHeading}>
        <View style={[styles.statusIcon, { backgroundColor: statusColor }]}>
          <Text style={[styles.statusIconText, { color: presentation.color === "accent" && theme.dark ?
            theme.colors.accentForeground : "#ffffff" }]}>{presentation.icon}</Text>
        </View>
        <Text style={[styles.activityTitle, { color: theme.colors.text }]}>中间过程 · {presentation.label}</Text>
      </View>
      <Text style={[styles.toggleIcon, { color: theme.colors.textMuted }]}>{expanded ? "⌃" : "⌄"}</Text>
    </Pressable>
    {expanded && <View style={styles.activityBody}>
      <View style={[styles.runMeta, { borderTopColor: theme.colors.border,
        borderBottomColor: theme.colors.border }]}>
        <Text style={[styles.duration, { color: theme.colors.textMuted }]}>
          {formatDuration(run.startedAt, run.finishedAt)} · {activityCount} 项动态
        </Text>
        <Text testID="run:actual-settings" numberOfLines={1}
          style={[styles.settings, { color: theme.colors.textMuted }]}>
          {run.actualSettings.model ?? "默认模型"} · {run.actualSettings.reasoningEffort ?? "默认"} · {run.actualSettings.serviceTier ?? "standard"}
        </Text>
      </View>
      {parts.map((part, index) => part.kind === "commentary" ? <View key={part.id}
        style={[styles.commentary, index > 0 && { borderTopColor: theme.colors.border,
          borderTopWidth: StyleSheet.hairlineWidth }]}>
        <MarkdownContent compact>{part.text}</MarkdownContent>
      </View> : <OperationGroup key={part.id} part={part} index={index} />)}
      {run.errorMessage && <Text testID="run:error:message"
        style={[styles.error, { color: theme.colors.danger }]}>{run.errorMessage}</Text>}
    </View>}
  </View>;
}

export function PlanCard({ run, onExecute }: { run: RunSnapshot; onExecute: () => void }) {
  if (run.actualSettings.collaborationMode !== "plan" || run.status !== "completed") return null;
  return <View testID={`run:${encodeURIComponent(run.id)}:plan`}><Card style={styles.card}><Title>Plan 已准备好</Title>
    <Muted>确认后会切换到 Default 模式，并以一条新的 Turn 执行这份 Plan。</Muted>
    <Button testID="plan:execute" title="执行 Plan" onPress={onExecute} /></Card></View>;
}

export function InteractiveCard({ interactive, onSubmit }: {
  interactive: RunSnapshot["pendingInteractives"][number];
  onSubmit: (answer: unknown) => void;
}) {
  const theme = useTheme();
  const [answers, setAnswers] = useState<Record<string, string>>({});
  if (interactive.secret) {
    return <View testID={`interactive:${encodeURIComponent(interactive.id)}:secret`}><Card style={styles.card}>
      <Title>需要 Secret 输入</Title><Muted>为保护敏感信息，这个问题只能在 Codex Desktop 完成。</Muted></Card></View>;
  }
  return <View testID={`interactive:${encodeURIComponent(interactive.id)}`}><Card style={styles.card}><Title>需要你的回答</Title>
    {interactive.questions.map((question) => <View key={question.id} style={styles.question}>
      <Text style={[styles.questionHeader, { color: theme.colors.textMuted }]}>{question.header}</Text>
      <Text style={[styles.questionText, { color: theme.colors.text }]}>{question.question}</Text>
      {question.options?.length ? <View style={styles.options}>{question.options.map((option, index) => <Pressable
        key={option.label} testID={`interactive:option:${index}`}
        onPress={() => setAnswers((value) => ({ ...value, [question.id]: option.label }))}
        style={[styles.option, { borderColor: answers[question.id] === option.label ? theme.colors.accent : theme.colors.border,
          backgroundColor: theme.colors.surfaceAlt }]}><Text style={{ color: theme.colors.text,
          fontFamily: "Inter_500Medium" }}>{option.label}</Text><Muted>{option.description}</Muted></Pressable>)}</View> :
        <TextInput testID={`interactive:input:${encodeURIComponent(question.id)}`}
          value={answers[question.id] ?? ""}
          onChangeText={(value) => setAnswers((current) => ({ ...current, [question.id]: value }))}
          style={[styles.input, { borderColor: theme.colors.border, color: theme.colors.text }]} />}
    </View>)}
    <Button title="提交回答" disabled={interactive.questions.some((question) => !answers[question.id]?.trim())}
      testID={`interactive:${encodeURIComponent(interactive.id)}:submit`} onPress={() => onSubmit({ answers })} />
  </Card></View>;
}

const styles = StyleSheet.create({
  activity: { marginHorizontal: 12, marginVertical: 8, borderWidth: StyleSheet.hairlineWidth,
    borderRadius: 12, overflow: "hidden" },
  statusRail: { position: "absolute", top: 0, bottom: 0, left: 0, width: 4 },
  activityHeader: { minHeight: 50, paddingLeft: 16, paddingRight: 12, flexDirection: "row",
    alignItems: "center", justifyContent: "space-between" },
  activityHeading: { flex: 1, minWidth: 0, flexDirection: "row", alignItems: "center", gap: 8 },
  statusIcon: { width: 20, height: 20, borderRadius: 5, alignItems: "center", justifyContent: "center" },
  statusIconText: { fontFamily: "Inter_600SemiBold", fontSize: 13, lineHeight: 17 },
  activityTitle: { flex: 1, minWidth: 0, fontFamily: "Inter_600SemiBold", fontSize: 15, lineHeight: 21 },
  toggleIcon: { width: 24, textAlign: "right", fontSize: 16 },
  duration: { fontFamily: "Inter_400Regular", fontSize: 12, lineHeight: 18 },
  activityBody: { paddingLeft: 16, paddingRight: 12, paddingBottom: 12 },
  runMeta: { paddingVertical: 9, borderTopWidth: StyleSheet.hairlineWidth,
    borderBottomWidth: StyleSheet.hairlineWidth, gap: 2 },
  settings: { fontFamily: "Inter_400Regular", fontSize: 11, lineHeight: 16 },
  commentary: { paddingVertical: 10 },
  operationGroup: { paddingVertical: 4 },
  operationToggle: { minHeight: 38, paddingRight: 2, flexDirection: "row", alignItems: "center", gap: 7 },
  operationRail: { width: 2, alignSelf: "stretch", marginVertical: 7, borderRadius: 1 },
  operationIcon: { fontSize: 15 },
  operationSummary: { flex: 1, minWidth: 0, fontFamily: "Inter_400Regular", fontSize: 14 },
  operationList: { marginLeft: 18, paddingLeft: 12, paddingVertical: 8, borderLeftWidth: 1, gap: 8 },
  operationRow: { flexDirection: "row", gap: 8, alignItems: "flex-start" },
  operationText: { flex: 1, fontFamily: "Inter_400Regular", fontSize: 13, lineHeight: 19 },
  error: { marginTop: 8, fontFamily: "Inter_400Regular", fontSize: 14, lineHeight: 21 },
  card: { marginHorizontal: 12, marginVertical: 6, gap: 10 },
  question: { gap: 6 }, questionHeader: { fontFamily: "Inter_600SemiBold", fontSize: 12 },
  questionText: { fontFamily: "Inter_400Regular", fontSize: 15 }, options: { gap: 6 },
  option: { borderWidth: 1, borderRadius: 8, padding: 10 },
  input: { borderWidth: StyleSheet.hairlineWidth, borderRadius: 8, minHeight: 44, paddingHorizontal: 10 },
});
