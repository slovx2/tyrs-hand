import { StyleSheet, Text, View } from "react-native";
import Markdown from "react-native-markdown-display";

import { Muted } from "@/components/ui";
import { useTheme } from "@/theme/ThemeProvider";
import type { Message } from "@/types/protocol";

function textContent(content: unknown): string {
  if (!content || typeof content !== "object") return "";
  const value = content as Record<string, unknown>;
  if (value.type === "text" && typeof value.text === "string") return value.text;
  const v = value.v as Record<string, unknown> | undefined;
  const nestedContent = v?.content as Record<string, unknown> | undefined;
  const data = nestedContent?.data as Record<string, unknown> | undefined;
  return typeof data?.message === "string" ? data.message : "";
}

export function MessageBubble({ message }: { message: Message }) {
  const theme = useTheme();
  const user = message.role === "user";
  const text = textContent(message.content);
  return <View testID={`message:role:${message.role}`}
    style={[styles.row, user && styles.userRow]}><View
      testID={`message:${encodeURIComponent(message.localId || String(message.seq))}`}>
    <View style={[styles.bubble, user ? { backgroundColor: theme.colors.accent } :
      { backgroundColor: theme.colors.surface, borderColor: theme.colors.border, borderWidth: StyleSheet.hairlineWidth }]}>
      {user ? <Text selectable style={[styles.userText, { color: theme.colors.accentForeground }]}>{text}</Text> :
        <Markdown style={{ body: { color: theme.colors.text, fontFamily: "Inter_400Regular", fontSize: 15,
          lineHeight: 23 }, code_inline: { backgroundColor: theme.colors.surfaceAlt, color: theme.colors.text },
          fence: { backgroundColor: theme.colors.surfaceAlt, color: theme.colors.text,
            borderColor: theme.colors.border } }}>{text}</Markdown>}
      {message.attachments.map((attachment) => <View key={attachment.id}
        style={[styles.attachment, { backgroundColor: user ? "rgba(255,255,255,0.16)" : theme.colors.surfaceAlt }]}>
        <Text numberOfLines={1} style={{ color: user ? theme.colors.accentForeground : theme.colors.text }}>
          {attachment.kind === "image" ? "图片" : "文件"} · {attachment.filename}
        </Text>
      </View>)}
      <Muted>{user ? "你" : "Tyrs Hand"}</Muted>
    </View></View>
  </View>;
}

const styles = StyleSheet.create({
  row: { flexDirection: "row", justifyContent: "flex-start", paddingHorizontal: 12, paddingVertical: 5 },
  userRow: { justifyContent: "flex-end" },
  bubble: { maxWidth: "88%", minWidth: 90, borderRadius: 12, paddingHorizontal: 13, paddingVertical: 9 },
  userText: { fontFamily: "Inter_400Regular", fontSize: 15, lineHeight: 22, marginBottom: 4 },
  attachment: { marginTop: 6, borderRadius: 6, paddingHorizontal: 8, paddingVertical: 6 },
});
