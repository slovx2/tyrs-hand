import * as Crypto from "expo-crypto";
import { useEffect, useMemo, useState } from "react";
import { Alert, FlatList, Pressable, StyleSheet, Text, View } from "react-native";

import { Card, EmptyState, Muted, Screen, StatusDot, Title } from "@/components/ui";
import { ChatComposer } from "@/features/chat/ChatComposer";
import { ParameterSheet } from "@/features/chat/ParameterSheet";
import { useTablet } from "@/hooks/useTablet";
import { useOutbox } from "@/hooks/useOutbox";
import { useAppStore } from "@/store/appStore";
import { enqueueTask, processOutbox, type LocalAttachment } from "@/sync/outbox";
import { useTheme } from "@/theme/ThemeProvider";
import type { SessionSettings } from "@/types/protocol";

export default function ProjectsScreen() {
  const theme = useTheme();
  const tablet = useTablet();
  const connection = useAppStore((state) => state.activeConnection);
  const bootstrap = useAppStore((state) => state.bootstrap);
  const selectedProjectId = useAppStore((state) => state.selectedProjectId);
  const selectProject = useAppStore((state) => state.setSelectedProject);
  const refresh = useAppStore((state) => state.refresh);
  const [text, setText] = useState("");
  const [attachments, setAttachments] = useState<LocalAttachment[]>([]);
  const [showParameters, setShowParameters] = useState(false);
  const outbox = useOutbox(connection?.serverId);
  const defaults = useMemo<SessionSettings | null>(() => {
    if (!bootstrap) return null;
    if (bootstrap.lastStartedSettings) return bootstrap.lastStartedSettings;
    const profile = bootstrap.agentProfiles[0];
    const model = bootstrap.modelCatalog.find((item) => item.default) ?? bootstrap.modelCatalog[0];
    if (!profile || !model) return null;
    return { agentProfileId: profile.id, model: model.id,
      reasoningEffort: model.defaultReasoningEffort as SessionSettings["reasoningEffort"],
      serviceTier: "standard", collaborationMode: "default", settingsVersion: 0 };
  }, [bootstrap]);
  const [settingsOverride, setSettingsOverride] = useState<SessionSettings | null>(null);
  const settings = settingsOverride ?? defaults;
  const selected = bootstrap?.projects.find((project) => project.id === selectedProjectId) ?? null;
  useEffect(() => {
    setText("");
    setAttachments([]);
    setSettingsOverride(null);
    setShowParameters(false);
  }, [connection?.serverId]);
  if (!connection || !bootstrap) {
    return <Screen><EmptyState title="还没有可用的 Control" detail="请先在连接页扫描管理后台中的设备二维码。" /></Screen>;
  }
  const send = async () => {
    if (!selected || !settings || !text.trim()) return;
    const localId = Crypto.randomUUID();
    try {
      await enqueueTask({ connection, localId, projectId: selected.id, text: text.trim(),
        settings, attachments });
      setText(""); setAttachments([]);
      await processOutbox(connection);
      await Promise.all([refresh(), outbox.refresh()]);
    } catch (error) {
      Alert.alert("任务已保存在待发送队列", error instanceof Error ? error.message : "稍后会自动重试");
    }
  };
  const projectList = <FlatList testID="projects:list" data={bootstrap.projects} keyExtractor={(item) => item.id}
    contentContainerStyle={styles.list} renderItem={({ item, index }) => {
      const active = item.id === selectedProjectId;
      return <Pressable testID={`project:row:${index}`} onPress={() => selectProject(item.id)}>
        <Card style={[styles.project, active && { borderColor: theme.colors.accent, borderWidth: 1.5 }]}
          testID={`project:${encodeURIComponent(item.id)}`}> 
          <View style={styles.row}><StatusDot status={item.availabilityStatus === "available" ? "success" : "warning"} />
            <Title>{item.name}</Title></View>
          <Muted numberOfLines={1}>{item.relativePath}{item.branch ? ` · ${item.branch}` : ""}{item.dirty ? " · 有修改" : ""}</Muted>
        </Card>
      </Pressable>;
    }} />;
  const composer = <View style={[styles.composerPane, tablet && { borderLeftColor: theme.colors.border }]}>
    <View style={styles.context}>
      <Text style={[styles.eyebrow, { color: theme.colors.textMuted }]}>新任务</Text>
      <Title>{selected?.name ?? "先选择一个项目"}</Title>
      <Muted>直接输入任务内容，不需要填写标题。首次发送会原子创建会话并启动 Turn。</Muted>
    </View>
    <View style={{ flex: 1 }} />
    {outbox.items.filter((item) => item.kind === "create_session").map((item) =>
      <Card key={item.localId} style={styles.pending}><View
        testID={`outbox:${encodeURIComponent(item.localId)}:${item.status}`}><Muted>{item.status === "failed" ?
        `发送失败：${item.error ?? "未知错误"}` : "任务等待发送…"}</Muted>
        {item.status === "failed" && <View style={styles.row}>
          <Pressable testID={`outbox:${encodeURIComponent(item.localId)}:retry`}
            onPress={() => void outbox.retry(item.localId).then(() => processOutbox(connection)).then(outbox.refresh)}>
            <Text style={{ color: theme.colors.accent }}>重试</Text></Pressable>
          <Pressable testID={`outbox:${encodeURIComponent(item.localId)}:discard`}
            onPress={() => void outbox.discard(item.localId)}>
            <Text style={{ color: theme.colors.danger }}>丢弃</Text></Pressable>
        </View>}</View>
      </Card>)}
    {settings && <ChatComposer value={text} onChange={setText} attachments={attachments}
      onAttachmentsChange={setAttachments} onParameters={() => setShowParameters(true)}
      onSend={() => void send()} sending={false}
      parameterLabel={`${settings.model ?? "默认模型"} · ${settings.reasoningEffort ?? "默认"} · ${settings.collaborationMode}`} />}
  </View>;
  return <Screen style={tablet ? styles.horizontal : undefined}>
    <View style={tablet ? styles.master : styles.mobileList}>{projectList}</View>
    {composer}
    {settings && <ParameterSheet visible={showParameters} bootstrap={bootstrap} value={settings}
      onChange={setSettingsOverride} onClose={() => setShowParameters(false)} />}
  </Screen>;
}

const styles = StyleSheet.create({
  horizontal: { flexDirection: "row" }, master: { width: 340 }, mobileList: { maxHeight: "52%" },
  list: { padding: 12, gap: 8 }, project: { gap: 6 }, row: { flexDirection: "row", gap: 9, alignItems: "center" },
  composerPane: { flex: 1, borderLeftWidth: StyleSheet.hairlineWidth },
  context: { padding: 20, gap: 5 }, eyebrow: { fontFamily: "Inter_600SemiBold", fontSize: 12,
    textTransform: "uppercase", letterSpacing: 0.8 },
  pending: { marginHorizontal: 12, marginBottom: 6, padding: 10, flexDirection: "row",
    alignItems: "center", justifyContent: "space-between" },
});
