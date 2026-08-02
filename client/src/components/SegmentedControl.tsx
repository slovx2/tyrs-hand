import { Pressable, StyleSheet, Text, View } from "react-native";

import { useTheme } from "@/theme/ThemeProvider";

export function SegmentedControl<T extends string>({ value, options, onChange, testIDPrefix }: {
  value: T;
  options: readonly { value: T; label: string }[];
  onChange: (value: T) => void;
  testIDPrefix?: string;
}) {
  const theme = useTheme();
  return <View style={[styles.container, { backgroundColor: theme.colors.surfaceAlt,
    borderColor: theme.colors.border }]}>
    {options.map((option) => {
      const selected = option.value === value;
      return <Pressable key={option.value}
        testID={testIDPrefix ? `${testIDPrefix}:${option.value}` : undefined}
        accessibilityRole="button" accessibilityState={{ selected }}
        onPress={() => onChange(option.value)}
        style={[styles.item, selected && { backgroundColor: theme.colors.surface },
          selected && theme.shadow]}>
        <Text style={[styles.label, { color: selected ? theme.colors.text : theme.colors.textMuted }]}> 
          {option.label}
        </Text>
        {selected && testIDPrefix && <View pointerEvents="none"
          testID={`${testIDPrefix}:${option.value}:selected`} style={styles.selectedMarker} />}
      </Pressable>;
    })}
  </View>;
}

const styles = StyleSheet.create({
  container: { flexDirection: "row", borderRadius: 8, borderWidth: StyleSheet.hairlineWidth, padding: 3 },
  item: { flex: 1, minHeight: 34, borderRadius: 6, alignItems: "center", justifyContent: "center" },
  selectedMarker: StyleSheet.absoluteFillObject,
  label: { fontFamily: "Inter_500Medium", fontSize: 13 },
});
