import * as Crypto from "expo-crypto";
import { FlashList, type FlashListRef } from "@shopify/flash-list";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Alert, Modal, Pressable, StyleSheet, Text, TextInput, View } from "react-native";

import { ClientApi } from "@/api/client";
import { loadCachedMessages, saveMessages } from "@/db/cache";
import { clearDraft, loadDraft, saveDraft } from "@/db/drafts";
import { Button, EmptyState, Muted, Title } from "@/components/ui";
import { useOutbox } from "@/hooks/useOutbox";
import { useAppStore } from "@/store/appStore";
import { enqueueMessage, processOutbox, type LocalAttachment } from "@/sync/outbox";
import { subscribeToUpdates, type SyncEvent } from "@/sync/synchronizer";
import { useTheme } from "@/theme/ThemeProvider";
import { type Message, type RunSnapshot, type SessionSettings } from "@/types/protocol";
import { ChatComposer } from "./ChatComposer";
import { MessageBubble } from "./MessageBubble";
import { mergeMessages } from "./messagePagination";
import { ParameterSheet } from "./ParameterSheet";
import { InteractiveCard, PlanCard, RunProgressCard } from "./RunCards";

export function ConversationPane({ sessionId }: { sessionId: string }) {
  const theme = useTheme();
  const connection = useAppStore((state) => state.activeConnection);
  const bootstrap = useAppStore((state) => state.bootstrap);
  const session = useAppStore((state) => state.sessions.find((item) => item.id === sessionId));
  const refreshSessions = useAppStore((state) => state.refresh);
  const [messages, setMessages] = useState<Message[]>([]);
  const [messagesReady, setMessagesReady] = useState(false);
  const [initialSyncComplete, setInitialSyncComplete] = useState(false);
  const [text, setText] = useState("");
  const [attachments, setAttachments] = useState<LocalAttachment[]>([]);
  const [settings, setSettings] = useState<SessionSettings | null>(null);
  const [savedSettings, setSavedSettings] = useState<SessionSettings | null>(null);
  const [currentRun, setCurrentRun] = useState<RunSnapshot | null>(null);
  const [liveEvents, setLiveEvents] = useState<SyncEvent[]>([]);
  const [showParameters, setShowParameters] = useState(false);
  const [settingsBeforeSheet, setSettingsBeforeSheet] = useState<SessionSettings | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [title, setTitle] = useState("");
  const [draftReady, setDraftReady] = useState(false);
  const list = useRef<FlashListRef<Message>>(null);
  const refreshPromise = useRef<Promise<void> | null>(null);
  const historyPagingReady = useRef(false);
  const nearBottom = useRef(true);
  const outbox = useOutbox(connection?.serverId, sessionId);
  const draftScope = `session:${sessionId}`;
  const load = useCallback(async (beforeSeq?: number) => {
    if (!connection) return;
    const api = new ClientApi(connection);
    if (beforeSeq === undefined) {
      const cached = await loadCachedMessages(connection.serverId, sessionId);
      if (cached.length > 0) {
        setMessages(cached);
        setMessagesReady(true);
      }
      const detail = await api.getSession(sessionId);
      setSettings(detail.settings); setSavedSettings(detail.settings);
      setCurrentRun(detail.currentRun);
      if (cached.length > 0) {
        let merged = cached;
        let afterSeq = cached.at(-1)!.seq;
        for (;;) {
          const page = await api.listMessages(sessionId, { afterSeq, limit: 80 });
          await saveMessages(connection.serverId, page.messages);
          merged = mergeMessages(merged, page.messages);
          setMessages(merged);
          if (!page.hasMoreAfter || page.messages.length === 0) break;
          afterSeq = page.messages.at(-1)!.seq;
        }
        setHasMore((merged[0]?.seq ?? 0) > 1);
        setInitialSyncComplete(true);
        return;
      }
    }
    const page = await api.listMessages(sessionId,
      beforeSeq === undefined ? { limit: 80 } : { beforeSeq, limit: 80 });
    await saveMessages(connection.serverId, page.messages);
    setMessages((current) => beforeSeq === undefined ? page.messages : mergeMessages(current, page.messages));
    if (beforeSeq === undefined) setMessagesReady(true);
    setHasMore(page.hasMoreBefore);
    if (beforeSeq === undefined) setInitialSyncComplete(true);
  }, [connection, sessionId]);
  const refresh = useCallback(() => {
    if (refreshPromise.current) return refreshPromise.current;
    const pending = load().finally(() => {
      if (refreshPromise.current === pending) refreshPromise.current = null;
    });
    refreshPromise.current = pending;
    return pending;
  }, [load]);
  useEffect(() => { void refresh(); }, [refresh]);
  useEffect(() => {
    setMessages([]);
    setMessagesReady(false);
    setInitialSyncComplete(false);
    historyPagingReady.current = false;
  }, [sessionId]);
  useEffect(() => {
    if (!connection) return;
    let canceled = false;
    setDraftReady(false);
    setText("");
    setAttachments([]);
    void loadDraft(connection.serverId, draftScope).then((draft) => {
      if (canceled) return;
      if (draft) {
        setText(draft.text);
        setAttachments(draft.attachments);
      }
      setDraftReady(true);
    });
    return () => { canceled = true; };
  }, [connection, draftScope]);
  useEffect(() => {
    if (!connection || !draftReady) return;
    const timer = setTimeout(() => void saveDraft(connection.serverId, draftScope,
      { text, attachments, settings: null }), 150);
    return () => clearTimeout(timer);
  }, [attachments, connection, draftReady, draftScope, text]);
  useEffect(() => setLiveEvents([]), [sessionId]);
  useEffect(() => subscribeToUpdates((event) => {
    if (event.sessionId !== sessionId) return;
    if (event.kind === "live") setLiveEvents((items) => [...items.slice(-49), event]);
    else void refresh();
  }), [refresh, sessionId]);
  const presentedRun = useMemo<RunSnapshot | null>(() => {
    if (!currentRun || liveEvents.length === 0) return currentRun;
    const knownSequences = new Set(currentRun.timeline.map((event) => event.sequence));
    const additions = liveEvents.map((event, index) => ({
      sequence: event.runEventSeq ?? currentRun.timeline.length + index + 1,
      type: event.type,
      payload: event.payload,
      occurredAt: new Date().toISOString(),
    })).filter((event) => !knownSequences.has(event.sequence));
    return { ...currentRun, timeline: [...currentRun.timeline, ...additions] };
  }, [currentRun, liveEvents]);
  const running = currentRun && ["starting", "running", "waiting_for_user", "reconciling"]
    .includes(String(currentRun.status));
  useEffect(() => {
    if (!running) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      try {
        await refresh();
      } finally {
        if (!stopped) timer = setTimeout(() => void poll(), 1500);
      }
    };
    timer = setTimeout(() => void poll(), 1500);
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
    };
  }, [refresh, running]);
  const finalMessage = presentedRun?.status === "completed" ?
    [...messages].reverse().find((message) => message.role === "agent") : undefined;
  const finalMessageId = finalMessage?.id;
  useEffect(() => {
    if (!finalMessageId || !nearBottom.current) return;
    let secondFrame = 0;
    const firstFrame = requestAnimationFrame(() => {
      list.current?.scrollToEnd({ animated: false });
      secondFrame = requestAnimationFrame(() => list.current?.scrollToEnd({ animated: true }));
    });
    return () => {
      cancelAnimationFrame(firstFrame);
      if (secondFrame) cancelAnimationFrame(secondFrame);
    };
  }, [finalMessageId]);
  const visibleMessages = finalMessage ? messages.filter((message) => message.id !== finalMessage.id) : messages;
  if (!connection || !bootstrap || !session || !settings || !messagesReady) {
    return <EmptyState title="正在载入会话" detail="先显示本地缓存，再从 Control 补齐最新消息。" />;
  }
  const send = async () => {
    if (!text.trim()) return;
    await enqueueMessage({ connection, localId: Crypto.randomUUID(), sessionId,
      text: text.trim(), attachments });
    await clearDraft(connection.serverId, draftScope);
    setText(""); setAttachments([]);
    await outbox.refresh();
    await processOutbox(connection);
    await Promise.all([refresh(), outbox.refresh(), refreshSessions()]);
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
  return <View style={{ flex: 1 }}>
    <View testID="session:toolbar" style={[styles.toolbar, { borderBottomColor: theme.colors.border }]}> 
      <View style={styles.toolbarMain}><View style={styles.toolbarTitle}><Title>{session.title}</Title></View>
        {running && <Button testID="session:stop" title="停止" variant="danger" onPress={() => void action("stop")} />}
        <Pressable testID="session:rename" onPress={() => { setTitle(session.title); setRenaming(true); }}>
          <Text style={{ color: theme.colors.textMuted, padding: 10 }}>改名</Text>
        </Pressable>
        <Pressable testID={session.lifecycleState === "archived" ? "session:restore" : "session:archive"}
          onPress={() => void action(session.lifecycleState === "archived" ? "restore" : "archive")}>
          <Text style={{ color: theme.colors.textMuted, padding: 10 }}>{session.lifecycleState === "archived" ? "恢复" : "归档"}</Text>
        </Pressable>
      </View>
      <View testID={running ? "session:next-turn-settings" : "session:idle-settings"}>
        <Muted>{running ? "正在运行 · 参数修改将在下一轮生效" : `${session.serviceTier} · ${session.collaborationMode}`}</Muted>
      </View>
    </View>
    <FlashList key={`${sessionId}:${initialSyncComplete ? "synced" : "cached"}`} ref={list}
      testID="messages:list" data={visibleMessages} keyExtractor={(item) => item.id}
      renderItem={({ item }) => <MessageBubble message={item} />}
      keyboardShouldPersistTaps="handled"
      scrollEventThrottle={100}
      onScroll={({ nativeEvent }) => {
        if (!historyPagingReady.current) return;
        const distance = nativeEvent.contentSize.height - nativeEvent.layoutMeasurement.height -
          nativeEvent.contentOffset.y;
        nearBottom.current = distance < 160;
      }}
      maintainVisibleContentPosition={{ startRenderingFromBottom: true,
        autoscrollToBottomThreshold: 0.1 }}
      onLoad={() => requestAnimationFrame(() => {
        list.current?.scrollToEnd({ animated: false });
        if (initialSyncComplete) historyPagingReady.current = true;
      })}
      onStartReached={() => {
        if (historyPagingReady.current && hasMore && messages[0]) void load(messages[0].seq);
      }}
      onStartReachedThreshold={0.2} contentContainerStyle={{ paddingTop: 10, paddingBottom: 24 }}
      ListFooterComponent={<><View testID={liveEvents.length > 0 ? "run:live" : undefined}>
        {presentedRun && <RunProgressCard run={presentedRun} />}</View>
        {finalMessage && <MessageBubble message={finalMessage} />}
        {presentedRun && <PlanCard run={presentedRun} onExecute={() => void new ClientApi(connection)
          .executePlan(sessionId, presentedRun.id).then(() => refresh()).catch((error: unknown) =>
            Alert.alert("执行 Plan 失败", error instanceof Error ? error.message : "请重试"))} />}
        {presentedRun?.pendingInteractives.map((interactive) => <InteractiveCard key={interactive.id}
          interactive={interactive} onSubmit={(answer) => void new ClientApi(connection)
            .answerInteractive(interactive.id, answer).then(() => refresh()).catch((error: unknown) =>
              Alert.alert("提交回答失败", error instanceof Error ? error.message : "请重试"))} />)}</>} />
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
    <View style={[styles.composerDock, { borderTopColor: theme.colors.border, backgroundColor: theme.colors.app }]}>
      <ChatComposer value={text} onChange={setText} attachments={attachments}
        onAttachmentsChange={setAttachments} onParameters={() => {
          setSettingsBeforeSheet(settings); setShowParameters(true);
        }}
        onSend={() => void send()} sending={false}
        parameterLabel={`${settings.model ?? "默认模型"} · ${settings.reasoningEffort ?? "默认"} · ${settings.collaborationMode}`} />
    </View>
    <ParameterSheet visible={showParameters} bootstrap={bootstrap}
      workspaceId={session.workspaceId} value={settings}
      currentRunLabel={running ? "当前 Run 使用已冻结参数；修改将在下一轮生效" : "当前会话参数"}
      onChange={setSettings} onClose={() => void closeParameters()}
      onCancel={() => { setSettings(settingsBeforeSheet ?? savedSettings); setShowParameters(false); }} />
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
  toolbar: { minHeight: 64, paddingHorizontal: 14, paddingVertical: 8,
    borderBottomWidth: StyleSheet.hairlineWidth, gap: 2 },
  toolbarMain: { flexDirection: "row", alignItems: "center", gap: 8 },
  toolbarTitle: { flex: 1, minWidth: 0 },
  composerDock: { borderTopWidth: StyleSheet.hairlineWidth },
  outbox: { marginHorizontal: 12, marginTop: 6, borderWidth: StyleSheet.hairlineWidth,
    borderRadius: 8, padding: 8, flexDirection: "row", alignItems: "center", gap: 8 },
  modal: { flex: 1, justifyContent: "center", padding: 24 },
  rename: { borderWidth: StyleSheet.hairlineWidth, borderRadius: 12, padding: 16, gap: 12 },
  titleInput: { minHeight: 44, borderWidth: StyleSheet.hairlineWidth, borderRadius: 8,
    paddingHorizontal: 12, fontFamily: "Inter_400Regular" },
});
