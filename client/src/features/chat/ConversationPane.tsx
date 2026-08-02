import * as Crypto from "expo-crypto";
import { FlashList, type FlashListRef } from "@shopify/flash-list";
import { useCallback, useEffect, useRef, useState } from "react";
import { Alert, Modal, Pressable, StyleSheet, Text, TextInput, View } from "react-native";

import { ClientApi } from "@/api/client";
import { loadCachedMessages, saveMessages } from "@/db/cache";
import { Button, EmptyState, Muted, Title } from "@/components/ui";
import { useOutbox } from "@/hooks/useOutbox";
import { useAppStore } from "@/store/appStore";
import { enqueueMessage, processOutbox, type LocalAttachment } from "@/sync/outbox";
import { subscribeToUpdates, type SyncEvent } from "@/sync/synchronizer";
import { useTheme } from "@/theme/ThemeProvider";
import { type Message, type RunSnapshot, type SessionSettings } from "@/types/protocol";
import { ChatComposer } from "./ChatComposer";
import { MessageBubble } from "./MessageBubble";
import { ParameterSheet } from "./ParameterSheet";
import { InteractiveCard, PlanCard, RunProgressCard } from "./RunCards";

export function ConversationPane({ sessionId }: { sessionId: string }) {
  const theme = useTheme();
  const connection = useAppStore((state) => state.activeConnection);
  const bootstrap = useAppStore((state) => state.bootstrap);
  const session = useAppStore((state) => state.sessions.find((item) => item.id === sessionId));
  const refreshSessions = useAppStore((state) => state.refresh);
  const [messages, setMessages] = useState<Message[]>([]);
  const [text, setText] = useState("");
  const [attachments, setAttachments] = useState<LocalAttachment[]>([]);
  const [settings, setSettings] = useState<SessionSettings | null>(null);
  const [savedSettings, setSavedSettings] = useState<SessionSettings | null>(null);
  const [currentRun, setCurrentRun] = useState<RunSnapshot | null>(null);
  const [liveEvents, setLiveEvents] = useState<SyncEvent[]>([]);
  const [showParameters, setShowParameters] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [title, setTitle] = useState("");
  const list = useRef<FlashListRef<Message>>(null);
  const outbox = useOutbox(connection?.serverId, sessionId);
  const load = useCallback(async (beforeSeq?: number) => {
    if (!connection) return;
    const api = new ClientApi(connection);
    if (beforeSeq === undefined) {
      const cached = await loadCachedMessages(connection.serverId, sessionId);
      if (cached.length > 0) setMessages(cached);
      const detail = await api.getSession(sessionId);
      setSettings(detail.settings); setSavedSettings(detail.settings);
      setCurrentRun(detail.currentRun);
    }
    const page = await api.listMessages(sessionId,
      beforeSeq === undefined ? { limit: 80 } : { beforeSeq, limit: 80 });
    await saveMessages(connection.serverId, page.messages);
    setMessages((current) => beforeSeq === undefined ? page.messages :
      [...page.messages, ...current.filter((item) => !page.messages.some((next) => next.id === item.id))]);
    setHasMore(page.hasMoreBefore);
  }, [connection, sessionId]);
  useEffect(() => { void load(); }, [load]);
  useEffect(() => subscribeToUpdates((event) => {
    if (event.sessionId !== sessionId) return;
    if (event.kind === "live") setLiveEvents((items) => [...items.slice(-49), event]);
    else void load();
  }), [load, sessionId]);
  if (!connection || !bootstrap || !session || !settings) {
    return <EmptyState title="正在载入会话" detail="先显示本地缓存，再从 Control 补齐最新消息。" />;
  }
  const send = async () => {
    if (!text.trim()) return;
    await enqueueMessage({ connection, localId: Crypto.randomUUID(), sessionId,
      text: text.trim(), attachments });
    setText(""); setAttachments([]);
    await outbox.refresh();
    await processOutbox(connection);
    await Promise.all([load(), outbox.refresh(), refreshSessions()]);
  };
  const closeParameters = async () => {
    setShowParameters(false);
    if (!savedSettings || JSON.stringify(savedSettings) === JSON.stringify(settings)) return;
    try {
      const updated = await new ClientApi(connection).patchSession(sessionId, {
        agentProfileId: settings.agentProfileId, model: settings.model,
        reasoningEffort: settings.reasoningEffort, serviceTier: settings.serviceTier,
        collaborationMode: settings.collaborationMode,
        expectedSettingsVersion: savedSettings.settingsVersion,
      });
      const next = { ...settings, settingsVersion: updated.settingsVersion };
      setSettings(next); setSavedSettings(next);
      await refreshSessions();
    } catch (error) {
      setSettings(savedSettings);
      Alert.alert("参数没有保存", error instanceof Error ? error.message : "请重试");
    }
  };
  const action = async (value: "stop" | "archive" | "restore") => {
    try { await new ClientApi(connection).action(sessionId, value); await refreshSessions(); }
    catch (error) { Alert.alert("操作失败", error instanceof Error ? error.message : "请重试"); }
  };
  const running = currentRun && ["starting", "running", "waiting_for_user", "reconciling"]
    .includes(String(currentRun.status));
  return <View style={{ flex: 1 }}>
    <View testID="session:toolbar" style={[styles.toolbar, { borderBottomColor: theme.colors.border }]}> 
      <View style={{ flex: 1 }}><Title>{session.title}</Title>
        <View testID={running ? "session:next-turn-settings" : "session:idle-settings"}>
          <Muted>{running ? "正在运行 · 参数修改将在下一轮生效" : `${session.serviceTier} · ${session.collaborationMode}`}</Muted>
        </View></View>
      {running && <Button testID="session:stop" title="停止" variant="danger" onPress={() => void action("stop")} />}
      <Pressable testID="session:rename" onPress={() => { setTitle(session.title); setRenaming(true); }}>
        <Text style={{ color: theme.colors.textMuted, padding: 10 }}>改名</Text>
      </Pressable>
      <Pressable testID={session.lifecycleState === "archived" ? "session:restore" : "session:archive"}
        onPress={() => void action(session.lifecycleState === "archived" ? "restore" : "archive")}> 
        <Text style={{ color: theme.colors.textMuted, padding: 10 }}>{session.lifecycleState === "archived" ? "恢复" : "归档"}</Text>
      </Pressable>
    </View>
    <FlashList ref={list} testID="messages:list" data={messages} keyExtractor={(item) => item.id}
      renderItem={({ item }) => <MessageBubble message={item} />}
      maintainVisibleContentPosition={{ autoscrollToBottomThreshold: 0.1 }}
      onStartReached={() => { if (hasMore && messages[0]) void load(messages[0].seq); }}
      onStartReachedThreshold={0.2} contentContainerStyle={{ paddingVertical: 10 }}
      ListFooterComponent={<>{currentRun && <RunProgressCard run={currentRun} />}
        {currentRun && <PlanCard run={currentRun} onExecute={() => void new ClientApi(connection)
          .executePlan(sessionId, currentRun.id).then(() => load()).catch((error: unknown) =>
            Alert.alert("执行 Plan 失败", error instanceof Error ? error.message : "请重试"))} />}
        {currentRun?.pendingInteractives.map((interactive) => <InteractiveCard key={interactive.id}
          interactive={interactive} onSubmit={(answer) => void new ClientApi(connection)
            .answerInteractive(interactive.id, answer).then(() => load()).catch((error: unknown) =>
              Alert.alert("提交回答失败", error instanceof Error ? error.message : "请重试"))} />)}</>} />
    {liveEvents.length > 0 && <View testID="run:live" style={[styles.progress, { backgroundColor: theme.colors.surfaceAlt }]}> 
      <Muted>实时进度 · {liveEvents[liveEvents.length - 1]?.type}</Muted>
    </View>}
    {outbox.items.map((item) => <View key={item.localId}
      testID={`outbox:${encodeURIComponent(item.localId)}:${item.status}`}
      style={[styles.outbox, { borderColor: theme.colors.border }]}> 
      <Muted>{item.status === "failed" ? `发送失败：${item.error ?? "未知错误"}` : "等待发送…"}</Muted>
      {item.status === "failed" && <><Button testID={`outbox:${encodeURIComponent(item.localId)}:retry`}
        title="重试" variant="secondary" onPress={() => void outbox.retry(item.localId)
        .then(() => processOutbox(connection)).then(outbox.refresh)} /><Button
          testID={`outbox:${encodeURIComponent(item.localId)}:discard`} title="丢弃" variant="danger"
          onPress={() => void outbox.discard(item.localId)} /></>}
    </View>)}
    <ChatComposer value={text} onChange={setText} attachments={attachments}
      onAttachmentsChange={setAttachments} onParameters={() => setShowParameters(true)}
      onSend={() => void send()} sending={false}
      parameterLabel={`${settings.model ?? "默认模型"} · ${settings.reasoningEffort ?? "默认"} · ${settings.collaborationMode}`} />
    <ParameterSheet visible={showParameters} bootstrap={bootstrap} value={settings}
      currentRunLabel={running ? "当前 Run 使用已冻结参数；修改将在下一轮生效" : "当前会话参数"}
      onChange={setSettings} onClose={() => void closeParameters()} />
    <Modal visible={renaming} transparent animationType="fade" onRequestClose={() => setRenaming(false)}>
      <View style={[styles.modal, { backgroundColor: theme.colors.overlay }]}><View style={[styles.rename,
        { backgroundColor: theme.colors.surface, borderColor: theme.colors.border }]}>
        <Title>修改会话名称</Title>
        <TextInput testID="session:rename:input" autoFocus value={title} onChangeText={setTitle} maxLength={120}
          style={[styles.titleInput, { color: theme.colors.text, borderColor: theme.colors.border }]} />
        <View style={{ flexDirection: "row", gap: 8, justifyContent: "flex-end" }}>
          <Button title="取消" variant="secondary" onPress={() => setRenaming(false)} />
          <Button testID="session:rename:save" title="保存" disabled={!title.trim()} onPress={() => void new ClientApi(connection)
            .patchSession(sessionId, { title: title.trim() }).then(() => refreshSessions())
            .then(() => setRenaming(false)).catch((error: unknown) =>
              Alert.alert("改名失败", error instanceof Error ? error.message : "请重试"))} />
        </View>
      </View></View>
    </Modal>
  </View>;
}

const styles = StyleSheet.create({
  toolbar: { minHeight: 64, paddingHorizontal: 14, borderBottomWidth: StyleSheet.hairlineWidth,
    flexDirection: "row", alignItems: "center", gap: 8 },
  progress: { marginHorizontal: 12, paddingHorizontal: 12, paddingVertical: 8, borderRadius: 8 },
  outbox: { marginHorizontal: 12, marginTop: 6, borderWidth: StyleSheet.hairlineWidth,
    borderRadius: 8, padding: 8, flexDirection: "row", alignItems: "center", gap: 8 },
  modal: { flex: 1, justifyContent: "center", padding: 24 },
  rename: { borderWidth: StyleSheet.hairlineWidth, borderRadius: 12, padding: 16, gap: 12 },
  titleInput: { minHeight: 44, borderWidth: StyleSheet.hairlineWidth, borderRadius: 8,
    paddingHorizontal: 12, fontFamily: "Inter_400Regular" },
});
