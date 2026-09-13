import { Audio } from "expo-av";
import type { AVPlaybackStatus } from "expo-av";
import { useEffect, useRef, useState } from "react";
import { Modal, Pressable, StyleSheet, Text, View } from "react-native";

import { useTheme } from "@/theme/ThemeProvider";
import { findLiveVoice, liveVoices, type LiveVoice } from "./voices";

type PreviewStatus = "idle" | "playing" | "error";

export function LiveVoicePicker({ value, onChange }: {
  value: LiveVoice;
  onChange: (value: LiveVoice) => void;
}) {
  const theme = useTheme();
  const [open, setOpen] = useState(false);
  const [previewStatus, setPreviewStatus] = useState<PreviewStatus>("idle");
  const sound = useRef<Audio.Sound | null>(null);
  const playbackGeneration = useRef(0);
  const selected = findLiveVoice(value);
  const selectedIndex = liveVoices.findIndex((voice) => voice.slug === selected.slug);

  const stopPreview = async (updateState = true) => {
    playbackGeneration.current += 1;
    const current = sound.current;
    sound.current = null;
    if (updateState) setPreviewStatus("idle");
    if (!current) return;
    try { await current.stopAsync(); } catch { /* 已结束的预览无需重复停止。 */ }
    try { await current.unloadAsync(); } catch { /* 已卸载的预览无需重复释放。 */ }
  };

  useEffect(() => () => { void stopPreview(false); }, []);
  useEffect(() => { void stopPreview(); }, [selected.slug]);

  const playPreview = async () => {
    if (previewStatus === "playing") {
      await stopPreview();
      return;
    }
    await stopPreview();
    const generation = playbackGeneration.current;
    try {
      const result = await Audio.Sound.createAsync(selected.preview, { shouldPlay: true });
      if (generation !== playbackGeneration.current) {
        await result.sound.unloadAsync();
        return;
      }
      sound.current = result.sound;
      result.sound.setOnPlaybackStatusUpdate((status: AVPlaybackStatus) => {
        if (generation !== playbackGeneration.current) return;
        if (!status.isLoaded) {
          sound.current = null;
          setPreviewStatus("error");
          return;
        }
        if (status.didJustFinish) {
          sound.current = null;
          setPreviewStatus("idle");
          void result.sound.unloadAsync();
          return;
        }
        setPreviewStatus(status.isPlaying ? "playing" : "idle");
      });
      setPreviewStatus("playing");
    } catch {
      setPreviewStatus("error");
    }
  };

  const close = () => {
    void stopPreview();
    setOpen(false);
  };
  const choose = (index: number) => {
    const next = liveVoices[(index + liveVoices.length) % liveVoices.length];
    if (!next || next.slug === selected.slug) return;
    void stopPreview();
    onChange(next.slug);
  };
  const previous = () => choose(selectedIndex - 1);
  const next = () => choose(selectedIndex + 1);
  const previewLabel = previewStatus === "playing"
    ? `暂停 ${selected.name} 试听`
    : previewStatus === "error"
      ? `重试 ${selected.name} 试听`
      : `播放 ${selected.name} 试听`;

  return <>
    <Pressable testID="live:voice" accessibilityRole="button"
      accessibilityLabel={`音色：${selected.name}`} accessibilityState={{ expanded: open }}
      onPress={() => setOpen(true)}
      style={({ pressed }) => [styles.trigger, { backgroundColor: theme.colors.surface,
        borderColor: theme.colors.border, opacity: pressed ? 0.78 : 1 }]}>
      <View style={styles.triggerCopy}><Text style={[styles.label, { color: theme.colors.textMuted }]}>音色</Text>
        <Text numberOfLines={1} style={[styles.value, { color: theme.colors.text }]}>{selected.name}</Text></View>
      <Text style={[styles.chevron, { color: theme.colors.textMuted }]}>›</Text>
    </Pressable>
    <Modal visible={open} transparent animationType="fade" onRequestClose={close}>
      <Pressable style={[styles.backdrop, { backgroundColor: theme.colors.overlay }]} onPress={close}>
        <Pressable style={[styles.dialog, { backgroundColor: theme.colors.surface, borderColor: theme.colors.border },
          theme.shadow]} onPress={(event) => event.stopPropagation()}>
          <View style={styles.dialogHeader}>
            <Text style={[styles.dialogTitle, { color: theme.colors.text }]}>选择音色</Text>
            <Pressable testID="live:voice:close" accessibilityRole="button"
              accessibilityLabel="关闭音色选择" onPress={close} style={styles.closeButton}>
              <Text style={[styles.closeText, { color: theme.colors.accent }]}>关闭</Text>
            </Pressable>
          </View>
          <View style={styles.preview}>
            <Pressable testID="live:voice:previous" accessibilityRole="button" accessibilityLabel="上一个音色"
              onPress={previous} style={styles.arrow}>
              <Text style={[styles.arrowText, { color: theme.colors.textMuted }]}>‹</Text>
            </Pressable>
            <View style={styles.previewMain}>
              <Pressable testID="live:voice:preview" accessibilityRole="button" accessibilityLabel={previewLabel}
                onPress={() => void playPreview()} style={({ pressed }) => [styles.orb,
                  { backgroundColor: theme.colors.surfaceAlt, borderColor: theme.colors.accent,
                    opacity: pressed ? 0.78 : 1 }]}>
                <Text style={[styles.orbText, { color: theme.colors.text }]}>
                  {previewStatus === "playing" ? "Ⅱ" : previewStatus === "error" ? "↻" : "▶"}
                </Text>
              </Pressable>
              <Text style={[styles.voiceName, { color: theme.colors.text }]}>{selected.name}</Text>
              <Text style={[styles.description, { color: theme.colors.textMuted }]}>{selected.description}</Text>
              {previewStatus === "error" ? <Text accessibilityRole="alert"
                style={[styles.error, { color: theme.colors.danger }]}>试听暂时不可用</Text> : null}
            </View>
            <Pressable testID="live:voice:next" accessibilityRole="button" accessibilityLabel="下一个音色"
              onPress={next} style={styles.arrow}>
              <Text style={[styles.arrowText, { color: theme.colors.textMuted }]}>›</Text>
            </Pressable>
          </View>
          <View accessibilityRole="radiogroup" accessibilityLabel="音色" style={styles.dots}>
            {liveVoices.map((voice) => <Pressable key={voice.slug} testID={`live:voice:option:${voice.slug}`}
              accessibilityRole="radio" accessibilityLabel={`${voice.name}：${voice.description}`}
              accessibilityState={{ selected: voice.slug === selected.slug }}
              onPress={() => choose(liveVoices.indexOf(voice))}
              style={[styles.dot, { backgroundColor: voice.slug === selected.slug ? theme.colors.accent : theme.colors.border }]} />)}
          </View>
        </Pressable>
      </Pressable>
    </Modal>
  </>;
}

const styles = StyleSheet.create({
  trigger: { minHeight: 58, borderWidth: StyleSheet.hairlineWidth, borderRadius: 10,
    paddingHorizontal: 12, paddingVertical: 8, flexDirection: "row", alignItems: "center", gap: 8 },
  triggerCopy: { flex: 1, minWidth: 0, gap: 1 },
  label: { fontSize: 13, lineHeight: 18 },
  value: { fontFamily: "Inter_500Medium", fontSize: 15, lineHeight: 20 },
  chevron: { fontSize: 27, lineHeight: 27 },
  backdrop: { flex: 1, justifyContent: "center", padding: 20 },
  dialog: { width: "100%", borderWidth: StyleSheet.hairlineWidth, borderRadius: 16, padding: 16 },
  dialogHeader: { minHeight: 36, flexDirection: "row", alignItems: "center", justifyContent: "space-between" },
  dialogTitle: { fontFamily: "Inter_600SemiBold", fontSize: 18 },
  closeButton: { minHeight: 40, paddingHorizontal: 8, justifyContent: "center" },
  closeText: { fontFamily: "Inter_500Medium", fontSize: 14 },
  preview: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", gap: 4, marginTop: 18 },
  previewMain: { flex: 1, alignItems: "center", gap: 5 },
  arrow: { width: 42, height: 52, alignItems: "center", justifyContent: "center" },
  arrowText: { fontSize: 34, lineHeight: 38 },
  orb: { width: 150, height: 150, borderRadius: 75, borderWidth: 1,
    alignItems: "center", justifyContent: "center" },
  orbText: { fontSize: 26, lineHeight: 30 },
  voiceName: { marginTop: 8, fontFamily: "Inter_600SemiBold", fontSize: 18 },
  description: { fontSize: 13, lineHeight: 19 },
  error: { fontSize: 12, lineHeight: 18 },
  dots: { flexDirection: "row", justifyContent: "center", gap: 10, marginTop: 18 },
  dot: { width: 10, height: 10, borderRadius: 5 },
});
