import { memo } from "react";
import { StyleSheet, Text, View } from "react-native";

import { useTheme } from "@/theme/ThemeProvider";
import type { Message } from "@/types/protocol";
import { MarkdownContent } from "./MarkdownContent";

function textContent(content: unknown): string {
  if (!content || typeof content !== "object") return "";
  const value = content as Record<string, unknown>;
  if (value.type === "text" && typeof value.text === "string") return value.text;
  const v = value.v as Record<string, unknown> | undefined;
  const nestedContent = v?.content as Record<string, unknown> | undefined;
  const data = nestedContent?.data as Record<string, unknown> | undefined;
  return typeof data?.message === "string" ? data.message : "";
}

export const MessageBubble = memo(function MessageBubble({ message }: { message: Message }) {
  const theme = useTheme();
  const user = message.role === "user";
  const text = textContent(message.content);
  const messageId = `message:${encodeURIComponent(message.localId || String(message.seq))}`;
  if (user) return <View testID="message:role:user" style={[styles.row, styles.userRow]}>
    <View testID={messageId} style={[styles.userBubble, { backgroundColor: theme.colors.accent }]}>
      <Text selectable style={[styles.userText, { color: theme.colors.accentForeground }]}>{text}</Text>
      {message.attachments.map((attachment) => <View key={attachment.id}
        style={[styles.attachment, { backgroundColor: "rgba(255,255,255,0.16)" }]}>
        <Text selectable numberOfLines={1} style={{ color: theme.colors.accentForeground }}>
          {attachment.kind === "image" ? "图片" : "文件"} · {attachment.filename}
        </Text>
      </View>)}
    </View>
  </View>;
  return <View testID={`message:role:${message.role}`} style={styles.agentRow}>
    <View testID={messageId} style={styles.agentBody}>
      <MarkdownContent>{text}</MarkdownContent>
      {message.attachments.map((attachment) => <View key={attachment.id}
        style={[styles.attachment, { backgroundColor: theme.colors.surfaceAlt }]}>
        <Text selectable numberOfLines={1} style={{ color: theme.colors.text }}>
          {attachment.kind === "image" ? "图片" : "文件"} · {attachment.filename}
        </Text>
      </View>)}
    </View>
  </View>;
});

const styles = StyleSheet.create({
  row: { flexDirection: "row", justifyContent: "flex-start", paddingHorizontal: 12, paddingVertical: 5 },
  userRow: { justifyContent: "flex-end" },
  userBubble: { maxWidth: "88%", borderRadius: 12, paddingHorizontal: 13, paddingVertical: 9 },
  userText: { fontFamily: "Inter_400Regular", fontSize: 15, lineHeight: 22 },
  agentRow: { paddingHorizontal: 16, paddingVertical: 8 },
  agentBody: { width: "100%" },
  attachment: { marginTop: 6, borderRadius: 6, paddingHorizontal: 8, paddingVertical: 6 },
});
