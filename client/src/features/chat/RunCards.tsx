import { useEffect, useMemo, useState } from "react";
import { Pressable, StyleSheet, Text, TextInput, View } from "react-native";

import { Button, Card, Muted, Title } from "@/components/ui";
import { useTheme } from "@/theme/ThemeProvider";
import type { RunSnapshot, TurnRun } from "@/types/protocol";
import { MarkdownContent } from "./MarkdownContent";
import { buildRunActivity, type OperationsPart } from "./runActivity";

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
        <Text style={[styles.activityTitle, { color: theme.colors.text }]}>{presentation.label}</Text>
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
          {run.actualSettings.model ?? "默认模型"} · {run.actualSettings.reasoningEffort ?? "默认"} · {run.actualSettings.serviceTier === "fast" ? "快速" : "标准"}
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

export function PlanCard({ run, onExecute }: { run: RunSnapshot | TurnRun; onExecute: () => void }) {
  if (run.actualSettings.collaborationMode !== "plan" || run.status !== "completed") return null;
  return <View testID={`run:${encodeURIComponent(run.id)}:plan`}><Card style={styles.card}><Title>计划已准备好</Title>
    <Muted>确认后将开始执行这份计划。</Muted>
    <Button testID="plan:execute" title="执行计划" onPress={onExecute} /></Card></View>;
}

export function InteractiveCard({ interactive, onSubmit }: {
  interactive: TurnRun["pendingInteractives"][number];
  onSubmit: (answer: unknown) => void;
}) {
  const theme = useTheme();
  const [answers, setAnswers] = useState<Record<string, string>>({});
  if (interactive.secret) {
    return <View testID={`interactive:${encodeURIComponent(interactive.id)}:secret`}><Card style={styles.card}>
      <Title>需要输入敏感信息</Title><Muted>为保护你的信息，请在 Codex 桌面端完成。</Muted></Card></View>;
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
