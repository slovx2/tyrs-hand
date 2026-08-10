import type { Model } from "@codex-app-server/v2/Model";
import type { TurnPreferences } from "@/app-server/officialClient";
import { useEffect, useMemo, useState } from "react";
import { Alert, KeyboardAvoidingView, Platform, Pressable, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import type { LocalAttachment } from "@/app-server/attachments";
import { resolveNewTaskPreferences } from "@/app-server/preferences";
import { targetKey, type MobileProject } from "@/app-server/types";
import { Muted, Title } from "@/components/ui";
import { clearDraft, loadDraft, saveDraft } from "@/db/drafts";
import { loadLastTurnPreferences } from "@/db/settings";
import { ChatComposer } from "@/features/chat/ChatComposer";
import { ParameterSheet } from "@/features/chat/ParameterSheet";
import { useKeyboardVisible } from "@/hooks/useKeyboardVisible";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import { keyboardAvoidance } from "@/utils/keyboardAvoidance";

const EMPTY_MODELS: Model[] = [];

export function NewTaskPane({ project, expanded = false, onSubmitted }: {
  project: MobileProject;
  expanded?: boolean;
  onSubmitted?: (threadId: string) => void;
}) {
  const theme = useTheme();
  const insets = useSafeAreaInsets();
  const keyboardVisible = useKeyboardVisible();
  const connection = useAppStore((state) => state.activeConnection);
  const modelsByTarget = useAppStore((state) => state.modelsByTarget);
  const startTask = useAppStore((state) => state.startTask);
  const retryOutbox = useAppStore((state) => state.retryOutbox);
  const discardOutbox = useAppStore((state) => state.discardOutbox);
  const queued = useAppStore((state) => state.outbox.filter((item) =>
    item.kind === "create_task" && item.projectId === project.id));
  const [text, setText] = useState("");
  const [attachments, setAttachments] = useState<LocalAttachment[]>([]);
  const [showParameters, setShowParameters] = useState(false);
  const [preferences, setPreferences] = useState<TurnPreferences | null>(null);
  const [rememberedPreferences, setRememberedPreferences] = useState<TurnPreferences | null>(null);
  const [beforeSheet, setBeforeSheet] = useState<TurnPreferences | null>(null);
  const [draftReady, setDraftReady] = useState(false);
  const [sending, setSending] = useState(false);
  const profileId = connection?.profileId ?? null;
  const models = profileId
    ? modelsByTarget[targetKey(profileId, project.workspaceId)] ?? EMPTY_MODELS
    : EMPTY_MODELS;
  const fallback = useMemo(() => resolveNewTaskPreferences(models, rememberedPreferences),
    [models, rememberedPreferences]);
  const resolvedPreferences = preferences
    ? resolveNewTaskPreferences(models, preferences) : fallback;
  const draftScope = `project:${project.id}`;

  useEffect(() => {
    if (!profileId) return;
    let canceled = false;
    setDraftReady(false); setText(""); setAttachments([]); setPreferences(null);
    setRememberedPreferences(null);
    void Promise.all([loadDraft(profileId, draftScope), loadLastTurnPreferences(profileId)])
      .then(([draft, remembered]) => {
      if (canceled) return;
      setRememberedPreferences(remembered);
      if (draft) {
        setText(draft.text); setAttachments(draft.attachments); setPreferences(draft.settings);
      }
      setDraftReady(true);
    });
    return () => { canceled = true; };
  }, [draftScope, profileId]);

  useEffect(() => {
    if (!profileId || !draftReady) return;
    const timer = setTimeout(() => void saveDraft(profileId, draftScope,
      { text, attachments, settings: preferences }), 150);
    return () => clearTimeout(timer);
  }, [attachments, draftReady, draftScope, preferences, profileId, text]);

  if (!connection) return null;
  const send = async () => {
    if (!resolvedPreferences || (!text.trim() && attachments.length === 0)) return;
    setSending(true);
    try {
      const threadId = await startTask(project.id, text, attachments, resolvedPreferences);
      await clearDraft(connection.profileId, draftScope);
      setText(""); setAttachments([]);
      if (threadId) onSubmitted?.(threadId);
      else Alert.alert("已加入发送队列", "连接恢复后会自动创建任务并发送，无需重新输入。");
    } catch (cause) {
      Alert.alert("暂时无法发送", cause instanceof Error ? cause.message : "请检查连接后重试");
    } finally {
      setSending(false);
    }
  };

  return <KeyboardAvoidingView {...keyboardAvoidance(Platform.OS, insets.top, keyboardVisible)}
    testID="project:new-task" style={[styles.container, !expanded && styles.mobile,
      expanded && styles.expanded, { borderColor: theme.colors.border }]}>
    <View style={styles.heading}><View style={styles.headingCopy}><Title>新任务</Title>
      <Muted numberOfLines={1}>{project.name}</Muted></View></View>
    {queued.length > 0 && <Pressable style={[styles.outbox, {
      backgroundColor: theme.colors.surfaceAlt }]} onPress={() => {
        const failed = queued.find((item) => item.state === "failed");
        if (!failed) { void retryOutbox(); return; }
        Alert.alert("任务发送失败", failed.error ?? "连接恢复后可重试", [
          { text: "取消", style: "cancel" },
          { text: "丢弃", style: "destructive",
            onPress: () => void discardOutbox(failed.clientMessageId) },
          { text: "重试", onPress: () => void retryOutbox(failed.clientMessageId) },
        ]);
      }}>
      <Text style={{ color: queued.some((item) => item.state === "failed")
        ? theme.colors.danger : theme.colors.textMuted }}>
        {queued.some((item) => item.state === "failed")
          ? `${queued.length} 条任务等待处理，点按查看`
          : `${queued.length} 条任务正在等待网络`}
      </Text>
    </Pressable>}
    {expanded && <View style={{ flex: 1 }} />}
    <ChatComposer value={text} onChange={setText} attachments={attachments}
      onAttachmentsChange={setAttachments} onParameters={() => {
        if (!resolvedPreferences) {
          Alert.alert("参数暂不可用", "Codex App Server 没有返回可用模型。");
          return;
        }
        setBeforeSheet(preferences); setPreferences(resolvedPreferences); setShowParameters(true);
      }} onSend={() => void send()} sending={sending}
      parameterLabel={resolvedPreferences
        ? `${resolvedPreferences.model} · ${resolvedPreferences.effort ?? "默认"} · ${resolvedPreferences.collaborationMode === "plan" ? "先做计划" : "直接执行"}`
        : "参数暂不可用"} />
    {resolvedPreferences && <ParameterSheet visible={showParameters} models={models}
      value={resolvedPreferences} onChange={setPreferences}
      onClose={() => setShowParameters(false)}
      onCancel={() => { setPreferences(beforeSheet); setShowParameters(false); }} />}
  </KeyboardAvoidingView>;
}

const styles = StyleSheet.create({
  container: { borderTopWidth: StyleSheet.hairlineWidth },
  mobile: { height: 250, flexShrink: 0 },
  expanded: { flex: 1, borderTopWidth: 0 },
  heading: { paddingHorizontal: 16, paddingTop: 12 },
  headingCopy: { gap: 2 },
  outbox: { marginHorizontal: 16, marginTop: 8, minHeight: 34, borderRadius: 8,
    paddingHorizontal: 10, alignItems: "center", justifyContent: "center" },
});
