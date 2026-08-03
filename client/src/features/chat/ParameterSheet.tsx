import { Modal, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";

import { Button, Muted, Title } from "@/components/ui";
import { SegmentedControl } from "@/components/SegmentedControl";
import { useTheme } from "@/theme/ThemeProvider";
import type { Bootstrap, SessionSettings } from "@/types/protocol";

function Choice({ label, selected, onPress, testID }: {
  label: string;
  selected: boolean;
  onPress: () => void;
  testID: string;
}) {
  const theme = useTheme();
  return <Pressable testID={testID} onPress={onPress} style={[styles.choice, { borderColor: theme.colors.border,
    backgroundColor: selected ? theme.colors.surfaceAlt : theme.colors.surface }]}>
    <Text style={[styles.choiceText, { color: theme.colors.text }]}>{label}</Text>
    {selected && <View testID={`${testID}:selected`} />}
    {selected && <Text style={{ color: theme.colors.accent }}>✓</Text>}
  </Pressable>;
}

export function ParameterSheet({ visible, bootstrap, environmentId, value, currentRunLabel,
  onChange, onClose }: {
  visible: boolean;
  bootstrap: Bootstrap;
  environmentId: string;
  value: SessionSettings;
  currentRunLabel?: string;
  onChange: (value: SessionSettings) => void;
  onClose: () => void;
}) {
  const theme = useTheme();
  const models = (bootstrap.modelCatalogs[environmentId]?.data ?? []).filter((item) => !item.hidden);
  const model = models.find((item) => item.id === value.model) ?? models[0];
  const supportsFast = model?.serviceTiers.some((tier) => tier.id === "priority" || tier.id === "fast") ||
    model?.additionalSpeedTiers.includes("fast");
  return <Modal testID="parameters:sheet" visible={visible} animationType="slide"
    presentationStyle="pageSheet" onRequestClose={onClose}>
    <SafeAreaView style={[styles.sheet, { backgroundColor: theme.colors.app }]}> 
      <View style={styles.header}>
        <View><Title>会话参数</Title><Muted>{currentRunLabel ?? "发送成功后记为下次默认值"}</Muted></View>
        <Button testID="parameters:done" title="完成" onPress={onClose} />
      </View>
      <ScrollView contentContainerStyle={styles.content}>
        <Title>Agent Profile</Title>
        {bootstrap.agentProfiles.map((profile) => <Choice key={profile.id} label={profile.name}
          testID={`parameters:profile:${encodeURIComponent(profile.id)}`}
          selected={profile.id === value.agentProfileId}
          onPress={() => onChange({ ...value, agentProfileId: profile.id })} />)}
        <Title>模型</Title>
        {models.map((option) => <Choice key={option.id} label={option.displayName}
          testID={`parameters:model:${encodeURIComponent(option.id)}`}
          selected={option.id === value.model} onPress={() => onChange({ ...value, model: option.id,
            reasoningEffort: option.defaultReasoningEffort,
            serviceTier: value.serviceTier === "fast" && !modelSupportsFast(option) ? "standard" : value.serviceTier })} />)}
        {model && <>
          <Title>推理等级</Title>
          <View style={styles.wrap}>{model.supportedReasoningEfforts.map((effort) =>
            <Pressable key={effort.reasoningEffort} testID={`parameters:reasoning:${effort.reasoningEffort}`}
              onPress={() => onChange({ ...value, reasoningEffort: effort.reasoningEffort })}
              style={[styles.chip, { borderColor: theme.colors.border,
                backgroundColor: value.reasoningEffort === effort.reasoningEffort ? theme.colors.accent : theme.colors.surface }]}>
              <Text style={{ color: value.reasoningEffort === effort.reasoningEffort ? theme.colors.accentForeground : theme.colors.text,
                fontFamily: "Inter_500Medium" }}>{effort.reasoningEffort}</Text>
            </Pressable>)}</View>
        </>}
        <Title>速度</Title>
        <SegmentedControl testIDPrefix="parameters:tier" value={value.serviceTier}
          options={supportsFast ? [{ value: "standard", label: "标准" },
            { value: "fast", label: "快速" }] as const :
            [{ value: "standard", label: "标准" }] as const}
          onChange={(serviceTier) => onChange({ ...value, serviceTier })} />
        <Title>模式</Title>
        <SegmentedControl testIDPrefix="parameters:mode" value={value.collaborationMode} options={[
          { value: "default", label: "Default" }, { value: "plan", label: "Plan" },
        ] as const} onChange={(collaborationMode) => onChange({ ...value, collaborationMode })} />
      </ScrollView>
    </SafeAreaView>
  </Modal>;
}

function modelSupportsFast(model: Bootstrap["modelCatalogs"][string]["data"][number]) {
  return model.serviceTiers.some((tier) => tier.id === "priority" || tier.id === "fast") ||
    model.additionalSpeedTiers.includes("fast");
}

const styles = StyleSheet.create({
  sheet: { flex: 1 },
  header: { padding: 16, paddingTop: 24, flexDirection: "row", justifyContent: "space-between", alignItems: "center" },
  content: { padding: 16, paddingBottom: 48, gap: 10 },
  choice: { minHeight: 46, borderWidth: StyleSheet.hairlineWidth, borderRadius: 8,
    paddingHorizontal: 14, flexDirection: "row", alignItems: "center", justifyContent: "space-between" },
  choiceText: { fontFamily: "Inter_500Medium", fontSize: 14 },
  wrap: { flexDirection: "row", flexWrap: "wrap", gap: 8 },
  chip: { borderWidth: StyleSheet.hairlineWidth, borderRadius: 999, paddingHorizontal: 13, paddingVertical: 8 },
});
