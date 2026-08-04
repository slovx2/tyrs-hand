import * as Crypto from "expo-crypto";
import { useEffect, useMemo, useState } from "react";
import { Alert, Pressable, StyleSheet, Text, View } from "react-native";

import { Card, Muted, Title } from "@/components/ui";
import { clearDraft, loadDraft, saveDraft } from "@/db/drafts";
import { ChatComposer } from "@/features/chat/ChatComposer";
import { ParameterSheet } from "@/features/chat/ParameterSheet";
import { useOutbox } from "@/hooks/useOutbox";
import { useAppStore } from "@/store/appStore";
import { enqueueTask, listOutbox, processOutbox, type LocalAttachment } from "@/sync/outbox";
import { useTheme } from "@/theme/ThemeProvider";
import type { Project, SessionSettings } from "@/types/protocol";

export function NewTaskPane({ project, expanded = false, onSubmitted }: {
  project: Project;
  expanded?: boolean;
  onSubmitted?: () => void;
}) {
  const theme = useTheme();
  const connection = useAppStore((state) => state.activeConnection);
  const bootstrap = useAppStore((state) => state.bootstrap);
  const refresh = useAppStore((state) => state.refresh);
  const [text, setText] = useState("");
  const [attachments, setAttachments] = useState<LocalAttachment[]>([]);
  const [showParameters, setShowParameters] = useState(false);
  const [settingsOverride, setSettingsOverride] = useState<SessionSettings | null>(null);
  const [settingsBeforeSheet, setSettingsBeforeSheet] = useState<SessionSettings | null>(null);
  const [draftReady, setDraftReady] = useState(false);
  const outbox = useOutbox(connection?.serverId);
  const defaults = useMemo<SessionSettings | null>(() => {
    if (!bootstrap) return null;
    const profile = bootstrap.agentProfiles[0];
    const models = (bootstrap.modelCatalogs[project.workspaceId]?.data ?? []).filter((item) => !item.hidden);
    const remembered = bootstrap.lastStartedSettings;
    const rememberedModel = models.find((item) => item.id === remembered?.model);
    if (remembered && rememberedModel) return remembered;
    const model = models.find((item) => item.isDefault) ?? models[0];
    if (!profile || !model) return null;
    return {
      agentProfileId: profile.id,
      model: model.id,
      reasoningEffort: model.defaultReasoningEffort,
      serviceTier: model.defaultServiceTier === "priority" || model.defaultServiceTier === "fast" ? "fast" : "standard",
      collaborationMode: "default",
      settingsVersion: 0,
    };
  }, [bootstrap, project.workspaceId]);
  const settings = settingsOverride ?? defaults;
  const draftScope = `project:${project.id}`;

  useEffect(() => {
    if (!connection) return;
    let canceled = false;
    setDraftReady(false);
    setText("");
    setAttachments([]);
    setSettingsOverride(null);
    setSettingsBeforeSheet(null);
    setShowParameters(false);
    void loadDraft(connection.serverId, draftScope).then((draft) => {
      if (canceled) return;
      if (draft) {
        setText(draft.text);
        setAttachments(draft.attachments);
        setSettingsOverride(draft.settings);
      }
      setDraftReady(true);
    });
    return () => { canceled = true; };
  }, [connection, draftScope]);
  useEffect(() => {
    if (!connection || !draftReady) return;
    const timer = setTimeout(() => void saveDraft(connection.serverId, draftScope,
      { text, attachments, settings: settingsOverride }), 150);
    return () => clearTimeout(timer);
  }, [attachments, connection, draftReady, draftScope, settingsOverride, text]);

  if (!connection || !bootstrap) return null;

  const send = async () => {
    if (!settings || !text.trim()) return;
    const localId = Crypto.randomUUID();
    try {
      await enqueueTask({ connection, localId, projectId: project.id, text: text.trim(), settings, attachments });
      await clearDraft(connection.serverId, draftScope);
      setText("");
      setAttachments([]);
      await processOutbox(connection);
      await Promise.all([refresh(), outbox.refresh()]);
      const remaining = await listOutbox(connection.serverId);
      if (!remaining.some((item) => item.localId === localId)) onSubmitted?.();
    } catch (error) {
      Alert.alert("任务已保存在待发送队列", error instanceof Error ? error.message : "稍后会自动重试");
    }
  };
  const pending = outbox.items.filter((item) => item.kind === "create_session" && item.projectId === project.id);

  return <View testID="project:new-task" style={[styles.container, !expanded && styles.mobile,
    expanded && styles.expanded,
    { borderColor: theme.colors.border }]}>
    <View style={styles.heading}><View style={styles.headingCopy}><Title>新任务</Title>
      <Muted numberOfLines={1}>{project.name} · 直接发送即可创建会话</Muted></View></View>
    {expanded && <View style={{ flex: 1 }} />}
    {pending.map((item) => <Card key={item.localId} style={styles.pending}><View
      testID={`outbox:${encodeURIComponent(item.localId)}:${item.status}`}><Muted>{item.status === "failed" ?
      `发送失败：${item.error ?? "未知错误"}` : "任务等待发送…"}</Muted>
      {item.status === "failed" && <View style={styles.actions}>
        <Pressable testID={`outbox:${encodeURIComponent(item.localId)}:retry`}
          onPress={() => void outbox.retry(item.localId).then(() => processOutbox(connection)).then(outbox.refresh)}>
          <Text style={{ color: theme.colors.accent }}>重试</Text></Pressable>
        <Pressable testID={`outbox:${encodeURIComponent(item.localId)}:discard`}
          onPress={() => void outbox.discard(item.localId)}>
          <Text style={{ color: theme.colors.danger }}>丢弃</Text></Pressable>
      </View>}</View></Card>)}
    {settings && <ChatComposer value={text} onChange={setText} attachments={attachments}
      onAttachmentsChange={setAttachments} onParameters={() => {
        setSettingsBeforeSheet(settingsOverride); setShowParameters(true);
      }}
      onSend={() => void send()} sending={false}
      parameterLabel={`${settings.model ?? "默认模型"} · ${settings.reasoningEffort ?? "默认"} · ${settings.collaborationMode}`} />}
    {settings && <ParameterSheet visible={showParameters} bootstrap={bootstrap}
      workspaceId={project.workspaceId} value={settings}
      onChange={setSettingsOverride} onClose={() => setShowParameters(false)}
      onCancel={() => { setSettingsOverride(settingsBeforeSheet); setShowParameters(false); }} />}
  </View>;
}

const styles = StyleSheet.create({
  container: { borderTopWidth: StyleSheet.hairlineWidth },
  mobile: { height: 250, flexShrink: 0 },
  expanded: { flex: 1, borderTopWidth: 0 },
  heading: { paddingHorizontal: 16, paddingTop: 12 },
  headingCopy: { gap: 2 },
  pending: { marginHorizontal: 12, marginTop: 8, padding: 10 },
  actions: { flexDirection: "row", gap: 16, marginTop: 4 },
});
