import { StyleSheet, Text, View } from "react-native";

import type { PendingMessagePreview } from "@/db/pendingMessages";
import { useTheme } from "@/theme/ThemeProvider";

export function PendingMessagePreviews({ items }: { items: PendingMessagePreview[] }) {
  const theme = useTheme();
  if (items.length === 0) return null;
  return <View style={styles.container} pointerEvents="none">
    {items.map((item) => <View key={item.clientMessageId}
      style={[styles.chip, { backgroundColor: theme.colors.surfaceAlt, borderColor: theme.colors.border }]}>
      <Text numberOfLines={1} ellipsizeMode="tail" style={[styles.text, { color: theme.colors.textMuted }]}>
        {item.text.trim() || item.attachments.map((attachment) => attachment.name).join("、")}
      </Text>
    </View>)}
  </View>;
}

const styles = StyleSheet.create({
  container: { gap: 5, paddingHorizontal: 12, paddingBottom: 4 },
  chip: { minHeight: 28, borderWidth: StyleSheet.hairlineWidth, borderRadius: 8,
    justifyContent: "center", paddingHorizontal: 9 },
  text: { fontFamily: "Inter_400Regular", fontSize: 12, lineHeight: 18 },
});
