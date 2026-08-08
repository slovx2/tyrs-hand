import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";
import { memo, useState } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";

import { Muted } from "@/components/ui";
import { useTheme } from "@/theme/ThemeProvider";
import { activityDetailPreview } from "./activityDetail";
import { MarkdownContent } from "./MarkdownContent";

export const OfficialThreadItem = memo(function OfficialThreadItem({ item }: { item: ThreadItem }) {
  const theme = useTheme();
  if (item.type === "userMessage") {
    const text = item.content.filter((input) => input.type === "text")
      .map((input) => input.type === "text" ? input.text : "").join("\n");
    const files = item.content.filter((input) => input.type === "localImage" || input.type === "mention");
    return <View testID="message:role:user" style={styles.userRow}>
      <View testID={`message:${encodeURIComponent(item.clientId ?? item.id)}`}
        style={[styles.userBubble, { backgroundColor: theme.colors.accent }]}>
        {text ? <Text selectable style={[styles.userText,
          { color: theme.colors.accentForeground }]}>{text}</Text> : null}
        {files.map((input, index) => <Text key={`${input.type}:${index}`} numberOfLines={1}
          style={[styles.file, { color: theme.colors.accentForeground }]}>
          {input.type === "mention" ? input.name : input.path.split("/").at(-1)}
        </Text>)}
      </View>
    </View>;
  }
  if (item.type === "agentMessage") {
    return <View testID="message:role:agent" style={styles.agentRow}>
      <MarkdownContent cacheKey={`agentMessage:${item.id}`}>{item.text}</MarkdownContent>
    </View>;
  }
  if (item.type === "plan") {
    return <View testID={`plan:${item.id}`} style={[styles.activity,
      { borderColor: theme.colors.border }]}>
      <Muted>计划</Muted>
      <MarkdownContent cacheKey={`plan:${item.id}`}>{item.text}</MarkdownContent>
    </View>;
  }
  if (item.type === "reasoning") {
    const summary = item.summary.join("\n");
    return summary ? <View testID={`item:${item.type}:${encodeURIComponent(item.id)}`}
      style={styles.reasoning}><Muted selectable>{summary}</Muted></View> : null;
  }
  if (item.type === "commandExecution") {
    return <Activity testID={`item:${item.type}:${encodeURIComponent(item.id)}`}
      title={item.command} status={item.status}
      detail={item.aggregatedOutput ?? undefined} />;
  }
  if (item.type === "fileChange") {
    return <Activity testID={`item:${item.type}:${encodeURIComponent(item.id)}`}
      title={`修改 ${item.changes.length} 个文件`} status={item.status}
      detail={item.changes.map((change) => `${change.kind}: ${change.path}`).join("\n")} />;
  }
  if (item.type === "mcpToolCall") {
    return <Activity testID={`item:${item.type}:${encodeURIComponent(item.id)}`}
      title={`${item.server} · ${item.tool}`} status={item.status}
      detail={item.error?.message ?? undefined} />;
  }
  if (item.type === "dynamicToolCall") {
    return <Activity testID={`item:${item.type}:${encodeURIComponent(item.id)}`}
      title={`${item.namespace ? `${item.namespace} · ` : ""}${item.tool}`}
      status={item.status} />;
  }
  if (item.type === "collabAgentToolCall") {
    return <Activity testID={`item:${item.type}:${encodeURIComponent(item.id)}`}
      title={`协作任务 · ${String(item.tool)}`} status={item.status} />;
  }
  if (item.type === "webSearch") return <Activity
    testID={`item:${item.type}:${encodeURIComponent(item.id)}`} title="网页搜索" status="completed" />;
  if (item.type === "imageView") return <Activity
    testID={`item:${item.type}:${encodeURIComponent(item.id)}`}
    title={`查看图片 · ${item.path}`} status="completed" />;
  if (item.type === "imageGeneration") return <Activity
    testID={`item:${item.type}:${encodeURIComponent(item.id)}`} title="生成图片" status={item.status} />;
  if (item.type === "contextCompaction") return <Activity
    testID={`item:${item.type}:${encodeURIComponent(item.id)}`}
    title="已压缩会话上下文" status="completed" />;
  if (item.type === "enteredReviewMode" || item.type === "exitedReviewMode") {
    return <Activity testID={`item:${item.type}:${encodeURIComponent(item.id)}`}
      title={item.type === "enteredReviewMode" ? "开始代码审查" : "完成代码审查"}
      status="completed" />;
  }
  return null;
});

function Activity({ testID, title, status, detail }: {
  testID: string;
  title: string;
  status: string;
  detail?: string | undefined;
}) {
  const theme = useTheme();
  const running = status === "inProgress" || status === "running";
  const preview = detail ? activityDetailPreview(detail) : null;
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const expanded = expandedId === testID;
  return <View testID={testID}
    style={[styles.activity, { borderColor: theme.colors.border }]}>
    <View style={styles.activityHeader}>
      <Text numberOfLines={2} style={[styles.activityTitle, { color: theme.colors.text }]}>{title}</Text>
      <Muted>{running ? "运行中" : status}</Muted>
    </View>
    {preview ? <Text selectable numberOfLines={expanded ? undefined : 13}
      style={[styles.output, expanded && styles.outputExpanded,
        { color: theme.colors.textMuted }]}>
      {expanded ? detail : preview.text}
    </Text> : null}
    {preview?.truncated ? <Pressable accessibilityRole="button"
      testID={`${testID}:toggle-output`} hitSlop={8}
      onPress={() => setExpandedId(expanded ? null : testID)}>
      <Muted>{expanded ? "收起输出" : "展开完整输出"}</Muted>
    </Pressable> : null}
  </View>;
}

const styles = StyleSheet.create({
  userRow: { flexDirection: "row", justifyContent: "flex-end", paddingHorizontal: 12, paddingVertical: 5 },
  userBubble: { maxWidth: "88%", borderRadius: 12, paddingHorizontal: 13, paddingVertical: 9 },
  userText: { fontFamily: "Inter_400Regular", fontSize: 15, lineHeight: 22 },
  file: { fontFamily: "Inter_400Regular", fontSize: 13, marginTop: 5, opacity: 0.9 },
  agentRow: { paddingHorizontal: 16, paddingVertical: 8 },
  reasoning: { paddingHorizontal: 16, paddingVertical: 6 },
  activity: { marginHorizontal: 16, paddingVertical: 10, borderTopWidth: StyleSheet.hairlineWidth, gap: 7 },
  activityHeader: { flexDirection: "row", gap: 10, justifyContent: "space-between", alignItems: "center" },
  activityTitle: { flex: 1, fontFamily: "Inter_500Medium", fontSize: 13, lineHeight: 18 },
  output: { fontFamily: "monospace", fontSize: 12, lineHeight: 17, maxHeight: 180 },
  outputExpanded: { maxHeight: undefined },
});
