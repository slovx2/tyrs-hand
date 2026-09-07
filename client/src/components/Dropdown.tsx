import { Modal, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import * as React from "react";

import { useTheme } from "@/theme/ThemeProvider";
import { Muted } from "./ui";

export type DropdownOption = { value: string; label: string; detail?: string };

export function Dropdown({ label, value, options, placeholder, emptyLabel = "无可用选项",
  onChange, testID }: {
  label: string;
  value: string | null;
  options: readonly DropdownOption[];
  placeholder?: string;
  emptyLabel?: string;
  onChange: (value: string) => void;
  testID: string;
}) {
  const theme = useTheme();
  const [selected] = options.filter((option) => option.value === value);
  const [visible, setVisible] = React.useState(false);
  const display = selected?.label ?? placeholder ?? emptyLabel;
  return <>
    <Pressable testID={testID} accessibilityRole="button" accessibilityLabel={`${label}：${display}`}
      onPress={() => setVisible(true)} style={({ pressed }) => [styles.trigger,
        { backgroundColor: theme.colors.surface, borderColor: theme.colors.border,
          opacity: pressed ? 0.78 : 1 }] }>
      <View style={styles.triggerCopy}><Muted>{label}</Muted>
        <Text numberOfLines={1} ellipsizeMode="tail"
          style={[styles.value, { color: selected ? theme.colors.text : theme.colors.textMuted }]}>
          {display}
        </Text></View>
      <Text style={[styles.chevron, { color: theme.colors.textMuted }]}>⌄</Text>
    </Pressable>
    <Modal visible={visible} transparent animationType="slide" onRequestClose={() => setVisible(false)}>
      <Pressable style={[styles.backdrop, { backgroundColor: theme.colors.overlay }]}
        onPress={() => setVisible(false)}>
        <Pressable style={[styles.sheet, { backgroundColor: theme.colors.surface }]}
          onPress={(event) => event.stopPropagation()}>
          <View style={styles.sheetHeader}><Text style={[styles.sheetTitle, { color: theme.colors.text }]}>
            {label}</Text><Pressable testID={`${testID}:close`} onPress={() => setVisible(false)}>
            <Text style={[styles.close, { color: theme.colors.accent }]}>关闭</Text>
          </Pressable></View>
          {options.length === 0 ? <View style={styles.empty}><Muted>{emptyLabel}</Muted></View> :
            <ScrollView contentContainerStyle={styles.options}>
              {options.map((option) => { const active = option.value === value;
                return <Pressable key={option.value} testID={`${testID}:option:${option.value}`}
                  accessibilityRole="button" accessibilityState={{ selected: active }}
                  onPress={() => { onChange(option.value); setVisible(false); }}
                  style={({ pressed }) => [styles.option, { borderColor: theme.colors.border,
                    backgroundColor: active || pressed ? theme.colors.surfaceAlt : theme.colors.surface }] }>
                  <View style={styles.optionCopy}><Text numberOfLines={1} ellipsizeMode="tail"
                    style={[styles.optionLabel, { color: theme.colors.text }]}>{option.label}</Text>
                    {option.detail ? <Muted numberOfLines={1}>{option.detail}</Muted> : null}</View>
                  {active ? <Text style={[styles.check, { color: theme.colors.accent }]}>✓</Text> : null}
                </Pressable>;
              })}
            </ScrollView>}
        </Pressable>
      </Pressable>
    </Modal>
  </>;
}

const styles = StyleSheet.create({
  trigger: { minHeight: 58, borderWidth: StyleSheet.hairlineWidth, borderRadius: 10,
    paddingHorizontal: 12, paddingVertical: 8, flexDirection: "row", alignItems: "center", gap: 8 },
  triggerCopy: { flex: 1, minWidth: 0, gap: 1 },
  value: { fontFamily: "Inter_500Medium", fontSize: 15, lineHeight: 20 },
  chevron: { fontSize: 22, lineHeight: 22, marginTop: -5 },
  backdrop: { flex: 1, justifyContent: "flex-end" },
  sheet: { maxHeight: "78%", borderTopLeftRadius: 18, borderTopRightRadius: 18, paddingTop: 16,
    paddingBottom: 24 },
  sheetHeader: { minHeight: 44, paddingHorizontal: 18, flexDirection: "row", alignItems: "center",
    justifyContent: "space-between" },
  sheetTitle: { fontFamily: "Inter_600SemiBold", fontSize: 18 },
  close: { fontFamily: "Inter_500Medium", fontSize: 14 },
  options: { padding: 12, gap: 8 },
  option: { minHeight: 52, borderWidth: StyleSheet.hairlineWidth, borderRadius: 10,
    paddingHorizontal: 12, paddingVertical: 8, flexDirection: "row", alignItems: "center", gap: 10 },
  optionCopy: { flex: 1, minWidth: 0, gap: 1 },
  optionLabel: { fontFamily: "Inter_500Medium", fontSize: 15, lineHeight: 20 },
  check: { fontSize: 20, lineHeight: 20 },
  empty: { minHeight: 100, alignItems: "center", justifyContent: "center", padding: 20 },
});
