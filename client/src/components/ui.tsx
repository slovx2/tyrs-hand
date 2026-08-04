import type { ReactNode } from "react";
import {
  ActivityIndicator,
  Pressable,
  StyleSheet,
  Text,
  View,
  type PressableProps,
  type StyleProp,
  type ViewProps,
  type ViewStyle,
} from "react-native";

import { useTheme } from "@/theme/ThemeProvider";

export function Screen({ children, style }: { children: ReactNode; style?: StyleProp<ViewStyle> }) {
  const theme = useTheme();
  return <View style={[styles.screen, { backgroundColor: theme.colors.app }, style]}>{children}</View>;
}

export function Card({ children, style, ...props }: ViewProps & { children: ReactNode; style?: StyleProp<ViewStyle> }) {
  const theme = useTheme();
  return <View {...props} style={[styles.card, { backgroundColor: theme.colors.surface,
    borderColor: theme.colors.border }, theme.shadow, style]}>{children}</View>;
}

export function Title({ children }: { children: ReactNode }) {
  const theme = useTheme();
  return <Text style={[styles.title, { color: theme.colors.text }]}>{children}</Text>;
}

export function Muted({ children, numberOfLines, selectable }: {
  children: ReactNode; numberOfLines?: number; selectable?: boolean;
}) {
  const theme = useTheme();
  return <Text numberOfLines={numberOfLines} selectable={selectable}
    style={[styles.muted, { color: theme.colors.textMuted }]}>{children}</Text>;
}

export function Button({ title, variant = "primary", loading, disabled, style, ...props }:
  PressableProps & { title: string; variant?: "primary" | "secondary" | "danger";
    loading?: boolean; style?: StyleProp<ViewStyle> }) {
  const theme = useTheme();
  const backgroundColor = variant === "primary" ? theme.colors.accent :
    variant === "danger" ? theme.colors.danger : theme.colors.surfaceAlt;
  const color = variant === "primary" || variant === "danger" ? theme.colors.accentForeground : theme.colors.text;
  return <Pressable accessibilityRole="button" disabled={disabled || loading} {...props}
    style={({ pressed }) => [styles.button, { backgroundColor, borderColor: theme.colors.border,
      opacity: disabled || loading ? 0.5 : pressed ? 0.75 : 1 }, style]}>
    {loading ? <ActivityIndicator color={color} /> : <Text style={[styles.buttonText, { color }]}>{title}</Text>}
  </Pressable>;
}

export function StatusDot({ status }: { status: "success" | "warning" | "danger" | "muted" }) {
  const theme = useTheme();
  const color = status === "success" ? theme.colors.success : status === "warning" ? theme.colors.warning :
    status === "danger" ? theme.colors.danger : theme.colors.textMuted;
  return <View style={[styles.dot, { backgroundColor: color }]} />;
}

export function EmptyState({ title, detail, action }: { title: string; detail: string; action?: ReactNode }) {
  return <View style={styles.empty}>
    <Title>{title}</Title>
    <Muted>{detail}</Muted>
    {action}
  </View>;
}

const styles = StyleSheet.create({
  screen: { flex: 1 },
  card: { borderWidth: StyleSheet.hairlineWidth, borderRadius: 12, padding: 16 },
  title: { fontFamily: "Inter_600SemiBold", fontSize: 18, lineHeight: 24 },
  muted: { fontFamily: "Inter_400Regular", fontSize: 13, lineHeight: 19 },
  button: { minHeight: 42, borderRadius: 8, borderWidth: StyleSheet.hairlineWidth,
    paddingHorizontal: 16, alignItems: "center", justifyContent: "center" },
  buttonText: { fontFamily: "Inter_600SemiBold", fontSize: 14 },
  dot: { width: 8, height: 8, borderRadius: 999 },
  empty: { flex: 1, alignItems: "center", justifyContent: "center", gap: 8, padding: 32 },
});
