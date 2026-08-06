import * as Crypto from "expo-crypto";
import { FlashList, type FlashListRef } from "@shopify/flash-list";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Alert, KeyboardAvoidingView, Platform, Pressable, StyleSheet, Text, useWindowDimensions,
  View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { ClientApi } from "@/api/client";
import { clearDraft, loadDraft, saveDraft } from "@/db/drafts";
import { sessionHasUnread, visibleSessionReadSnapshot } from "@/db/sessionReadStatus";
import { Button, EmptyState, Muted } from "@/components/ui";
import { useKeyboardVisible } from "@/hooks/useKeyboardVisible";
import { useOutbox } from "@/hooks/useOutbox";
import { previewPerf } from "@/preview/perf";
import { useAppStore } from "@/store/appStore";
import { useConversationStore } from "@/store/conversationStore";
import { enqueueMessage, processOutbox, type LocalAttachment } from "@/sync/outbox";
import { subscribeToUpdates, type SyncEvent } from "@/sync/synchronizer";
import { useTheme } from "@/theme/ThemeProvider";
import { type Message, type RunSegment, type SessionSettings,
  type TurnRun } from "@/types/protocol";
import { keyboardAvoidance } from "@/utils/keyboardAvoidance";
import { ChatComposer } from "./ChatComposer";
import { MarkdownContent } from "./MarkdownContent";
import { MessageBubble } from "./MessageBubble";
import { ParameterSheet } from "./ParameterSheet";
import { InteractiveCard, PlanCard } from "./RunCards";
import { RunSegmentCard } from "./RunSegmentCard";
import { renderableFinalAnswer } from "./responseDirectives";
import { conversationTurnIdFromPayload } from "./updateRouting";

type ConversationRow =
  | { kind: "message"; message: Message }
  | { kind: "segment"; run: TurnRun; segment: RunSegment; continued: boolean; active: boolean;
    hasFinalAnswer: boolean }
  | { kind: "interactive"; id: string }
  | { kind: "stream"; runId: string };

const outerPositioning = {
  animateAutoScrollToBottom: false,
} as const;
const initialRenderRowCount = 16;
const emptyTurns: never[] = [];

function conversationRowKey(item: ConversationRow): string {
  return item.kind === "message" ? `message:${item.message.id}` :
    item.kind === "segment" ? `segment:${item.segment.id}` :
    item.kind === "interactive" ? `interactive:${item.id}` : `stream:${item.runId}`;
}

function liveTimelineEvent(event: SyncEvent): { type: string; payload: unknown } {
  if (!event.payload || typeof event.payload !== "object") {
    return { type: event.type, payload: event.payload };
  }
  const value = event.payload as Record<string, unknown>;
  return {
    type: typeof value.eventType === "string" ? value.eventType : event.type,
    payload: "data" in value ? value.data : event.payload,
  };
}

export function ConversationPane({ sessionId }: { sessionId: string }) {
  const renderStartedAt = performance.now();
  const theme = useTheme();
  const window = useWindowDimensions();
  const insets = useSafeAreaInsets();
  const keyboardVisible = useKeyboardVisible();
  const connection = useAppStore((state) => state.activeConnection);
  const bootstrap = useAppStore((state) => state.bootstrap);
  const listedSession = useAppStore((state) => state.sessions.find((item) => item.id === sessionId));
  const refreshSessions = useAppStore((state) => state.refresh);
  const markSessionRead = useAppStore((state) => state.markSessionRead);
  const sessionRead = useAppStore((state) => state.sessionReads[sessionId]);
  const conversationKey = connection ? `${connection.serverId}:${sessionId}` : "";
  const entry = useConversationStore((state) => state.entries[conversationKey]);
  const openConversation = useConversationStore((state) => state.open);
  const refreshConversation = useConversationStore((state) => state.refresh);
  const loadOlderConversation = useConversationStore((state) => state.loadOlder);
  const refreshConversationTurn = useConversationStore((state) => state.refreshTurn);
  const noteConversationCursor = useConversationStore((state) => state.noteCursor);
  const closeConversation = useConversationStore((state) => state.close);
  const turns = entry?.view?.turns ?? emptyTurns;
  const session = entry?.view?.session ?? listedSession;
  const readSnapshot = visibleSessionReadSnapshot(listedSession, session);
  const messagesReady = Boolean(entry?.view);
  const [initialSyncComplete, setInitialSyncComplete] = useState(false);
  const loadError = entry?.status === "error";
  const [text, setText] = useState("");
  const [attachments, setAttachments] = useState<LocalAttachment[]>([]);
  const [settings, setSettings] = useState<SessionSettings | null>(null);
  const [savedSettings, setSavedSettings] = useState<SessionSettings | null>(null);
  const [liveVersions, setLiveVersions] = useState<Record<string, number>>({});
  const [finalDrafts, setFinalDrafts] = useState<Record<string, string>>({});
  const [showParameters, setShowParameters] = useState(false);
  const [settingsBeforeSheet, setSettingsBeforeSheet] = useState<SessionSettings | null>(null);
  const hasMore = entry?.view?.hasMoreBefore ?? false;
  const [showScrollToLatest, setShowScrollToLatest] = useState(false);
  const [outerScrollEnabled, setOuterScrollEnabled] = useState(true);
  const [listPositioned, setListPositioned] = useState(false);
  const [draftReady, setDraftReady] = useState(false);
  const list = useRef<FlashListRef<ConversationRow>>(null);
  const activeSessionId = useRef(sessionId);
  activeSessionId.current = sessionId;
  const historyPaging = useRef(false);
  const historyPagingReady = useRef(false);
  const finalDraftSequences = useRef(new Map<string, Set<number>>());
  const nearBottom = useRef(true);
  const outerPinnedToLatest = useRef(true);
  const outerDragging = useRef(false);
  const outerMomentum = useRef(false);
  const initialPositioned = useRef(false);
  const initialPositionTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const initialPositionFrame = useRef<number | null>(null);
  const initialSyncSessionId = useRef<string | null>(null);
  const lastAutoScrollKey = useRef<string | undefined>(undefined);
  const activeRunId = useRef<string | null>(null);
  const scrollMetrics = useRef({ contentHeight: 0, viewportHeight: 0, offsetY: 0 });
  const lastLoggedOffset = useRef(0);
  const outbox = useOutbox(connection?.serverId, sessionId);
  const draftScope = `session:${sessionId}`;
  const updateScrollState = useCallback((contentHeight: number, viewportHeight: number, offsetY: number) => {
    const distance = contentHeight - viewportHeight - offsetY;
    nearBottom.current = distance < 160;
    const shouldShow = distance > 320;
    setShowScrollToLatest((current) => current === shouldShow ? current : shouldShow);
  }, []);
  const refresh = useCallback(() => connection ? refreshConversation(connection, sessionId) : Promise.resolve(),
    [connection, refreshConversation, sessionId]);
  const refreshTurn = useCallback((turnId: string) => connection ?
    refreshConversationTurn(connection, sessionId, turnId) : Promise.resolve(),
  [connection, refreshConversationTurn, sessionId]);
  useEffect(() => {
    setInitialSyncComplete(false);
    initialSyncSessionId.current = null;
    setSettings(null);
    setSavedSettings(null);
    setLiveVersions({});
    setFinalDrafts({});
    historyPaging.current = false;
    finalDraftSequences.current.clear();
    setShowScrollToLatest(false);
    setOuterScrollEnabled(true);
    setListPositioned(false);
    nearBottom.current = true;
    outerPinnedToLatest.current = true;
    outerDragging.current = false;
    outerMomentum.current = false;
    initialPositioned.current = false;
    if (initialPositionTimer.current) clearTimeout(initialPositionTimer.current);
    if (initialPositionFrame.current !== null) cancelAnimationFrame(initialPositionFrame.current);
    initialPositionTimer.current = null;
    initialPositionFrame.current = null;
    lastAutoScrollKey.current = undefined;
    activeRunId.current = null;
    scrollMetrics.current = { contentHeight: 0, viewportHeight: 0, offsetY: 0 };
    historyPagingReady.current = false;
    return () => {
      if (initialPositionTimer.current) clearTimeout(initialPositionTimer.current);
      if (initialPositionFrame.current !== null) cancelAnimationFrame(initialPositionFrame.current);
    };
  }, [sessionId]);
  useEffect(() => {
    if (!connection) return;
    void openConversation(connection, sessionId);
    return () => closeConversation(connection, sessionId);
  }, [closeConversation, connection, openConversation, sessionId]);
  useEffect(() => {
    const next = entry?.view?.settings;
    if (!next || showParameters) return;
    setSettings(next); setSavedSettings(next);
    initialSyncSessionId.current = sessionId;
    setInitialSyncComplete(true);
  }, [entry?.view?.settings, sessionId, showParameters]);
  useEffect(() => {
    if (initialSyncComplete && initialSyncSessionId.current === sessionId && readSnapshot &&
      sessionHasUnread(readSnapshot, sessionRead)) {
      void markSessionRead(readSnapshot);
    }
  }, [initialSyncComplete, markSessionRead, readSnapshot, sessionId, sessionRead]);
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
  const bumpLiveVersion = useCallback((runId: string | null) => {
    if (!runId) return;
    setLiveVersions((current) => ({ ...current, [runId]: (current[runId] ?? 0) + 1 }));
  }, []);
  useEffect(() => subscribeToUpdates((event) => {
    if (event.type === "sync.resumed") {
      bumpLiveVersion(activeRunId.current);
      return;
    }
    if (event.sessionId !== sessionId) return;
    if (connection && event.cursor !== undefined) {
      noteConversationCursor(connection, sessionId, event.cursor);
    }
    if (event.kind === "live") {
      bumpLiveVersion(event.entityType === "run" ? event.entityId : activeRunId.current);
      const unwrapped = liveTimelineEvent(event);
      const payload = unwrapped.payload && typeof unwrapped.payload === "object" ?
        unwrapped.payload as Record<string, unknown> : {};
      const item = payload.item && typeof payload.item === "object" ?
        payload.item as Record<string, unknown> : {};
      const phase = String(item.phase ?? payload.phase ?? "");
      const delta = String(payload.delta ?? payload.text ?? "");
      if ((unwrapped.type === "item/agentMessage/delta" || unwrapped.type === "item/delta") &&
        phase === "final_answer" && delta) {
        const sequence = event.runEventSeq;
        if (sequence !== undefined) {
          const seen = finalDraftSequences.current.get(event.entityId) ?? new Set<number>();
          if (seen.has(sequence)) return;
          seen.add(sequence);
          finalDraftSequences.current.set(event.entityId, seen);
        }
        setFinalDrafts((current) => ({ ...current,
          [event.entityId]: (current[event.entityId] ?? "") + delta }));
      }
    } else {
      const conversationTurnId = conversationTurnIdFromPayload(event.payload);
      if (event.type === "message.created" && conversationTurnId) {
        void refreshTurn(conversationTurnId);
      } else if (event.entityType === "turn") {
        void refreshTurn(event.entityId);
      } else {
        void refresh();
      }
    }
  }), [bumpLiveVersion, connection, noteConversationCursor, refresh, refreshTurn, sessionId]);
  const latestTurn = turns.at(-1);
  const latestRun = latestTurn?.runs.at(-1);
  const running = latestRun && ["starting", "running", "waiting_for_user", "reconciling"]
    .includes(latestRun.status);
  activeRunId.current = running ? latestRun.id : null;
  const conversationRows = useMemo<ConversationRow[]>(() => {
    const rows: ConversationRow[] = [];
    for (const turn of turns) {
      const rendered = new Set<string>();
      const users = turn.messages.filter((message) => message.role !== "agent");
      const agents = turn.messages.filter((message) => message.role === "agent");
      const finalRun = turn.runs.at(-1);
      for (const run of turn.runs) {
        for (let index = 0; index < run.segments.length; index++) {
          const segment = run.segments[index]!;
          const trigger = segment.triggerMessageId ?
            users.find((message) => message.id === segment.triggerMessageId) :
            users.find((message) => !rendered.has(message.id));
          if (trigger && !rendered.has(trigger.id)) {
            rows.push({ kind: "message", message: trigger });
            rendered.add(trigger.id);
          }
          if (segment.triggerType === "interactive" && segment.interactiveRequestId) {
            rows.push({ kind: "interactive", id: segment.interactiveRequestId });
          }
          const isLatest = turn.id === latestTurn?.id && run.id === latestRun?.id &&
            segment.id === run.segments.at(-1)?.id;
          rows.push({ kind: "segment", run, segment, continued: index < run.segments.length - 1,
            active: Boolean(isLatest && running), hasFinalAnswer: agents.length > 0 &&
              run.id === finalRun?.id && segment.id === run.segments.at(-1)?.id });
        }
      }
      for (const message of users) {
        if (!rendered.has(message.id)) rows.push({ kind: "message", message });
      }
      const draftRun = turn.runs.at(-1);
      if (agents.length === 0 && draftRun && draftRun.actualSettings.collaborationMode !== "plan") {
        rows.push({ kind: "stream", runId: draftRun.id });
      }
      for (const message of agents) rows.push({ kind: "message", message });
    }
    return rows;
  }, [latestRun?.id, latestTurn?.id, running, turns]);
  useEffect(() => {
    previewPerf("conversation:commit", { sessionId, turns: turns.length, rows: conversationRows.length,
      renderElapsed: (performance.now() - renderStartedAt).toFixed(1) });
  });
  const handleFinalDraft = useCallback((runId: string, value: string) => {
    setFinalDrafts((current) => {
      if (current[runId] === value) return current;
      const next = { ...current };
      if (value) next[runId] = value;
      else delete next[runId];
      return next;
    });
  }, []);
  const lockOuterScroll = useCallback(() => setOuterScrollEnabled(false), [setOuterScrollEnabled]);
  const unlockOuterScroll = useCallback(() => setOuterScrollEnabled(true), [setOuterScrollEnabled]);
  const followLatestActivity = useCallback((force = false) => {
    if (!force && !outerPinnedToLatest.current) return;
    outerPinnedToLatest.current = true;
    previewPerf("conversation:programmatic-scroll", { source: "segment-activity", target: "end" });
    list.current?.scrollToEnd({ animated: true });
    setShowScrollToLatest(false);
  }, []);
  const segmentCardMaxHeight = Math.min(620, Math.max(420, Math.round(window.height * 0.70)));
  const renderConversationRow = useCallback(({ item }: { item: ConversationRow }) =>
    item.kind === "message" ? <MessageBubble message={item.message} /> :
      item.kind === "segment" ? <RunSegmentCard sessionId={sessionId} run={item.run} segment={item.segment}
        continued={item.continued} active={item.active} maxHeight={segmentCardMaxHeight}
        liveVersion={liveVersions[item.run.id] ?? 0}
        hasFinalAnswer={item.hasFinalAnswer || Boolean(finalDrafts[item.run.id])}
        onInteractionStart={lockOuterScroll}
        onInteractionEnd={unlockOuterScroll} onFollowLatest={followLatestActivity}
        onFinalDraft={handleFinalDraft} /> :
        item.kind === "interactive" ? <View style={styles.interactiveHistory}>
          <Muted selectable>已提交交互回答，任务继续执行</Muted>
        </View> : finalDrafts[item.runId] ?
          <View testID={`run:${item.runId}:stream-final`} style={styles.streamedFinal}>
            <MarkdownContent>{renderableFinalAnswer(finalDrafts[item.runId] ?? "")}</MarkdownContent>
          </View> : null,
  [finalDrafts, followLatestActivity, handleFinalDraft, liveVersions, lockOuterScroll, segmentCardMaxHeight, sessionId,
    unlockOuterScroll]);
  const rowExtraData = useMemo(() => ({ finalDrafts, liveVersions }), [finalDrafts, liveVersions]);
  const lastRow = conversationRows.at(-1);
  const initialScrollIndex = Math.max(0, conversationRows.length - initialRenderRowCount);
  const finalMessageId = lastRow?.kind === "message" ? lastRow.message.id :
    lastRow?.kind === "stream" ? `${lastRow.runId}:stream` : undefined;
  useEffect(() => {
    if (!finalMessageId || finalMessageId === lastAutoScrollKey.current) return;
    lastAutoScrollKey.current = finalMessageId;
    if (!initialPositioned.current || !outerPinnedToLatest.current) return;
    const frame = requestAnimationFrame(() => {
      previewPerf("conversation:programmatic-scroll", { source: "final-message", target: "end" });
      list.current?.scrollToEnd({ animated: true });
      setShowScrollToLatest(false);
    });
    return () => cancelAnimationFrame(frame);
  }, [finalMessageId]);
  if (!connection || !bootstrap) {
    return <EmptyState title="连接不可用" detail="请返回连接页后重试。" />;
  }
  if (!session) {
    return <EmptyState title="会话不可用" detail="它可能已被移除，或不在当前连接中。" />;
  }
  const resolvedSettings = settings ?? entry?.view?.settings ?? null;
  if ((!resolvedSettings || !messagesReady) && loadError) {
    return <EmptyState title="无法加载会话" detail="请检查网络后重试。"
      action={<Button testID="session:load:retry" title="重试" onPress={() => void refresh()} />} />;
  }
  if (!resolvedSettings || !messagesReady) {
    return <EmptyState title="正在加载会话" detail="请稍候…" />;
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
    if (!savedSettings || JSON.stringify(savedSettings) === JSON.stringify(resolvedSettings)) return;
    try {
      const updated = await new ClientApi(connection).patchSession(sessionId, {
        agentProfileId: resolvedSettings.agentProfileId, model: resolvedSettings.model,
        reasoningEffort: resolvedSettings.reasoningEffort, serviceTier: resolvedSettings.serviceTier,
        collaborationMode: resolvedSettings.collaborationMode,
        expectedSettingsVersion: savedSettings.settingsVersion,
      });
      const next = { ...resolvedSettings, settingsVersion: updated.settingsVersion };
      setSettings(next); setSavedSettings(next);
      await refreshSessions();
    } catch (error) {
      setSettings(savedSettings);
      Alert.alert("参数没有保存", error instanceof Error ? error.message : "请重试");
    }
  };
  return <KeyboardAvoidingView {...keyboardAvoidance(Platform.OS, insets.top, keyboardVisible)}
    style={styles.keyboardAvoiding}>
    <View style={styles.messageArea} onLayout={({ nativeEvent }) => previewPerf("conversation:layout", {
      height: nativeEvent.layout.height, width: nativeEvent.layout.width,
    })}>
      <FlashList key={sessionId} ref={list}
      style={[styles.messageList, { opacity: listPositioned ? 1 : 0 }]}
      testID="messages:list" data={conversationRows} extraData={rowExtraData}
      initialScrollIndex={initialScrollIndex}
      keyExtractor={conversationRowKey}
      renderItem={renderConversationRow}
      drawDistance={160}
      nestedScrollEnabled
      scrollEnabled={outerScrollEnabled}
      keyboardShouldPersistTaps="handled"
      scrollEventThrottle={100}
      onScroll={({ nativeEvent }) => {
        scrollMetrics.current = { contentHeight: nativeEvent.contentSize.height,
          viewportHeight: nativeEvent.layoutMeasurement.height, offsetY: nativeEvent.contentOffset.y };
        if (Math.abs(lastLoggedOffset.current - nativeEvent.contentOffset.y) >= 40) {
          lastLoggedOffset.current = nativeEvent.contentOffset.y;
          previewPerf("conversation:offset", { offset: nativeEvent.contentOffset.y.toFixed(1),
            contentHeight: nativeEvent.contentSize.height.toFixed(1),
            viewportHeight: nativeEvent.layoutMeasurement.height.toFixed(1) });
        }
        updateScrollState(scrollMetrics.current.contentHeight, scrollMetrics.current.viewportHeight,
          scrollMetrics.current.offsetY);
        if (outerDragging.current || outerMomentum.current) {
          outerPinnedToLatest.current = nearBottom.current;
        }
      }}
      onContentSizeChange={(_, contentHeight) => {
        previewPerf("conversation:content-size", { previous: scrollMetrics.current.contentHeight.toFixed(1),
          next: contentHeight.toFixed(1), offset: scrollMetrics.current.offsetY.toFixed(1) });
        scrollMetrics.current.contentHeight = contentHeight;
        if (scrollMetrics.current.viewportHeight > 0) updateScrollState(contentHeight,
          scrollMetrics.current.viewportHeight, scrollMetrics.current.offsetY);
      }}
      maintainVisibleContentPosition={outerPositioning}
      onLoad={({ elapsedTimeInMs }) => {
        if (initialPositioned.current || initialPositionTimer.current) return;
        initialPositionTimer.current = setTimeout(() => {
          initialPositionTimer.current = null;
          if (activeSessionId.current !== sessionId) return;
          initialPositioned.current = true;
          nearBottom.current = true;
          outerPinnedToLatest.current = true;
          list.current?.scrollToEnd({ animated: false });
          initialPositionFrame.current = requestAnimationFrame(() => {
            initialPositionFrame.current = null;
            if (activeSessionId.current !== sessionId) return;
            setShowScrollToLatest(false);
            setListPositioned(true);
            previewPerf("conversation:initial-position", { source: "near-end-before-reveal",
              initialScrollIndex, elapsed: elapsedTimeInMs.toFixed(1) });
          });
        }, 120);
      }}
      onScrollBeginDrag={() => {
        outerDragging.current = true;
        if (initialSyncComplete) historyPagingReady.current = true;
      }}
      onScrollEndDrag={() => {
        outerPinnedToLatest.current = nearBottom.current;
        outerDragging.current = false;
      }}
      onMomentumScrollBegin={() => { outerMomentum.current = true; }}
      onMomentumScrollEnd={() => {
        outerPinnedToLatest.current = nearBottom.current;
        outerMomentum.current = false;
      }}
      onStartReached={() => {
        if (!connection || !historyPagingReady.current || historyPaging.current || !hasMore) return;
        historyPaging.current = true;
        void loadOlderConversation(connection, sessionId)
          .finally(() => { historyPaging.current = false; });
      }}
      onStartReachedThreshold={0.2} contentContainerStyle={{ paddingTop: 10, paddingBottom: 24 }}
      ListFooterComponent={<>{latestRun && <PlanCard run={latestRun} onExecute={() => void new ClientApi(connection)
          .executePlan(sessionId, latestRun.id).then(() => refresh()).catch((error: unknown) =>
            Alert.alert("执行计划失败", error instanceof Error ? error.message : "请重试"))} />}
        {latestRun?.pendingInteractives.map((interactive) => <InteractiveCard key={interactive.id}
          interactive={interactive} onSubmit={(answer) => void new ClientApi(connection)
            .answerInteractive(interactive.id, answer).then(() => refresh()).catch((error: unknown) =>
              Alert.alert("提交回答失败", error instanceof Error ? error.message : "请重试"))} />)}</>} />
      {showScrollToLatest && <Pressable testID="chat:scroll-to-latest" accessibilityRole="button"
        accessibilityLabel="回到最新消息" onPress={() => {
          nearBottom.current = true;
          outerPinnedToLatest.current = true;
          setShowScrollToLatest(false);
          previewPerf("conversation:programmatic-scroll", { source: "latest-button", target: "end" });
          list.current?.scrollToEnd({ animated: true });
        }} style={({ pressed }) => [styles.scrollToLatest, { backgroundColor: theme.colors.surface,
          borderColor: theme.colors.border, opacity: pressed ? 0.72 : 1 }, theme.shadow]}>
        <Text style={[styles.scrollToLatestIcon, { color: theme.colors.text }]}>↓</Text>
      </Pressable>}
    </View>
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
      <View pointerEvents="none" style={styles.composerShadow}>
        {(theme.dark
          ? ["rgba(0,0,0,0.02)", "rgba(0,0,0,0.05)", "rgba(0,0,0,0.10)", "rgba(0,0,0,0.18)"]
          : ["rgba(31,35,40,0.01)", "rgba(31,35,40,0.025)", "rgba(31,35,40,0.05)", "rgba(31,35,40,0.09)"]
        ).map((backgroundColor) => <View key={backgroundColor} style={[styles.composerShadowBand,
          { backgroundColor }]} />)}
      </View>
      <ChatComposer value={text} onChange={setText} attachments={attachments}
        onAttachmentsChange={setAttachments} onParameters={() => {
          setSettingsBeforeSheet(resolvedSettings); setShowParameters(true);
        }}
        onSend={() => void send()} sending={false}
        parameterLabel={`${resolvedSettings.model ?? "默认模型"} · ${resolvedSettings.reasoningEffort ?? "默认"} · ${resolvedSettings.collaborationMode === "plan" ? "先做计划" : "直接执行"}`} />
    </View>
    <ParameterSheet visible={showParameters} bootstrap={bootstrap}
      workspaceId={session.workspaceId} value={resolvedSettings}
      currentRunLabel={running ? "当前任务继续使用启动时的参数；修改将在下一轮生效" : "当前会话参数"}
      onChange={setSettings} onClose={() => void closeParameters()}
      onCancel={() => { setSettings(settingsBeforeSheet ?? savedSettings); setShowParameters(false); }} />
  </KeyboardAvoidingView>;
}

const styles = StyleSheet.create({
  keyboardAvoiding: { flex: 1, minHeight: 0 },
  messageArea: { flex: 1, minHeight: 0 },
  messageList: { flex: 1 },
  streamedFinal: { paddingHorizontal: 16, paddingVertical: 8 },
  interactiveHistory: { marginHorizontal: 12, marginVertical: 5, paddingHorizontal: 12,
    paddingVertical: 9, borderRadius: 8 },
  scrollToLatest: { position: "absolute", right: 16, bottom: 12, width: 42, height: 42,
    borderRadius: 21, borderWidth: StyleSheet.hairlineWidth, alignItems: "center",
    justifyContent: "center", elevation: 5 },
  scrollToLatestIcon: { fontFamily: "Inter_500Medium", fontSize: 22, lineHeight: 26 },
  composerDock: { borderTopWidth: StyleSheet.hairlineWidth, position: "relative", zIndex: 2 },
  composerShadow: { position: "absolute", left: 0, right: 0, top: -20, height: 20 },
  composerShadowBand: { flex: 1 },
  outbox: { marginHorizontal: 12, marginTop: 6, borderWidth: StyleSheet.hairlineWidth,
    borderRadius: 8, padding: 8, flexDirection: "row", alignItems: "center", gap: 8 },
});
