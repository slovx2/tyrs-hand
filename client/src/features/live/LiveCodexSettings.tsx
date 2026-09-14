import { useEffect, useMemo, useState } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";

import type { Model } from "@codex-app-server/v2/Model";
import { targetKey } from "@/app-server/types";
import { Dropdown } from "@/components/Dropdown";
import { Card, Muted, Title } from "@/components/ui";
import { useTheme } from "@/theme/ThemeProvider";
import { loadLiveCodexPreferences, saveLiveCodexPreferences } from "@/db/settings";
import { resolveLiveCodexPreferences, type LiveCodexPreferences } from "@/features/live/liveCodexPreferences";
import { useAppStore } from "@/store/appStore";

export function LiveCodexSettings() {
  const theme = useTheme();
  const connection = useAppStore((state) => state.activeConnection);
  const modelsByTarget = useAppStore((state) => state.modelsByTarget);
  const models = useMemo(() => {
    if (!connection) return [] as Model[];
    return modelsByTarget[targetKey(connection.profileId, null)] ?? [];
  }, [connection, modelsByTarget]);
  const [value, setValue] = useState<LiveCodexPreferences>(
    resolveLiveCodexPreferences([], null));

  useEffect(() => {
    if (!connection) {
      setValue(resolveLiveCodexPreferences(models, null));
      return;
    }
    let cancelled = false;
    void loadLiveCodexPreferences(connection.profileId).then((stored) => {
      if (!cancelled) setValue(resolveLiveCodexPreferences(models, stored));
    });
    return () => { cancelled = true; };
  }, [connection, models]);

  const selected = models.find((item) => item.id === value.model);
  const efforts = selected?.supportedReasoningEfforts ?? [];
  const update = (next: LiveCodexPreferences) => {
    const resolved = resolveLiveCodexPreferences(models, next);
    setValue(resolved);
    if (connection) void saveLiveCodexPreferences(connection.profileId, resolved);
  };

  return <Card testID="settings:live-codex" style={styles.card}>
    <Title>Live Codex</Title>
    <Muted>新建或重置 Live 时使用的 Worker 模型。默认 gpt-5.6-luna · medium。</Muted>
    {!connection ? <Muted>请先选择一台已关联的机器。</Muted> : models.length === 0
      ? <Muted>连接 Worker 后会拉取可选模型。</Muted>
      : <View style={styles.fields}>
        <Dropdown testID="settings:live-codex:model" label="模型" value={value.model}
          options={models.filter((item) => !item.hidden).map((item) => ({
            value: item.id, label: item.displayName, detail: item.id,
          }))}
          onChange={(model) => update({ model, effort: value.effort })} />
        {efforts.length > 0 ? <View style={styles.effort}>
          <Muted>推理等级</Muted>
          <View style={styles.effortRow}>
            {efforts.map((item) => {
              const selected = value.effort === item.reasoningEffort;
              return <Pressable key={item.reasoningEffort}
                testID={`settings:live-codex:effort:${item.reasoningEffort}`}
                onPress={() => update({ model: value.model, effort: item.reasoningEffort })}
                style={[styles.chip, { borderColor: theme.colors.border,
                  backgroundColor: selected ? theme.colors.accent : theme.colors.surface }]}>
                <Text style={{ color: selected ? theme.colors.accentForeground : theme.colors.text,
                  fontFamily: "Inter_500Medium" }}>{item.reasoningEffort}</Text>
              </Pressable>;
            })}
          </View>
        </View> : null}
      </View>}
  </Card>;
}

const styles = StyleSheet.create({
  card: { marginHorizontal: 16, padding: 16, gap: 8 },
  fields: { gap: 10, marginTop: 4 },
  effort: { gap: 6 },
  effortRow: { flexDirection: "row", flexWrap: "wrap", gap: 8 },
  chip: { minHeight: 34, paddingHorizontal: 12, borderRadius: 8, borderWidth: StyleSheet.hairlineWidth,
    alignItems: "center", justifyContent: "center" },
});
