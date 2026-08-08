import type { ServerRequest } from "@codex-app-server/ServerRequest";
import { useMemo, useState } from "react";
import { Pressable, StyleSheet, Text, TextInput, View } from "react-native";

import { Button, Card, Muted, Title } from "@/components/ui";
import { useTheme } from "@/theme/ThemeProvider";

export function ServerRequestCard({ request, onAnswer }: {
  request: ServerRequest;
  onAnswer: (result: unknown) => void;
}) {
  if (request.method === "item/tool/requestUserInput") {
    return <QuestionRequest request={request} onAnswer={onAnswer} />;
  }
  const approval = request.method === "item/commandExecution/requestApproval" ||
    request.method === "item/fileChange/requestApproval";
  if (approval) {
    const title = request.method === "item/commandExecution/requestApproval"
      ? request.params.command ?? request.params.reason ?? "Codex 请求执行命令"
      : request.params.reason ?? "Codex 请求修改文件";
    return <Card testID={`interactive:${String(request.id)}`} style={styles.card}>
      <Title>需要确认</Title><Muted selectable>{title}</Muted>
      <View style={styles.actions}>
        <Button testID={`interactive:${String(request.id)}:decline`} title="拒绝"
          variant="secondary" onPress={() => onAnswer({ decision: "decline" })} />
        <Button testID={`interactive:${String(request.id)}:accept`} title="允许"
          onPress={() => onAnswer({ decision: "accept" })} />
      </View>
    </Card>;
  }
  if (request.method === "item/permissions/requestApproval") {
    const permissions = {
      ...(request.params.permissions.network ? { network: request.params.permissions.network } : {}),
      ...(request.params.permissions.fileSystem ? { fileSystem: request.params.permissions.fileSystem } : {}),
    };
    return <Card testID={`interactive:${String(request.id)}`} style={styles.card}>
      <Title>额外权限</Title>
      <Muted>{request.params.reason ?? "Codex 请求扩大本轮权限"}</Muted>
      <View style={styles.actions}>
        <Button testID={`interactive:${String(request.id)}:decline`} title="拒绝" variant="secondary"
          onPress={() => onAnswer({ permissions: {}, scope: "turn" })} />
        <Button testID={`interactive:${String(request.id)}:accept`} title="本轮允许"
          onPress={() => onAnswer({ permissions, scope: "turn" })} />
      </View>
    </Card>;
  }
  return <Card testID={`interactive:${String(request.id)}`} style={styles.card}>
    <Title>等待客户端处理</Title>
    <Muted>{request.method}</Muted>
    <Button testID={`interactive:${String(request.id)}:cancel`} title="取消请求"
      variant="secondary" onPress={() => onAnswer({ action: "cancel",
      content: null, _meta: null })} />
  </Card>;
}

function QuestionRequest({ request, onAnswer }: {
  request: Extract<ServerRequest, { method: "item/tool/requestUserInput" }>;
  onAnswer: (result: unknown) => void;
}) {
  const theme = useTheme();
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const complete = useMemo(() => request.params.questions.every((question) =>
    Boolean(answers[question.id]?.trim())), [answers, request.params.questions]);
  return <Card testID={`interactive:${String(request.id)}`} style={styles.card}>
    <Title>Codex 需要你的回答</Title>
    {request.params.questions.map((question) => <View key={question.id} style={styles.question}>
      <Muted>{question.header}</Muted>
      <Text style={{ color: theme.colors.text, fontFamily: "Inter_500Medium" }}>{question.question}</Text>
      {question.options?.map((option, index) => <Pressable key={option.label}
        testID={`interactive:option:${index}`}
        onPress={() => setAnswers((current) => ({ ...current, [question.id]: option.label }))}
        style={[styles.option, { borderColor: theme.colors.border,
          backgroundColor: answers[question.id] === option.label
            ? theme.colors.surfaceAlt : theme.colors.surface }]}>
        <Text style={{ color: theme.colors.text }}>{option.label}</Text>
        <Muted>{option.description}</Muted>
      </Pressable>)}
      {(question.isOther || !question.options) && <TextInput
        testID={question.isSecret
          ? `interactive:${String(request.id)}:secret`
          : `interactive:input:${question.id}`}
        secureTextEntry={question.isSecret}
        value={answers[question.id] ?? ""}
        onChangeText={(value) => setAnswers((current) => ({ ...current, [question.id]: value }))}
        style={[styles.input, { borderColor: theme.colors.border, color: theme.colors.text }]} />}
    </View>)}
    <Button testID={`interactive:${String(request.id)}:submit`} title="提交回答"
      disabled={!complete} onPress={() => onAnswer({
      answers: Object.fromEntries(Object.entries(answers).map(([id, answer]) =>
        [id, { answers: [answer] }])),
    })} />
  </Card>;
}

const styles = StyleSheet.create({
  card: { marginHorizontal: 12, marginVertical: 6, padding: 13, gap: 10 },
  actions: { flexDirection: "row", justifyContent: "flex-end", gap: 8 },
  question: { gap: 7 },
  option: { minHeight: 44, borderWidth: StyleSheet.hairlineWidth, borderRadius: 7,
    paddingHorizontal: 11, paddingVertical: 8, gap: 2 },
  input: { minHeight: 44, borderWidth: StyleSheet.hairlineWidth, borderRadius: 7,
    paddingHorizontal: 11, fontFamily: "Inter_400Regular" },
});
