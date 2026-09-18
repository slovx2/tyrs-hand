import { Ionicons } from "@expo/vector-icons";
import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";
import { memo, useCallback, useEffect, useMemo, useRef, useState,
  type ComponentProps } from "react";
import { useRecyclingState } from "@shopify/flash-list";
import type { ASTNode } from "react-native-markdown-display";
import { sameConversationRow, type ConversationRow } from "./conversationRows";
import { ActivityIndicator, Animated, Easing, Pressable, StyleSheet, Text, View } from "react-native";

import { Muted } from "@/components/ui";
import type { MobileTurn, UserInputResponseItem } from "@/app-server/types";
import { CachedMessageImage } from "@/features/images/CachedMessageImage";
import { RemoteMessageImage } from "@/features/images/RemoteMessageImage";
import { useReducedMotion } from "@/hooks/useReducedMotion";
import { useTheme } from "@/theme/ThemeProvider";
import { MarkdownContent } from "./MarkdownContent";
import { ThinkingShimmer } from "./ThinkingShimmer";
import { projectUserMessage, type UserAttachment } from "./userMessagePresentation";
import { turnActivitySummary,
  type ToolGroup, type TurnBlock } from "./turnPresentation";

type ContentRow = Exclude<ConversationRow, { kind: "request" }>;

export const ConversationContentRow = memo(function ConversationContentRow({ row, profileId,
  threadId, expandedTool, onToggleTurn, onLoadDetails, onToggleTool, onDisclosureChange }: {
  row: ContentRow;
  profileId: string;
  threadId: string;
  expandedTool: boolean;
  onToggleTurn: (turnId: string) => void;
  onLoadDetails: (turnId: string) => void;
  onToggleTool: (key: string) => void;
  onDisclosureChange: () => void;
}) {
  const theme = useTheme();
  const toggleTool = useCallback(() => onToggleTool(row.key), [onToggleTool, row.key]);
  if (row.kind === "activity") return <TurnActivityRow row={row} onToggle={onToggleTurn} />;
  if (row.kind === "detailPage") return <Pressable testID={`turn:${row.turnId}:load-details`}
    disabled={row.detail?.loading} onPress={() => onLoadDetails(row.turnId)}
    style={styles.detailPage} accessibilityRole="button">
    {row.detail?.loading ? <ActivityIndicator color={theme.colors.textMuted} /> : null}
    <Text style={{ color: row.detail?.error ? theme.colors.danger : theme.colors.accent }}>
      {row.detail?.loading ? "正在加载本轮内容…" : row.detail?.error
        ? "加载失败，点按重试" : !row.detail?.loaded ? "加载本轮内容"
          : row.detail.direction === "desc" ? "加载更早过程" : "加载更多过程"}
    </Text>
  </Pressable>;
  if (row.kind === "operation") return <View style={styles.operationRow}
    testID={`item:${row.item.type}:${encodeURIComponent(row.item.id)}`}>
    <Ionicons name={row.operation.failed ? "close-circle-outline" : row.operation.running
      ? "ellipsis-horizontal-circle-outline" : "checkmark-circle-outline"} size={15}
      color={row.operation.failed ? theme.colors.danger : theme.colors.textMuted} />
    <Text selectable numberOfLines={2} style={[styles.toolOperationText,
      { color: row.operation.failed ? theme.colors.danger : theme.colors.textMuted }]}>
      {row.operation.text}
    </Text>
  </View>;
  if (row.kind === "status") return <View style={styles.thinking}>
    {row.thinking ? <ThinkingShimmer active color={theme.colors.textMuted}
      highlightColor={theme.colors.text} style={styles.thinkingText}>正在思考</ThinkingShimmer> : null}
    {row.turn.status === "failed" || row.turn.status === "interrupted"
      ? <Text selectable testID="turn:error" style={{ color: theme.colors.danger }}>
        {row.turn.error?.message || (row.turn.status === "interrupted" ? "本轮已中断" : "本轮执行失败")}
      </Text> : null}
  </View>;
  return <TurnBlockView block={row.block} profileId={profileId} threadId={threadId}
    turnId={row.turnId} live={row.live} ast={row.ast} resolveAst={row.resolveAst} expandedTool={expandedTool}
    onToggleTool={toggleTool} onDisclosureChange={onDisclosureChange} />;
}, (left, right) => sameConversationRow(left.row, right.row) &&
  left.profileId === right.profileId && left.threadId === right.threadId &&
  left.expandedTool === right.expandedTool && left.onToggleTurn === right.onToggleTurn &&
  left.onLoadDetails === right.onLoadDetails && left.onToggleTool === right.onToggleTool &&
  left.onDisclosureChange === right.onDisclosureChange);

function TurnActivityRow({ row, onToggle }: {
  row: Extract<ConversationRow, { kind: "activity" }>;
  onToggle: (turnId: string) => void;
}) {
  const now = useElapsedClock(row.turn, row.turn.status === "inProgress");
  return <ActivityHeader turnId={row.turnId} collapsed={!row.expanded}
    summary={row.noFinal ? "本轮无最终回答 · 查看本轮内容" : turnActivitySummary(row.turn, now)}
    onPress={() => onToggle(row.turnId)} />;
}

function ActivityHeader({ turnId, collapsed, summary, onPress }: {
  turnId: string;
  collapsed: boolean;
  summary: string;
  onPress: () => void;
}) {
  const theme = useTheme();
  return <View style={[styles.activitySummary, { borderBottomColor: theme.colors.border }]}>
    <Pressable accessibilityRole="button" accessibilityState={{ expanded: !collapsed }}
      accessibilityLabel={`${summary}，${collapsed ? "展开" : "收起"}处理过程`}
      testID={`turn:${turnId}:activity-toggle`} hitSlop={8} onPress={onPress}
      style={styles.activitySummaryButton}>
      <Text style={[styles.activitySummaryText, { color: theme.colors.textMuted }]}>{summary}</Text>
      <DisclosureChevron expanded={!collapsed} color={theme.colors.textMuted} />
    </Pressable>
  </View>;
}

function TurnBlockView({ block, profileId, threadId, turnId, live, ast, resolveAst, expandedTool,
  onToggleTool, onDisclosureChange }: {
  block: TurnBlock;
  profileId: string;
  threadId: string;
  turnId: string;
  live: boolean;
  ast: ASTNode[] | undefined;
  resolveAst: (() => ASTNode[]) | undefined;
  expandedTool: boolean;
  onToggleTool: () => void;
  onDisclosureChange: () => void;
}) {
  if (block.kind === "user") return <UserMessage item={block.item} profileId={profileId} />;
  if (block.kind === "commentary") return block.item.text.trim()
    ? <View testID="message:phase:commentary" style={[styles.commentary, ast && styles.markdownSegment]}>
      {live ? <LiveTextContent compact>{block.item.text}</LiveTextContent>
        : <MarkdownContent astOverride={ast} resolveAst={resolveAst} compact profileId={profileId} cacheKey={`commentary:${block.item.id}`}>
          {block.item.text}
        </MarkdownContent>}
    </View> : null;
  if (block.kind === "tools") return <ToolGroupView group={block}
    expanded={expandedTool} onToggle={onToggleTool} />;
  if (block.kind === "plan") return <View testID={`plan:${block.item.id}`} style={[styles.plan, ast && styles.markdownSegment]}>
    <Muted>计划</Muted>
    {live ? <LiveTextContent>{block.item.text}</LiveTextContent>
      : <MarkdownContent astOverride={ast} resolveAst={resolveAst} profileId={profileId} cacheKey={`plan:${block.item.id}`}>
        {block.item.text}
      </MarkdownContent>}
  </View>;
  if (block.kind === "userInputResponse") return <UserInputResponse item={block.item}
    onDisclosureChange={onDisclosureChange} />;
  if (block.kind === "generatedImage") return <View style={styles.generatedImage}>
    {block.image.source.startsWith("/")
      ? <RemoteMessageImage profileId={profileId} remotePath={block.image.source}
        filename={imageFilename(block.image.source)}
        cacheKey={`turn:${threadId}:${turnId}:${block.image.id}`}
        testID={`generated-image:${block.image.id}`} />
      : <CachedMessageImage uri={block.image.source} filename="生成的图片"
        cacheKey={`turn:${threadId}:${turnId}:${block.image.id}`}
        testID={`generated-image:${block.image.id}`} />}
  </View>;
  return block.item.text.trim() ? <View testID="message:role:agent" style={[styles.agentRow, ast && styles.markdownSegment]}>
    {live ? <LiveTextContent>{block.item.text}</LiveTextContent>
      : <MarkdownContent astOverride={ast} resolveAst={resolveAst} profileId={profileId} cacheKey={`agentMessage:${block.item.id}`}>
        {block.item.text}
      </MarkdownContent>}
  </View> : null;
}

const LiveTextContent = memo(function LiveTextContent({ children, compact = false }: {
  children: string;
  compact?: boolean;
}) {
  const theme = useTheme();
  return <Text selectable testID="message:live-text"
    style={[styles.liveText, compact && styles.liveTextCompact, { color: theme.colors.text }]}>
    {children}
  </Text>;
});

function UserInputResponse({ item, onDisclosureChange }: {
  item: UserInputResponseItem;
  onDisclosureChange: () => void;
}) {
  const theme = useTheme();
  const [expanded, setExpanded] = useRecyclingState(false, [item.id]);
  const count = item.questions.length;
  return <View testID={`user-input-response:${item.requestId}`} style={styles.userInputResponse}>
    <Pressable accessibilityRole="button" accessibilityState={{ expanded }}
      accessibilityLabel={`询问了 ${count} 个问题，${expanded ? "收起" : "展开"}回答`}
      testID={`user-input-response:${item.requestId}:toggle`} hitSlop={8}
      style={styles.userInputResponseHeader} onPress={() => {
        setExpanded((value) => !value);
        onDisclosureChange();
      }}>
      <Ionicons name="help-circle-outline" size={17} color={theme.colors.textMuted} />
      <Text style={[styles.userInputResponseTitle, { color: theme.colors.textMuted }]}>询问了 {count} 个问题</Text>
      <DisclosureChevron expanded={expanded} color={theme.colors.textMuted} />
    </Pressable>
    {expanded ? <View style={[styles.userInputResponseDetails,
      { borderLeftColor: theme.colors.border }]}>
      {item.questions.map((question) => {
        const answer = item.answers[question.id]?.join("，") || "未提供回答";
        return <View key={question.id} style={styles.userInputResponseQuestion}>
          <Text selectable style={[styles.userInputResponseQuestionText,
            { color: theme.colors.text }]}>{question.question}</Text>
          <Text selectable style={[styles.userInputResponseAnswer,
            { color: theme.colors.textMuted }]}>{answer}</Text>
        </View>;
      })}
    </View> : null}
  </View>;
}

function UserMessage({ item, profileId }: {
  item: Extract<ThreadItem, { type: "userMessage" }>;
  profileId: string;
}) {
  const theme = useTheme();
  const presentation = useMemo(() => projectUserMessage(item), [item]);
  return <View testID="message:role:user" style={styles.userRow}>
    <View testID={`message:${encodeURIComponent(item.clientId ?? item.id)}`}
      style={[styles.userBubble, { backgroundColor: theme.colors.surfaceAlt }]}>
      {presentation.text ? <Text selectable style={[styles.userText, { color: theme.colors.text }]}>
        {presentation.text}
      </Text>
        : null}
      {presentation.attachments.map((attachment) => <UserAttachmentView key={attachment.key}
        attachment={attachment} profileId={profileId} />)}
    </View>
  </View>;
}

function UserAttachmentView({ attachment, profileId }: {
  attachment: UserAttachment;
  profileId: string;
}) {
  const theme = useTheme();
  if (attachment.kind === "image" && attachment.remotePath) {
    return <RemoteMessageImage profileId={profileId} remotePath={attachment.remotePath}
      filename={attachment.name} cacheKey={`attachment:${attachment.key}`}
      testID={`user-image:${attachment.key}`} />;
  }
  if (attachment.kind === "image" && attachment.uri) {
    return <CachedMessageImage uri={attachment.uri} filename={attachment.name}
      cacheKey={`attachment:${attachment.key}`}
      testID={`user-image:${attachment.key}`} />;
  }
  return <Text numberOfLines={1} style={[styles.file, { color: theme.colors.textMuted }]}>
    {attachment.name}
  </Text>;
}

function imageFilename(path: string): string {
  return path.split("/").at(-1) || "生成的图片";
}

function ToolGroupView({ group, expanded, onToggle }: {
  group: ToolGroup;
  expanded: boolean;
  onToggle: () => void;
}) {
  const theme = useTheme();
  return <View testID={`tool-group:${group.key}`} style={styles.toolGroup}>
    <Pressable accessibilityRole="button" accessibilityState={{ expanded }}
      accessibilityLabel={`${group.title}，${expanded ? "收起" : "展开"}操作`}
      testID={`tool-group:${group.key}:toggle`} hitSlop={8} style={styles.toolHeader}
      onPress={onToggle}>
      <Ionicons name={toolIcon(group.category)} size={17} color={theme.colors.textMuted} />
      <View style={styles.toolTitle}>
        <ThinkingShimmer active={group.running} color={theme.colors.textMuted}
          highlightColor={theme.colors.text} style={styles.toolTitleText}
          testID={group.running ? "tool-group:shimmer" : undefined}>{group.title}</ThinkingShimmer>
      </View>
      <DisclosureChevron expanded={expanded} color={theme.colors.textMuted} />
    </Pressable>
  </View>;
}

function useElapsedClock(turn: MobileTurn, enabled: boolean): number {
  const [nowMs, setNowMs] = useState(Date.now());
  useEffect(() => {
    if (!enabled || turn.durationMs !== null || turn.completedAt !== null || turn.startedAt === null) {
      return;
    }
    const timer = setInterval(() => setNowMs(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [enabled, turn.completedAt, turn.durationMs, turn.startedAt]);
  return nowMs;
}

function DisclosureChevron({ expanded, color }: { expanded: boolean; color: string }) {
  const reduceMotion = useReducedMotion();
  const progress = useRef(new Animated.Value(expanded ? 1 : 0)).current;
  useEffect(() => {
    if (reduceMotion) {
      progress.setValue(expanded ? 1 : 0);
      return;
    }
    const animation = Animated.timing(progress, { toValue: expanded ? 1 : 0,
      duration: 180, easing: Easing.out(Easing.cubic), useNativeDriver: true });
    animation.start();
    return () => animation.stop();
  }, [expanded, progress, reduceMotion]);
  const rotate = progress.interpolate({ inputRange: [0, 1], outputRange: ["0deg", "90deg"] });
  return <Animated.View style={{ transform: [{ rotate }] }}>
    <Ionicons name="chevron-forward" size={15} color={color} />
  </Animated.View>;
}

function toolIcon(category: ToolGroup["category"]): ComponentProps<typeof Ionicons>["name"] {
  switch (category) {
  case "command": return "terminal-outline";
  case "file": return "document-text-outline";
  case "search": return "search-outline";
  case "image": return "image-outline";
  case "collaboration": return "people-outline";
  case "mcp":
  case "dynamic": return "extension-puzzle-outline";
  case "wait": return "time-outline";
  case "context": return "layers-outline";
  case "review": return "code-slash-outline";
  case "mixed": return "construct-outline";
  }
}

const styles = StyleSheet.create({
  markdownSegment: { paddingTop: 0, paddingBottom: 0 },
  detailPage: { paddingHorizontal: 16, paddingVertical: 12, flexDirection: "row", gap: 8 },
  operationRow: { marginHorizontal: 24, paddingVertical: 4, flexDirection: "row", gap: 7 },
  userRow: { flexDirection: "row", justifyContent: "flex-end", paddingHorizontal: 12,
    paddingBottom: 8, paddingTop: 5 },
  userBubble: { maxWidth: "88%", borderRadius: 18, paddingHorizontal: 14, paddingVertical: 10 },
  userText: { fontFamily: "Inter_400Regular", fontSize: 15, lineHeight: 22 },
  file: { fontFamily: "Inter_400Regular", fontSize: 13, marginTop: 5 },
  agentRow: { paddingHorizontal: 16, paddingBottom: 10, paddingTop: 8 },
  liveText: { fontFamily: "Inter_400Regular", fontSize: 15, lineHeight: 24,
    includeFontPadding: false },
  liveTextCompact: { lineHeight: 22 },
  commentary: { opacity: 0.78, paddingHorizontal: 16, paddingVertical: 5 },
  thinking: { paddingHorizontal: 16, paddingVertical: 8 },
  thinkingText: { fontFamily: "Inter_400Regular", fontSize: 14, lineHeight: 20 },
  plan: { gap: 6, paddingHorizontal: 16, paddingBottom: 4, paddingTop: 8 },
  generatedImage: { paddingHorizontal: 16, paddingVertical: 5 },
  activitySummary: { borderBottomWidth: StyleSheet.hairlineWidth, marginHorizontal: 16,
    marginBottom: 7, marginTop: 7, paddingBottom: 9 },
  activitySummaryButton: { alignItems: "center", alignSelf: "flex-start", flexDirection: "row",
    gap: 3, minHeight: 24 },
  activitySummaryText: { fontFamily: "Inter_400Regular", fontSize: 14, lineHeight: 20 },
  toolGroup: { marginHorizontal: 16, paddingVertical: 4 },
  toolHeader: { alignItems: "center", flexDirection: "row", gap: 8, minHeight: 30 },
  toolTitle: { flexShrink: 1, minWidth: 0 },
  toolTitleText: { fontFamily: "Inter_400Regular", fontSize: 14, lineHeight: 20 },
  toolOperations: { borderLeftWidth: StyleSheet.hairlineWidth, gap: 7, marginLeft: 8,
    paddingBottom: 4, paddingLeft: 16, paddingTop: 5 },
  toolOperation: { alignItems: "flex-start", flexDirection: "row", gap: 7 },
  toolOperationText: { flex: 1, fontFamily: "Inter_400Regular", fontSize: 13, lineHeight: 19 },
  userInputResponse: { marginHorizontal: 16, paddingVertical: 4 },
  userInputResponseHeader: { alignItems: "center", alignSelf: "flex-start", flexDirection: "row",
    gap: 8, minHeight: 30 },
  userInputResponseTitle: { fontFamily: "Inter_400Regular", fontSize: 14, lineHeight: 20 },
  userInputResponseDetails: { borderLeftWidth: StyleSheet.hairlineWidth, gap: 12, marginLeft: 8,
    paddingBottom: 5, paddingLeft: 16, paddingTop: 7 },
  userInputResponseQuestion: { gap: 3 },
  userInputResponseQuestionText: { fontFamily: "Inter_500Medium", fontSize: 14, lineHeight: 20 },
  userInputResponseAnswer: { fontFamily: "Inter_400Regular", fontSize: 13, lineHeight: 19 },
  error: { marginHorizontal: 16, paddingVertical: 8 },
  errorText: { fontFamily: "Inter_400Regular", fontSize: 14, lineHeight: 20 },
});
