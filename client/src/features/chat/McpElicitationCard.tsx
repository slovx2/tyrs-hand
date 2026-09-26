import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { JsonValue } from "@codex-app-server/serde_json/JsonValue";
import { useMemo, useState } from "react";
import { Linking, StyleSheet, Text, TextInput, View } from "react-native";

import { Button, Card, Muted, Title } from "@/components/ui";
import { useTheme } from "@/theme/ThemeProvider";

import { collectMcpValues, initialMcpValues, mcpUrl, parseMcpJson, prepareMcpForm, type McpField } from "./mcpElicitation";

type Request = Extract<ServerRequest, { method: "mcpServer/elicitation/request" }>;
type Props = { request: Request; onAnswer: (result: unknown) => void };
type FormParams = Exclude<Request["params"], { mode: "url" }>;

export function McpElicitationCard({ request, onAnswer }: Props) {
  const { params } = request;
  const [error, setError] = useState<string | null>(null);
  const url = params.mode === "url" ? mcpUrl(params.url) : null;
  const answer = (action: "accept" | "decline" | "cancel", content: JsonValue | null = null) =>
    onAnswer({ action, content, _meta: null });
  return <Card testID={`interactive:${String(request.id)}`} style={styles.card}>
    <Title>MCP 需要你的确认</Title>
    <Muted selectable>服务：{params.serverName}</Muted>
    <Muted selectable>{params.message}</Muted>
    {params.mode === "url" ? <>
      <Muted selectable>{url ?? "链接无效或不安全，无法继续。"}</Muted>
      <Muted>打开链接并完成页面操作后，返回确认继续。</Muted>
      <Button title="打开链接" variant="secondary" disabled={!url}
        testID={`interactive:${String(request.id)}:open-url`} onPress={() => {
          if (url) void Linking.openURL(url).catch(() => setError("无法打开链接，请稍后重试。"));
        }} />
      {error && <Muted>{error}</Muted>}
      <Button title="已完成，继续" disabled={!url}
        testID={`interactive:${String(request.id)}:accept`} onPress={() => answer("accept")} />
    </> : <McpForm params={params} id={String(request.id)} onAccept={(value) => answer("accept", value)} />}
    <View style={styles.actions}>
      <Button title="拒绝" variant="secondary" testID={`interactive:${String(request.id)}:decline`}
        onPress={() => answer("decline")} />
      <Button title="取消请求" variant="secondary" testID={`interactive:${String(request.id)}:cancel`}
        onPress={() => answer("cancel")} />
    </View>
  </Card>;
}

function McpForm({ params, id, onAccept }: { params: FormParams; id: string;
  onAccept: (value: JsonValue) => void }) {
  const theme = useTheme();
  const form = useMemo(() => prepareMcpForm(params.requestedSchema), [params.requestedSchema]);
  const [values, setValues] = useState(() => initialMcpValues(form.fields));
  const [texts, setTexts] = useState<Record<string, string>>({});
  const [json, setJson] = useState(() => JSON.stringify(initialMcpValues(form.fields), null, 2));
  const [manualJson, setManualJson] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showSchema, setShowSchema] = useState(false);
  const useJson = form.jsonMode || manualJson;
  const inputStyle = [styles.input, { color: theme.colors.text, borderColor: theme.colors.border }];
  const setValue = (name: string, value: JsonValue | undefined) => {
    setError(null);
    setValues((current) => {
      const next = { ...current };
      if (value === undefined) delete next[name];
      else Object.defineProperty(next, name, { value, enumerable: true, configurable: true, writable: true });
      return next;
    });
  };
  const submit = () => {
    let content: JsonValue;
    if (useJson) {
      try { content = parseMcpJson(json); }
      catch { setError("请输入有效的 JSON。"); return; }
    } else {
      const result = collectMcpValues(form.fields, values, texts);
      if (!result.valid) { setError(result.message); return; }
      content = result.value;
    }
    const result = form.validate(content);
    if (!result.valid) { setError(result.message); return; }
    setError(null);
    onAccept(content);
  };
  const choices = (field: McpField) => field.kind === "boolean"
    ? [{ label: "是", value: true }, { label: "否", value: false }] : field.options;
  return <View style={styles.form}>
    {form.error ? <Muted>{form.error}</Muted> : useJson ? <>
      <Muted>请按表单结构填写 JSON，提交前会校验所有约束。</Muted>
      <TextInput testID={`interactive:${id}:json`} accessibilityLabel="表单 JSON" multiline
        autoCapitalize="none" autoCorrect={false} value={json} onChangeText={(value) => {
          setJson(value); setError(null);
        }} style={[inputStyle, styles.json]} />
    </> : form.fields.map((field) => <View key={field.name} style={styles.field}>
      <Text style={{ color: theme.colors.text }}>{field.title}{field.required ? "（必填）" : "（选填）"}</Text>
      {Boolean(field.description) && <Muted>{field.description}</Muted>}
      {["boolean", "enum", "multiEnum"].includes(field.kind) ? <>
        <View style={styles.actions}>
          {choices(field).map((choice, index) => {
            const selected = field.kind === "multiEnum" ? Array.isArray(values[field.name]) &&
              (values[field.name] as JsonValue[]).some((value) => JSON.stringify(value) === JSON.stringify(choice.value))
              : Object.hasOwn(values, field.name) && JSON.stringify(values[field.name]) === JSON.stringify(choice.value);
            return <Button key={index} title={`${selected ? "✓ " : ""}${choice.label}`}
              testID={`interactive:${id}:field:${field.name}:option:${index}`}
              variant={selected ? "primary" : "secondary"} onPress={() => {
                if (field.kind !== "multiEnum") { setValue(field.name, choice.value); return; }
                const current = Array.isArray(values[field.name]) ? values[field.name] as JsonValue[] : [];
                setValue(field.name, selected ? current.filter((value) =>
                  JSON.stringify(value) !== JSON.stringify(choice.value)) : [...current, choice.value]);
              }} />;
          })}
        </View>
        {!field.required && <Button title="不填写" variant="secondary"
          testID={`interactive:${id}:field:${field.name}:clear`} onPress={() => setValue(field.name, undefined)} />}
      </> : <TextInput testID={`interactive:${id}:field:${field.name}`} accessibilityLabel={field.title}
        multiline={field.kind === "json"} autoCapitalize="none" autoCorrect={false}
        value={(Object.hasOwn(texts, field.name) ? texts[field.name] : undefined) ??
          (!Object.hasOwn(values, field.name) ? "" : field.kind === "string"
          ? String(values[field.name]) : JSON.stringify(values[field.name]))}
        onChangeText={(value) => { setTexts((current) => ({ ...current, [field.name]: value })); setError(null); }}
        style={[inputStyle, field.kind === "json" && styles.json]} />}
    </View>)}
    {!useJson && !form.error && <Button title="使用完整 JSON 编辑" variant="secondary"
      testID={`interactive:${id}:edit-json`} onPress={() => {
        const result = collectMcpValues(form.fields, values, texts);
        if (!result.valid) { setError(result.message); return; }
        setJson(JSON.stringify(result.value, null, 2));
        setManualJson(true); setError(null);
      }} />}
    <Button title={showSchema ? "收起表单结构" : "查看表单结构"} variant="secondary"
      onPress={() => setShowSchema((current) => !current)} />
    {showSchema && <Muted selectable>{form.schemaText}</Muted>}
    {error && <Text accessibilityRole="alert" testID={`interactive:${id}:error`}
      style={{ color: theme.colors.danger }}>{error}</Text>}
    <Button title="提交并继续" disabled={Boolean(form.error)} testID={`interactive:${id}:accept`} onPress={submit} />
  </View>;
}

const styles = StyleSheet.create({
  card: { marginHorizontal: 12, marginVertical: 6, padding: 13, gap: 10 },
  form: { gap: 12 }, field: { gap: 7 },
  actions: { flexDirection: "row", flexWrap: "wrap", gap: 8 },
  input: { minHeight: 44, borderWidth: StyleSheet.hairlineWidth, borderRadius: 7,
    paddingHorizontal: 11, fontFamily: "Inter_400Regular" },
  json: { minHeight: 100, textAlignVertical: "top" },
});
