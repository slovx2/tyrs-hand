import type { Model } from "@codex-app-server/v2/Model";
import type { ServerRequest } from "@codex-app-server/ServerRequest";
import { FlashList, type FlashListRef } from "@shopify/flash-list";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ActivityIndicator, Alert, AppState, KeyboardAvoidingView, Platform, Pressable,
  StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import type { LocalAttachment } from "@/app-server/attachments";
import { latestCompletedPlan, type TurnPreferences } from "@/app-server/officialClient";
import { defaultTurnPreferences } from "@/app-server/preferences";
import { targetKey } from "@/app-server/types";
import { Button, EmptyState } from "@/components/ui";
import { clearDraft, loadDraft, saveDraft } from "@/db/drafts";
import { useKeyboardVisible } from "@/hooks/useKeyboardVisible";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import { keyboardAvoidance } from "@/utils/keyboardAvoidance";
import { ChatComposer } from "./ChatComposer";
import { createFollowState, latestTurnPhase, reduceFollowState,
  shouldFollowLatest, type FollowEvent } from "./conversationFollow";
import { OfficialTurn } from "./OfficialTurn";
import { ParameterSheet } from "./ParameterSheet";
import { ServerRequestCard } from "./ServerRequestCard";
import { createActiveTurnReconciler } from "./activeTurnReconciler";
import { ACTIVITY_TOGGLE_SCROLL_SETTLE_MS, activityToggleAllowed } from "./activityDisclosure";
import { conversationRows, type ConversationRow } from "./conversationRows";
import { anchorViewOffset, conversationScrollState, loadConversationPosition,
  resolveConversationPosition, saveConversationPosition, visibleRowTop } from "./conversationPosition";

const rowKey = (row: ConversationRow) => row.key;
const rowType = (row: ConversationRow) => row.kind;

const EMPTY_MODELS: Model[] = [];
const EMPTY_REQUESTS: ServerRequest[] = [];
const LIST_POSITIONING = {
  startRenderingFromBottom: true,
} as const;
const LIST_DRAW_DISTANCE = 180;
const LIST_RECYCLE_POOL_SIZE = 12;
const OLDER_LOAD_SETTLE_MS = 120;

export function ConversationPane({ sessionId }: { sessionId: string }) {
  const theme = useTheme();
  const insets = useSafeAreaInsets();
  const keyboardVisible = useKeyboardVisible();
  const list = useRef<FlashListRef<ConversationRow>>(null);
  const connection = useAppStore((state) => state.activeConnection);
  const record = useAppStore((state) => state.threads.find((item) => item.thread.id === sessionId));
  const modelsByTarget = useAppStore((state) => state.modelsByTarget);
  const requests = useAppStore((state) => state.pendingRequests[sessionId] ?? EMPTY_REQUESTS);
  const loadThread = useAppStore((state) => state.loadThread);
  const refreshThreadTail = useAppStore((state) => state.refreshThreadTail);
  const loadOlderThread = useAppStore((state) => state.loadOlderThread);
  const submitMessage = useAppStore((state) => state.submitMessage);
  const setThreadVisible = useAppStore((state) => state.setThreadVisible);
  const retryOutbox = useAppStore((state) => state.retryOutbox);
  const discardOutbox = useAppStore((state) => state.discardOutbox);
  const outbox = useAppStore((state) => state.outbox);
  const queued = useMemo(() => outbox.filter((item) =>
    item.kind === "submit_message" && item.threadId === sessionId), [outbox, sessionId]);
  const executePlan = useAppStore((state) => state.executePlan);
  const interruptThread = useAppStore((state) => state.interruptThread);
  const answerRequest = useAppStore((state) => state.answerRequest);
  const profileId = connection?.profileId ?? null;
  const workspaceId = record?.workspaceId ?? null;
  const positionKey = profileId ? `${profileId}:${sessionId}` : null;
  const savedPosition = useMemo(() => positionKey
    ? loadConversationPosition(positionKey) : null, [positionKey]);
  const [text, setText] = useState("");
  const [attachments, setAttachments] = useState<LocalAttachment[]>([]);
  const [preferences, setPreferences] = useState<TurnPreferences | null>(null);
  const [beforeSheet, setBeforeSheet] = useState<TurnPreferences | null>(null);
  const [showParameters, setShowParameters] = useState(false);
  const [draftReady, setDraftReady] = useState(false);
  const [loading, setLoading] = useState(true);
  const [sending, setSending] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [olderError, setOlderError] = useState<string | null>(null);
  const [showScrollToLatest, setShowScrollToLatest] = useState(
    savedPosition?.kind === "anchor");
  const [positionRestored, setPositionRestored] = useState(false);
  const rowsRef = useRef<ConversationRow[]>([]);
  const pinnedToLatest = useRef(savedPosition?.kind !== "anchor");
  const showScrollToLatestRef = useRef(savedPosition?.kind === "anchor");
  const historyPagingReady = useRef(false);
  const olderLoadRequested = useRef(false);
  const momentumScrolling = useRef(false);
  const userDragging = useRef(false);
  const interactionBlocked = useRef(false);
  const interactionSettleTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const followState = useRef(createFollowState());
  const latestPhase = useRef<ReturnType<typeof latestTurnPhase>>("idle");
  const activityToggleBlockedUntil = useRef(0);
  const olderLoadTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const activeSessionId = useRef(sessionId);
  const scrollOffset = useRef(0);
  activeSessionId.current = sessionId;
  const models = profileId
    ? modelsByTarget[targetKey(profileId, workspaceId)] ?? EMPTY_MODELS
    : EMPTY_MODELS;
  const fallbackPreferences = useMemo(() => defaultTurnPreferences(models), [models]);
  const resolvedPreferences = preferences ?? fallbackPreferences;
  const draftScope = `thread:${sessionId}`;
  const activeTurnId = [...(record?.thread.turns ?? [])].reverse()
    .find((turn) => turn.status === "inProgress")?.id ?? null;
  const activeTurn = [...(record?.thread.turns ?? [])].reverse()
    .find((turn) => turn.status === "inProgress") ?? null;
  const setScrollToLatestVisible = useCallback((visible: boolean) => {
    if (showScrollToLatestRef.current === visible) return;
    showScrollToLatestRef.current = visible;
    setShowScrollToLatest(visible);
  }, []);
  const canToggleActivity = useCallback(() =>
    activityToggleAllowed(activityToggleBlockedUntil.current), []);
  const dispatchFollow = useCallback((event: FollowEvent) => {
    followState.current = reduceFollowState(followState.current, event);
  }, []);

  useEffect(() => {
    let canceled = false;
    setLoading(true);
    setError(null);
    void loadThread(sessionId).catch((cause) => {
      if (!canceled) setError(cause instanceof Error ? cause.message : "无法读取官方会话历史");
    }).finally(() => { if (!canceled) setLoading(false); });
    return () => { canceled = true; };
  }, [loadThread, sessionId]);

  useEffect(() => {
    const applyVisibility = (state: string) => setThreadVisible(sessionId, state === "active");
    applyVisibility(AppState.currentState);
    const subscription = AppState.addEventListener("change", applyVisibility);
    return () => {
      subscription.remove();
      setThreadVisible(sessionId, false);
    };
  }, [sessionId, setThreadVisible]);

  useEffect(() => {
    const position = positionKey ? loadConversationPosition(positionKey) : null;
    pinnedToLatest.current = position?.kind !== "anchor";
    historyPagingReady.current = false;
    olderLoadRequested.current = false;
    momentumScrolling.current = false;
    userDragging.current = false;
    interactionBlocked.current = false;
    followState.current = createFollowState();
    latestPhase.current = "idle";
    activityToggleBlockedUntil.current = 0;
    if (interactionSettleTimer.current) clearTimeout(interactionSettleTimer.current);
    interactionSettleTimer.current = null;
    if (olderLoadTimer.current) clearTimeout(olderLoadTimer.current);
    olderLoadTimer.current = null;
    scrollOffset.current = 0;
    setScrollToLatestVisible(position?.kind === "anchor");
    setPositionRestored(false);
    setLoadingOlder(false);
    setOlderError(null);
  }, [positionKey, setScrollToLatestVisible]);

  useEffect(() => () => {
    if (olderLoadTimer.current) clearTimeout(olderLoadTimer.current);
    if (interactionSettleTimer.current) clearTimeout(interactionSettleTimer.current);
  }, []);

  useEffect(() => {
    if (!profileId) return;
    let canceled = false;
    setDraftReady(false);
    setText("");
    setAttachments([]);
    setPreferences(null);
    void loadDraft(profileId, draftScope).then((draft) => {
      if (canceled) return;
      if (draft) {
        setText(draft.text);
        setAttachments(draft.attachments);
        setPreferences(draft.settings);
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

  useEffect(() => {
    if (!activeTurnId) return;
    const reconciler = createActiveTurnReconciler(() => refreshThreadTail(sessionId));
    const applyAppState = (state: string) => {
      if (state === "active") reconciler.start();
      else reconciler.stop();
    };
    const subscription = AppState.addEventListener("change", applyAppState);
    applyAppState(AppState.currentState);
    return () => {
      subscription.remove();
      reconciler.dispose();
    };
  }, [activeTurnId, refreshThreadTail, sessionId]);

  const rows = useMemo(() => conversationRows(record?.thread.turns ?? [], requests),
    [record?.thread.turns, requests]);
  rowsRef.current = rows;
  const restorePosition = useMemo(() => resolveConversationPosition(savedPosition,
    rows.map((row) => row.key)), [rows, savedPosition]);
  const hasRestorableAnchor = restorePosition.kind === "anchor";
  const hasMoreHistory = record?.history.kind === "loaded" &&
    !record.history.hasLoadedOldest && record.history.olderCursor !== null;

  useEffect(() => {
    const nextPhase = latestTurnPhase(activeTurn);
    const previousPhase = latestPhase.current;
    latestPhase.current = nextPhase;
    if (previousPhase !== nextPhase) dispatchFollow({ type: "latest_turn_phase_changed",
      previousLatestTurnPhase: previousPhase, latestTurnPhase: nextPhase });
    if (shouldFollowLatest(followState.current) && !interactionBlocked.current) {
      list.current?.scrollToEnd({ animated: false });
    }
  }, [activeTurn, dispatchFollow, rows]);

  const saveVisiblePosition = useCallback((offsetY?: number) => {
    if (!positionKey) return;
    if (pinnedToLatest.current) {
      saveConversationPosition(positionKey, { kind: "latest" });
      return;
    }
    const current = list.current;
    if (!current) return;
    const index = current.getFirstVisibleIndex();
    const row = rowsRef.current[index];
    const layout = current.getLayout(index);
    if (!row || !layout) return;
    const scrollOffset = offsetY ?? current.getAbsoluteLastScrollOffset();
    saveConversationPosition(positionKey,
      { kind: "anchor", rowKey: row.key,
        topOffset: visibleRowTop(layout.y, current.getFirstItemOffset(), scrollOffset) });
  }, [positionKey]);

  const followLatest = useCallback((animated: boolean) => {
    dispatchFollow({ type: "scroll_to_bottom", latestTurnPhase: latestPhase.current });
    pinnedToLatest.current = true;
    setScrollToLatestVisible(false);
    if (positionKey) saveConversationPosition(positionKey, { kind: "latest" });
    list.current?.scrollToEnd({ animated });
  }, [dispatchFollow, positionKey, setScrollToLatestVisible]);

  const finishUserInteraction = useCallback(() => {
    interactionBlocked.current = false;
    if (pinnedToLatest.current) {
      dispatchFollow({ type: "user_reached_bottom", latestTurnPhase: latestPhase.current });
    }
    if (shouldFollowLatest(followState.current)) {
      list.current?.scrollToEnd({ animated: false });
    }
  }, [dispatchFollow]);

  const settleUserInteraction = useCallback(() => {
    if (interactionSettleTimer.current) clearTimeout(interactionSettleTimer.current);
    interactionSettleTimer.current = setTimeout(() => {
      interactionSettleTimer.current = null;
      if (!momentumScrolling.current && !userDragging.current) finishUserInteraction();
    }, 120);
  }, [finishUserInteraction]);

  const loadOlder = useCallback(async () => {
    olderLoadRequested.current = false;
    if (olderLoadTimer.current) clearTimeout(olderLoadTimer.current);
    olderLoadTimer.current = null;
    if (!hasMoreHistory || loadingOlder) return;
    historyPagingReady.current = false;
    setLoadingOlder(true);
    setOlderError(null);
    try {
      await loadOlderThread(sessionId);
    } catch (cause) {
      setOlderError(cause instanceof Error ? cause.message : "无法加载更早历史");
    } finally {
      setLoadingOlder(false);
    }
  }, [hasMoreHistory, loadOlderThread, loadingOlder, sessionId]);

  const requestOlderLoad = useCallback(() => {
    if (!historyPagingReady.current || !hasMoreHistory || loadingOlder) return;
    olderLoadRequested.current = true;
  }, [hasMoreHistory, loadingOlder]);

  const flushOlderLoad = useCallback(() => {
    if (!olderLoadRequested.current || momentumScrolling.current) return;
    olderLoadRequested.current = false;
    void loadOlder();
  }, [loadOlder]);

  const scheduleOlderLoad = useCallback(() => {
    if (!olderLoadRequested.current) return;
    if (olderLoadTimer.current) clearTimeout(olderLoadTimer.current);
    olderLoadTimer.current = setTimeout(() => {
      olderLoadTimer.current = null;
      flushOlderLoad();
    }, OLDER_LOAD_SETTLE_MS);
  }, [flushOlderLoad]);

  const send = useCallback(async () => {
    if (!resolvedPreferences || (!text.trim() && attachments.length === 0)) return;
    const message = text;
    const files = attachments;
    setSending(true);
    setText("");
    setAttachments([]);
    followLatest(false);
    try {
      const sent = await submitMessage(sessionId, message, files, resolvedPreferences);
      if (profileId) await clearDraft(profileId, draftScope);
      if (!sent) {
        setText(message);
        setAttachments(files);
        Alert.alert("已加入发送队列", "连接恢复后会自动发送这条消息。");
      }
    } catch (cause) {
      setText(message);
      setAttachments(files);
      Alert.alert("发送状态未确认", cause instanceof Error ? cause.message : "请刷新后重试");
    } finally {
      setSending(false);
    }
  }, [attachments, draftScope, followLatest, profileId, resolvedPreferences, sessionId,
    submitMessage, text]);

  const stop = useCallback(async () => {
    if (!activeTurnId || stopping) return;
    setStopping(true);
    try {
      await interruptThread(sessionId);
    } catch (cause) {
      Alert.alert("停止失败", cause instanceof Error ? cause.message : "请重试");
    } finally {
      setStopping(false);
    }
  }, [activeTurnId, interruptThread, sessionId, stopping]);

  const renderRow = useCallback(({ item }: { item: ConversationRow }) => item.kind === "turn"
    ? <OfficialTurn profileId={profileId ?? "unavailable"} threadId={sessionId} turn={item.turn}
      canToggleActivity={canToggleActivity} />
    : item.kind === "request"
      ? <ServerRequestCard request={item.request} onAnswer={(result) => {
        if (!answerRequest(sessionId, item.request.id, result)) {
          Alert.alert("请求已经处理", "这个请求已由其他连接回答，正在刷新官方状态。");
          void loadThread(sessionId);
        }
      }} />
      : null, [answerRequest, canToggleActivity, loadThread, profileId, sessionId]);

  if (!connection || !record) {
    return <EmptyState title="会话不可用" detail="它可能已被归档、移除，或属于其他连接。" />;
  }
  if (loading && record.thread.turns.length === 0) {
    return <EmptyState title="正在读取会话" detail="正在从 Codex App Server 加载官方历史。" />;
  }
  if (error && record.thread.turns.length === 0) {
    return <EmptyState title="无法加载会话" detail={error}
      action={<Button title="重试" onPress={() => void loadThread(sessionId)} />} />;
  }

  const plan = latestCompletedPlan(record.thread);
  return <KeyboardAvoidingView {...keyboardAvoidance(Platform.OS, insets.top, keyboardVisible)}
    style={styles.container}>
    <View style={styles.messageArea}>
      <FlashList key={sessionId} ref={list} testID="messages:list" data={rows}
        style={[styles.messageList, { opacity: hasRestorableAnchor && !positionRestored ? 0 : 1 }]}
        drawDistance={LIST_DRAW_DISTANCE}
        maxItemsInRecyclePool={LIST_RECYCLE_POOL_SIZE}
        removeClippedSubviews={Platform.OS === "android"}
        initialScrollIndex={restorePosition.kind === "anchor" ? restorePosition.index : undefined}
        keyExtractor={rowKey}
        getItemType={rowType}
        keyboardShouldPersistTaps="handled"
        renderItem={renderRow}
        contentContainerStyle={styles.list}
        maintainVisibleContentPosition={LIST_POSITIONING}
        scrollEventThrottle={100}
        onScroll={({ nativeEvent }) => {
          const state = conversationScrollState(nativeEvent.contentSize.height,
            nativeEvent.layoutMeasurement.height, nativeEvent.contentOffset.y);
          scrollOffset.current = nativeEvent.contentOffset.y;
          pinnedToLatest.current = state.distanceFromBottom <= 24;
          setScrollToLatestVisible(state.showLatest);
          if (userDragging.current || momentumScrolling.current) {
            dispatchFollow({ type: "scroll_distance_changed",
              distanceFromBottomPx: state.distanceFromBottom,
              latestTurnPhase: latestPhase.current });
            if (state.distanceFromBottom <= 24) {
              dispatchFollow({ type: "user_reached_bottom",
                latestTurnPhase: latestPhase.current });
            }
          }
        }}
        onScrollBeginDrag={() => {
          momentumScrolling.current = false;
          userDragging.current = true;
          interactionBlocked.current = true;
          if (interactionSettleTimer.current) clearTimeout(interactionSettleTimer.current);
          interactionSettleTimer.current = null;
          activityToggleBlockedUntil.current = Number.POSITIVE_INFINITY;
          if (olderLoadTimer.current) clearTimeout(olderLoadTimer.current);
          olderLoadTimer.current = null;
          historyPagingReady.current = true;
          if (scrollOffset.current <= 8) requestOlderLoad();
        }}
        onScrollEndDrag={() => {
          userDragging.current = false;
          activityToggleBlockedUntil.current = Date.now() + ACTIVITY_TOGGLE_SCROLL_SETTLE_MS;
          saveVisiblePosition();
          scheduleOlderLoad();
          settleUserInteraction();
        }}
        onMomentumScrollBegin={() => {
          momentumScrolling.current = true;
          interactionBlocked.current = true;
          if (interactionSettleTimer.current) clearTimeout(interactionSettleTimer.current);
          interactionSettleTimer.current = null;
          activityToggleBlockedUntil.current = Number.POSITIVE_INFINITY;
          if (olderLoadTimer.current) clearTimeout(olderLoadTimer.current);
          olderLoadTimer.current = null;
        }}
        onMomentumScrollEnd={() => {
          momentumScrolling.current = false;
          finishUserInteraction();
          activityToggleBlockedUntil.current = Date.now() + ACTIVITY_TOGGLE_SCROLL_SETTLE_MS;
          saveVisiblePosition();
          flushOlderLoad();
        }}
        onStartReached={() => {
          requestOlderLoad();
        }}
        onStartReachedThreshold={0.2}
        onLoad={() => {
          if (activeSessionId.current !== sessionId) return;
          if (restorePosition.kind !== "anchor") {
            pinnedToLatest.current = true;
            setScrollToLatestVisible(false);
            if (positionKey) saveConversationPosition(positionKey, { kind: "latest" });
            if (activeTurnId) dispatchFollow({ type: "scroll_to_bottom",
              latestTurnPhase: latestPhase.current });
            setPositionRestored(true);
            return;
          }
          const restore = list.current?.scrollToIndex({ index: restorePosition.index, animated: false,
            viewPosition: 0, viewOffset: anchorViewOffset(restorePosition.topOffset) });
          if (!restore) {
            followLatest(false);
            setPositionRestored(true);
            return;
          }
          void restore.catch(() => followLatest(false)).finally(() => {
            if (activeSessionId.current === sessionId) setPositionRestored(true);
          });
        }}
        ListHeaderComponent={loadingOlder
          ? <View style={styles.historyStatus}><ActivityIndicator size="small"
            color={theme.colors.textMuted} /></View>
          : olderError ? <Pressable testID="history:retry" style={styles.historyStatus}
            onPress={() => void loadOlder()}>
            <Text style={{ color: theme.colors.danger }}>加载更早消息失败，点按重试</Text>
          </Pressable> : null}
        ListFooterComponent={<>
          {plan && <View style={styles.planAction}>
            <Button testID="plan:execute" title="执行计划" loading={sending}
              disabled={!resolvedPreferences || sending}
              onPress={() => void (async () => {
                if (!resolvedPreferences) return;
                setSending(true);
                try {
                  await executePlan(sessionId, resolvedPreferences);
                  followLatest(false);
                } catch (cause) { Alert.alert("无法执行计划",
                  cause instanceof Error ? cause.message : "请刷新后重试"); }
                finally { setSending(false); }
              })()} />
          </View>}
        </>} />
      {showScrollToLatest && <Pressable testID="chat:scroll-to-latest"
        accessibilityRole="button" accessibilityLabel="回到最新消息"
        onPress={() => followLatest(true)} style={({ pressed }) => [styles.scrollToLatest, {
          backgroundColor: theme.colors.surface, borderColor: theme.colors.border,
          opacity: pressed ? 0.72 : 1,
        }, theme.shadow]}>
        <Text style={[styles.scrollToLatestIcon, { color: theme.colors.text }]}>↓</Text>
      </Pressable>}
    </View>
    {queued.length > 0 && <Pressable testID="chat:retry-outbox" style={[styles.outbox, {
      backgroundColor: theme.colors.surfaceAlt }]} onPress={() => {
        const failed = queued.find((item) => item.state === "failed");
        if (!failed) { void retryOutbox(); return; }
        Alert.alert("消息发送失败", failed.error ?? "连接恢复后可重试", [
          { text: "取消", style: "cancel" },
          { text: "丢弃", style: "destructive",
            onPress: () => void discardOutbox(failed.clientMessageId) },
          { text: "重试", onPress: () => void retryOutbox(failed.clientMessageId) },
        ]);
      }}>
      <Text style={{ color: queued.some((item) => item.state === "failed")
        ? theme.colors.danger : theme.colors.textMuted }}>
        {queued.some((item) => item.state === "failed")
          ? `${queued.length} 条消息等待处理，点按查看`
          : `${queued.length} 条消息正在等待网络`}
      </Text>
    </Pressable>}
    <ChatComposer value={text} onChange={setText} attachments={attachments}
      onAttachmentsChange={setAttachments} onParameters={() => {
        if (!resolvedPreferences) {
          Alert.alert("参数暂不可用", "Codex App Server 没有返回可用模型。");
          return;
        }
        setBeforeSheet(preferences); setPreferences(resolvedPreferences); setShowParameters(true);
      }} onSend={() => void send()} onStop={() => void stop()} active={activeTurnId !== null}
      sending={sending} stopping={stopping}
      parameterLabel={resolvedPreferences
        ? `${resolvedPreferences.model} · ${resolvedPreferences.effort ?? "默认"} · ${resolvedPreferences.collaborationMode === "plan" ? "先做计划" : "直接执行"}`
        : "参数暂不可用"} />
    {resolvedPreferences && <ParameterSheet visible={showParameters} models={models}
      value={resolvedPreferences} onChange={setPreferences} onClose={() => setShowParameters(false)}
      onCancel={() => { setPreferences(beforeSheet); setShowParameters(false); }} />}
  </KeyboardAvoidingView>;
}

const styles = StyleSheet.create({
  container: { flex: 1 },
  messageArea: { flex: 1, minHeight: 0 },
  messageList: { flex: 1 },
  list: { paddingVertical: 8 },
  historyStatus: { minHeight: 40, alignItems: "center", justifyContent: "center",
    paddingHorizontal: 16 },
  planAction: { paddingHorizontal: 16, paddingTop: 8 },
  outbox: { minHeight: 34, marginHorizontal: 12, marginBottom: 6, borderRadius: 8,
    paddingHorizontal: 10, alignItems: "center", justifyContent: "center" },
  scrollToLatest: { position: "absolute", right: 16, bottom: 12, width: 42, height: 42,
    borderRadius: 21, borderWidth: StyleSheet.hairlineWidth, alignItems: "center",
    justifyContent: "center", elevation: 5 },
  scrollToLatestIcon: { fontFamily: "Inter_500Medium", fontSize: 22, lineHeight: 26 },
});
