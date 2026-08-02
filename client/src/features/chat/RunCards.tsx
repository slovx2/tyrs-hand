import { useState } from "react";
import { Pressable, StyleSheet, Text, TextInput, View } from "react-native";

import { Button, Card, Muted, Title } from "@/components/ui";
import { useTheme } from "@/theme/ThemeProvider";
import type { RunSnapshot } from "@/types/protocol";

export function RunProgressCard({ run }: { run: RunSnapshot }) {
  const theme = useTheme();
  return <View testID={`run:${encodeURIComponent(run.id)}:progress`}><Card style={styles.card}>
    <View style={styles.row}><Title>运行进度</Title><Text style={{ color: theme.colors.textMuted }}>{run.status}</Text></View>
    <Text testID="run:actual-settings" style={{ color: theme.colors.textMuted }}>
      {run.actualSettings.model ?? "默认模型"} · {run.actualSettings.reasoningEffort ?? "默认"} · {run.actualSettings.serviceTier ?? "standard"} · {run.actualSettings.collaborationMode}
    </Text>
    {run.timeline.slice(-6).map((event) => <View key={event.sequence} style={styles.timeline}>
      <View style={[styles.lineDot, { backgroundColor: theme.colors.accent }]} />
      <View style={{ flex: 1 }}><Text style={{ color: theme.colors.text }}>{event.type}</Text>
        <Muted>{new Date(event.occurredAt).toLocaleTimeString("zh-CN")}</Muted></View>
    </View>)}
    {run.errorMessage && <Text testID="run:error" style={{ color: theme.colors.danger }}>{run.errorMessage}</Text>}
  </Card></View>;
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
      <Title>需要 Secret 输入</Title>
      <Muted>为保护敏感信息，这个问题只能在 Codex Desktop 完成。</Muted></Card></View>;
  }
  return <View testID={`interactive:${encodeURIComponent(interactive.id)}`}><Card style={styles.card}><Title>需要你的回答</Title>
    {interactive.questions.map((question) => <View key={question.id} style={styles.question}>
      <Text style={[styles.header, { color: theme.colors.textMuted }]}>{question.header}</Text>
      <Text style={[styles.questionText, { color: theme.colors.text }]}>{question.question}</Text>
      {question.options && question.options.length > 0 ? <View style={styles.options}>
        {question.options.map((option, index) => <Pressable key={option.label}
          testID={`interactive:option:${index}`}
          onPress={() => setAnswers((value) => ({ ...value, [question.id]: option.label }))}
          style={[styles.option, { borderColor: answers[question.id] === option.label ?
            theme.colors.accent : theme.colors.border, backgroundColor: theme.colors.surfaceAlt }]}>
          <Text style={{ color: theme.colors.text, fontFamily: "Inter_500Medium" }}>{option.label}</Text>
          <Muted>{option.description}</Muted>
        </Pressable>)}</View> : <TextInput value={answers[question.id] ?? ""}
          onChangeText={(value) => setAnswers((current) => ({ ...current, [question.id]: value }))}
          style={[styles.input, { borderColor: theme.colors.border, color: theme.colors.text }]} />}
    </View>)}
    <Button title="提交回答" disabled={interactive.questions.some((question) => !answers[question.id]?.trim())}
      testID={`interactive:${encodeURIComponent(interactive.id)}:submit`}
      onPress={() => onSubmit({ answers })} />
  </Card></View>;
}

const styles = StyleSheet.create({
  card: { marginHorizontal: 12, marginVertical: 6, gap: 10 },
  row: { flexDirection: "row", justifyContent: "space-between", alignItems: "center" },
  timeline: { flexDirection: "row", alignItems: "center", gap: 9 }, lineDot: { width: 7, height: 7, borderRadius: 999 },
  question: { gap: 6 }, header: { fontFamily: "Inter_600SemiBold", fontSize: 12 },
  questionText: { fontFamily: "Inter_400Regular", fontSize: 15 }, options: { gap: 6 },
  option: { borderWidth: 1, borderRadius: 8, padding: 10 },
  input: { borderWidth: StyleSheet.hairlineWidth, borderRadius: 8, minHeight: 44, paddingHorizontal: 10 },
});
