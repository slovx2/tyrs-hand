import type { Model } from "@codex-app-server/v2/Model";
import { Modal, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";

import type { TurnPreferences } from "@/app-server/officialClient";
import { SegmentedControl } from "@/components/SegmentedControl";
import { Button, Muted, Title } from "@/components/ui";
import { useTheme } from "@/theme/ThemeProvider";

function Choice({ label, selected, onPress, testID }: {
  label: string;
  selected: boolean;
  onPress: () => void;
  testID: string;
}) {
  const theme = useTheme();
  return <Pressable testID={testID} onPress={onPress}
    accessibilityRole="button" accessibilityState={{ selected }} style={[styles.choice,
    { borderColor: theme.colors.border,
      backgroundColor: selected ? theme.colors.surfaceAlt : theme.colors.surface }]}>
    <Text style={[styles.choiceText, { color: theme.colors.text }]}>{label}</Text>
    {selected && <View pointerEvents="none" testID={`${testID}:selected`}
      style={styles.selectedMarker} />}
    {selected && <Text style={{ color: theme.colors.accent }}>✓</Text>}
  </Pressable>;
}

export function ParameterSheet({ visible, models, value, onChange, onClose, onCancel }: {
  visible: boolean;
  models: Model[];
  value: TurnPreferences;
  onChange: (value: TurnPreferences) => void;
  onClose: () => void;
  onCancel?: () => void;
}) {
  const theme = useTheme();
  const visibleModels = models.filter((item) => !item.hidden);
  const model = visibleModels.find((item) => item.id === value.model) ?? visibleModels[0];
  return <Modal testID="parameters:sheet" visible={visible} animationType="slide"
    presentationStyle="pageSheet" onRequestClose={onCancel ?? onClose}>
    <SafeAreaView style={[styles.sheet, { backgroundColor: theme.colors.app }]}> 
      <View style={styles.header}>
        <View><Title>会话参数</Title><Muted>参数由当前 Codex App Server 提供</Muted></View>
        <View style={styles.headerActions}>
          <Button testID="parameters:cancel" title="取消" variant="secondary"
            onPress={onCancel ?? onClose} />
          <Button testID="parameters:done" title="完成" onPress={onClose} />
        </View>
      </View>
      <ScrollView contentContainerStyle={styles.content}>
        <Title>模型</Title>
        {visibleModels.map((option) => <Choice key={option.id} label={option.displayName}
          testID={`parameters:model:${encodeURIComponent(option.id)}`}
          selected={option.id === value.model} onPress={() => onChange({ ...value,
            model: option.id, effort: option.defaultReasoningEffort,
            serviceTier: option.defaultServiceTier })} />)}
        {model && <>
          <Title>推理等级</Title>
          <View style={styles.wrap}>{model.supportedReasoningEfforts.map((option) => {
            const selected = value.effort === option.reasoningEffort;
            const testID = `parameters:reasoning:${option.reasoningEffort}`;
            return <Pressable key={option.reasoningEffort} testID={testID}
              accessibilityRole="button" accessibilityState={{ selected }}
              onPress={() => onChange({ ...value, effort: option.reasoningEffort })}
              style={[styles.chip, { borderColor: theme.colors.border,
                backgroundColor: selected ? theme.colors.accent : theme.colors.surface }]}>
              <Text style={{ color: selected ? theme.colors.accentForeground : theme.colors.text,
                fontFamily: "Inter_500Medium" }}>{option.reasoningEffort}</Text>
              {selected && <View pointerEvents="none" testID={`${testID}:selected`}
                style={styles.selectedMarker} />}
            </Pressable>;
          })}</View>
          {model.serviceTiers.length > 0 && <>
            <Title>服务等级</Title>
            <Choice label="使用服务器默认值" testID="parameters:tier:default"
              selected={value.serviceTier === null}
              onPress={() => onChange({ ...value, serviceTier: null })} />
            {model.serviceTiers.map((tier) => <Choice key={tier.id} label={tier.name}
              testID={`parameters:tier:${encodeURIComponent(tier.id)}`}
              selected={value.serviceTier === tier.id}
              onPress={() => onChange({ ...value, serviceTier: tier.id })} />)}
          </>}
        </>}
        <Title>模式</Title>
        <SegmentedControl testIDPrefix="parameters:mode" value={value.collaborationMode}
          options={[{ value: "default", label: "直接执行" },
            { value: "plan", label: "先做计划" }] as const}
          onChange={(collaborationMode) => onChange({ ...value, collaborationMode })} />
      </ScrollView>
    </SafeAreaView>
  </Modal>;
}

const styles = StyleSheet.create({
  sheet: { flex: 1 },
  header: { padding: 16, paddingTop: 24, flexDirection: "row", justifyContent: "space-between",
    alignItems: "center", gap: 12 },
  headerActions: { flexDirection: "row", gap: 8 },
  content: { padding: 16, paddingBottom: 48, gap: 10 },
  choice: { minHeight: 46, borderWidth: StyleSheet.hairlineWidth, borderRadius: 8,
    paddingHorizontal: 14, flexDirection: "row", alignItems: "center",
    justifyContent: "space-between" },
  choiceText: { fontFamily: "Inter_500Medium", fontSize: 14 },
  selectedMarker: StyleSheet.absoluteFillObject,
  wrap: { flexDirection: "row", flexWrap: "wrap", gap: 8 },
  chip: { borderWidth: StyleSheet.hairlineWidth, borderRadius: 999,
    paddingHorizontal: 13, paddingVertical: 8 },
});
